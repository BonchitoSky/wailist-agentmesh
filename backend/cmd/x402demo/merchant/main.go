// Command merchant runs the x402-demo's "Standard" leaderboard entry: a
// single paid Algorand x402 endpoint, GoPlausible-facilitated, priced in
// testnet (or mainnet, once NETWORK=mainnet) USDC.
//
// Two routes, same price, same payTo, same 402/verify/settle gate:
//
//   - POST /resume-screen — the real, Prism-schema entry (see
//     https://prism-99h2.onrender.com/resume-screen-accurate). task_description
//   - files[] in, ranked candidates[] out. This is what should be
//     registered with Bazaar/the leaderboard.
//   - GET /resume-screen-demo — a zero-argument variant of the exact same
//     paid resource, for quick manual testing with no request body to
//     construct — same scorer, same price, same wallet, just a fixed
//     canned request instead of a client-supplied body. AgentMesh's own
//     "load demo workflow" button uses POST /resume-screen instead, now
//     that the engine supports POST+body tool402 nodes (added 2026-07-31).
//
// Run from the backend/ directory: go run ./cmd/x402demo/merchant
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
)

const (
	resourcePath     = "/resume-screen"
	demoResourcePath = "/resume-screen-demo"
)

// demoRequest is the fixed, canned input the GET demo route always scores —
// same data the standalone Go client driver uses, so both demo paths show
// the same result.
var demoRequest = demolib.ScreenRequest{
	TaskDescription: "Senior React Frontend Developer with 5+ years of experience",
	Files: []demolib.ResumeFile{
		{Filename: "jane_doe_resume.txt", Text: "Jane Doe. Senior Frontend Engineer with 6 years building React, TypeScript, and Next.js applications. Led migration to Redux Toolkit and Tailwind CSS."},
		{Filename: "sam_lee_resume.txt", Text: "Sam Lee. Backend engineer specializing in Go, PostgreSQL, and gRPC microservices. Some exposure to Kubernetes and Terraform."},
	},
}

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
	if cfg.MerchantWallet3Address == "" {
		log.Fatal("MERCHANT_WALLET3_ADDRESS is required — see cmd/x402demo/README.md")
	}

	// The merchant only ever receives payment — it never signs — so it
	// needs no wallet.Service, unlike orchestrator and client.
	facilitator := demolib.NewFacilitatorClient(cfg.FacilitatorURL)

	reqs := demolib.PaymentRequirements{
		Scheme:            "exact",
		Network:           cfg.CAIP2Network,
		Amount:            strconv.FormatUint(cfg.MerchantPriceUSDCMicros, 10),
		Asset:             strconv.FormatUint(cfg.USDCAssetID, 10),
		PayTo:             cfg.MerchantWallet3Address,
		MaxTimeoutSeconds: 300,
		Extra: map[string]any{
			"asset":    cfg.USDCAssetID,
			"tag":      "x402-global-challenge",
			"decimals": 6,
			"feePayer": cfg.FeePayer,
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"service":         "x402-demo merchant",
			"network":         cfg.Network,
			"resource":        cfg.PublicMerchantURL + resourcePath,
			"demoResource":    cfg.PublicMerchantURL + demoResourcePath,
			"priceUsdcMicros": cfg.MerchantPriceUSDCMicros,
			"payTo":           cfg.MerchantWallet3Address,
		})
	})

	mux.HandleFunc(resourcePath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		// Payment status must be checked BEFORE the body is ever parsed, not
		// after: a real x402 client's unauthenticated probe (learning the
		// price) legitimately sends no body at all (or a placeholder one),
		// and must still get a normal 402 back, not a 400 for a body it was
		// never obligated to send yet. Parsing here first was a real bug,
		// caught live: it 400'd the production engine's own unauthenticated
		// probe (see tool402.go's probeTool402Endpoint, which always probes
		// with a nil body) before the request ever reached the payment gate
		// below. The real body is only read -- and only needs to be valid --
		// once serveGated has confirmed a real settlement, inside run().
		bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 15<<20))
		if err != nil {
			http.Error(w, "failed to read request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		serveGated(w, r, facilitator, reqs, cfg.PublicMerchantURL+resourcePath,
			"Screens a batch of resumes against a target job description and returns ranked candidates with match scores, key skills, and role suggestions.",
			cfg, func() (demolib.ScreenResponse, error) {
				var reqBody demolib.ScreenRequest
				if err := json.Unmarshal(bodyBytes, &reqBody); err != nil {
					return demolib.ScreenResponse{}, fmt.Errorf("invalid JSON body: %w", err)
				}
				return demolib.ScreenResumes(reqBody), nil
			})
	})

	mux.HandleFunc(demoResourcePath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		serveGated(w, r, facilitator, reqs, cfg.PublicMerchantURL+demoResourcePath,
			"Fixed-input demo of the resume-screening resource (no request body needed) — a zero-config GET variant for quick manual testing.",
			cfg, func() (demolib.ScreenResponse, error) { return demolib.ScreenResumes(demoRequest), nil })
	})

	addr := ":" + strconv.Itoa(cfg.MerchantPort)
	log.Printf("[merchant] listening on %s (network=%s)", addr, cfg.Network)
	log.Printf("[merchant] resource:      %s", cfg.PublicMerchantURL+resourcePath)
	log.Printf("[merchant] demo resource: %s", cfg.PublicMerchantURL+demoResourcePath)
	log.Printf("[merchant] payTo (Wallet 3): %s", cfg.MerchantWallet3Address)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// serveGated runs the shared 402 / verify / settle gate for one resource,
// then calls run() and writes its result as the paid response. run is only
// ever invoked after a real facilitator settlement succeeds -- so any body
// parsing run() itself does (e.g. the POST route's JSON decode) happens
// strictly after payment status is known, never before it. An error from
// run() at that point means real money already settled but the caller's
// body was invalid: reported as 400, not silently swallowed and not
// refunded (x402 has no chargeback primitive, same posture as every other
// post-settlement failure in this package).
func serveGated(w http.ResponseWriter, r *http.Request, facilitator *demolib.FacilitatorClient, reqs demolib.PaymentRequirements, resourceURL, description string, cfg demolib.Config, run func() (demolib.ScreenResponse, error)) {
	challenge := demolib.PaymentRequired{
		X402Version: 2,
		Resource:    demolib.ResourceInfo{URL: resourceURL, Description: description, MimeType: ""},
		Accepts:     []demolib.PaymentRequirements{reqs},
		Extensions: map[string]any{
			"bazaar": map[string]any{
				"info": map[string]any{
					"output": map[string]any{
						"candidates": "array<{ name, match_score, role, key_skills, primary_domain, alternative_roles, reason }>",
						"confidence": "number 0..1",
					},
				},
			},
		},
	}
	challengeJSON, err := json.Marshal(challenge)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to encode challenge"})
		return
	}

	xPayment := r.Header.Get(demolib.PaymentSignatureHeader)
	if xPayment == "" {
		w.Header().Set("Payment-Required", base64.StdEncoding.EncodeToString(challengeJSON))
		writeJSON(w, http.StatusPaymentRequired, challenge)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
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

	settleResult, err := facilitator.Settle(ctx, payload, reqs)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "facilitator settle failed: " + err.Error()})
		return
	}
	if !settleResult.Success {
		writeJSON(w, http.StatusPaymentRequired, map[string]string{"error": "settlement failed: " + settleResult.ErrorReason})
		return
	}

	log.Printf("[merchant] SETTLED tx=%s network=%s payer=%s amount=%s (%s) resource=%s",
		settleResult.Transaction, settleResult.Network, settleResult.Payer, settleResult.Amount, cfg.ExplorerURL(settleResult.Transaction), resourceURL)

	if settleHeader, err := demolib.EncodeSettleResponseHeader(settleResult); err != nil {
		log.Printf("[merchant] WARNING: failed to encode settlement header: %v", err)
	} else {
		w.Header().Set(demolib.PaymentResponseHeader, settleHeader)
	}

	result, err := run()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
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
