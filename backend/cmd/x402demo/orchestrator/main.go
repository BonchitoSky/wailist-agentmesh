// Command orchestrator runs the x402-demo's "Orchestrator" leaderboard
// entry: it is itself a paid x402 endpoint (payTo = Wallet 2) that, once
// paid, becomes an x402 client and pays a second, real x402 endpoint (the
// merchant, Wallet 3) before returning a combined result. This is
// AgentMesh's Wallet 1 -> Wallet 2 -> target pattern, standalone and
// separate from AgentMesh's own production platform wallets.
//
// Run from the backend/ directory: go run ./cmd/x402demo/orchestrator
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/agentmesh/backend/cmd/x402demo/demolib"
	"github.com/agentmesh/backend/internal/wallet"
)

const resourcePath = "/orchestrate-resume-screen"

func main() {
	envPath := flag.String("env", "cmd/x402demo/.env", "path to the x402-demo .env file")
	flag.Parse()

	if err := demolib.LoadDotEnv(*envPath); err != nil {
		log.Fatalf("load %s: %v", *envPath, err)
	}
	cfg, err := demolib.LoadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.OrchWallet2Address == "" || cfg.OrchWallet2EncMnemonic == "" {
		log.Fatal("ORCH_WALLET2_ADDRESS and ORCH_WALLET2_ENC_MNEMONIC are required — see cmd/x402demo/README.md")
	}

	walletSvc := wallet.NewService(cfg.EncryptionKey, cfg.AlgodURL, cfg.AlgodToken, string(cfg.Network))
	facilitator := demolib.NewFacilitatorClient(cfg.FacilitatorURL)
	resourceURL := cfg.PublicOrchestratorURL + resourcePath
	merchantURL := cfg.PublicMerchantURL + "/resume-screen"

	reqs := demolib.PaymentRequirements{
		Scheme:            "exact",
		Network:           cfg.CAIP2Network,
		Amount:            strconv.FormatUint(cfg.OrchestratorPriceUSDCMicros, 10),
		Asset:             strconv.FormatUint(cfg.USDCAssetID, 10),
		PayTo:             cfg.OrchWallet2Address,
		MaxTimeoutSeconds: 300,
		Extra: map[string]any{
			"asset":    cfg.USDCAssetID,
			"tag":      "x402-global-challenge",
			"decimals": 6,
			"feePayer": cfg.FeePayer,
		},
	}
	challenge := demolib.PaymentRequired{
		X402Version: 2,
		Resource: demolib.ResourceInfo{
			URL:         resourceURL,
			Description: "Orchestrator entry: settles your payment, then pays a downstream resume-screening x402 endpoint on your behalf and returns its result plus both settlement tx ids.",
			MimeType:    "",
		},
		Accepts: []demolib.PaymentRequirements{reqs},
		Extensions: map[string]any{
			"bazaar": map[string]any{
				"info": map[string]any{
					"input":  map[string]any{"task_description": "string", "files": "array"},
					"output": map[string]any{"candidates": "array", "settlement": "object — inbound/outbound tx ids"},
				},
				"orchestrator": map[string]any{"target": merchantURL},
			},
		},
	}
	challengeJSON, err := json.Marshal(challenge)
	if err != nil {
		log.Fatalf("marshal challenge: %v", err)
	}
	challengeHeader := base64.StdEncoding.EncodeToString(challengeJSON)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"service":         "x402-demo orchestrator",
			"network":         cfg.Network,
			"resource":        resourceURL,
			"priceUsdcMicros": cfg.OrchestratorPriceUSDCMicros,
			"payTo":           cfg.OrchWallet2Address,
			"pays":            merchantURL,
		})
	})

	mux.HandleFunc(resourcePath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}

		bodyBytes, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}

		xPayment := r.Header.Get(demolib.PaymentSignatureHeader)
		if xPayment == "" {
			w.Header().Set("Payment-Required", challengeHeader)
			writeJSON(w, http.StatusPaymentRequired, challenge)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()

		payload, err := decodePaymentPayload(xPayment)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid X-Payment header: " + err.Error()})
			return
		}

		verifyResult, err := facilitator.Verify(ctx, payload, reqs)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "facilitator verify failed: " + err.Error()})
			return
		}
		if !verifyResult.IsValid {
			writeJSON(w, http.StatusPaymentRequired, map[string]string{"error": "payment invalid: " + verifyResult.InvalidReason})
			return
		}

		inboundSettle, err := facilitator.Settle(ctx, payload, reqs)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "facilitator settle failed: " + err.Error()})
			return
		}
		if !inboundSettle.Success {
			writeJSON(w, http.StatusPaymentRequired, map[string]string{"error": "settlement failed: " + inboundSettle.ErrorReason})
			return
		}
		log.Printf("[orchestrator] INBOUND SETTLED tx=%s payer=%s amount=%s (%s)",
			inboundSettle.Transaction, inboundSettle.Payer, inboundSettle.Amount, cfg.ExplorerURL(inboundSettle.Transaction))

		// Inbound leg collected — now pay the merchant out of Wallet 2,
		// in-process, as a real x402 client. A network/merchant failure here
		// does not unwind the inbound settlement (it already irreversibly
		// happened on-chain); report it as a 502 with the inbound tx id
		// still attached, same "money already moved, no chargeback" posture
		// AgentMesh's own production relay uses.
		var screenReq demolib.ScreenRequest
		if err := json.Unmarshal(bodyBytes, &screenReq); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":            "invalid request body: " + err.Error(),
				"inboundSettledTx": inboundSettle.Transaction,
			})
			return
		}

		// The inbound settle above just confirmed on-chain, but a facilitator
		// simulating the very next (outbound) payment can still be reading a
		// round behind -- observed live on testnet: an immediate outbound
		// attempt was rejected with "underflow ... sender amount 0" against a
		// wallet that had, moments earlier, genuinely received the funds.
		// Wait for Wallet 2's own balance to actually reflect the inbound
		// settle before spending it.
		orchPriceAmount, _ := strconv.ParseUint(reqs.Amount, 10, 64)
		if waitErr := demolib.WaitForAssetBalance(ctx, cfg.AlgodURL, cfg.AlgodToken, cfg.OrchWallet2Address, cfg.USDCAssetID, orchPriceAmount, 15*time.Second); waitErr != nil {
			log.Printf("[orchestrator] WARNING: balance wait failed, attempting outbound anyway: %v", waitErr)
		}

		result, err := demolib.PayAndFetch(ctx, walletSvc, cfg.OrchWallet2EncMnemonic, cfg, http.MethodPost, merchantURL, screenReq)
		if err == nil && result.StatusCode >= 300 {
			err = fmt.Errorf("merchant returned status %d: %s", result.StatusCode, string(result.Body))
		}
		if err != nil {
			log.Printf("[orchestrator] outbound leg to merchant failed after inbound settled (tx=%s): %v", inboundSettle.Transaction, err)
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error":            "downstream merchant call failed: " + err.Error(),
				"inboundSettledTx": inboundSettle.Transaction,
			})
			return
		}
		log.Printf("[orchestrator] OUTBOUND SETTLED tx=%s payer=%s amount=%s (%s)",
			result.Settlement.Transaction, result.Settlement.Payer, result.Settlement.Amount, cfg.ExplorerURL(result.Settlement.Transaction))

		var merchantBody map[string]any
		if err := json.Unmarshal(result.Body, &merchantBody); err != nil {
			merchantBody = map[string]any{"raw": string(result.Body)}
		}
		merchantBody["settlement"] = map[string]any{
			"inbound": map[string]any{
				"txId":        inboundSettle.Transaction,
				"network":     inboundSettle.Network,
				"payer":       inboundSettle.Payer,
				"amount":      inboundSettle.Amount,
				"explorerURL": cfg.ExplorerURL(inboundSettle.Transaction),
			},
			"outbound": map[string]any{
				"txId":        result.Settlement.Transaction,
				"network":     result.Settlement.Network,
				"payer":       result.Settlement.Payer,
				"amount":      result.Settlement.Amount,
				"explorerURL": cfg.ExplorerURL(result.Settlement.Transaction),
			},
		}

		if settleHeader, err := demolib.EncodeSettleResponseHeader(inboundSettle); err == nil {
			w.Header().Set(demolib.PaymentResponseHeader, settleHeader)
		}
		writeJSON(w, http.StatusOK, merchantBody)
	})

	addr := ":" + strconv.Itoa(cfg.OrchestratorPort)
	log.Printf("[orchestrator] listening on %s (network=%s)", addr, cfg.Network)
	log.Printf("[orchestrator] resource: %s", resourceURL)
	log.Printf("[orchestrator] payTo (Wallet 2): %s", cfg.OrchWallet2Address)
	log.Printf("[orchestrator] pays downstream: %s", merchantURL)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func decodePaymentPayload(header string) (demolib.PaymentPayload, error) {
	var payload demolib.PaymentPayload
	raw, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		return payload, err
	}
	err = json.Unmarshal(raw, &payload)
	return payload, err
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
