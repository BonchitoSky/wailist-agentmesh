package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/agentmesh/backend/internal/models"
)

// relayHTTPClient is used only for the two calls to our own /x402/relay
// endpoint (the quote fetch and the paid retry). It shares toolHTTPClient's
// SSRF-safe dialer (dialFn, tool.go) but needs a longer timeout: the relay
// handler's own worst-case latency for a single request can exceed
// toolHTTPClient's 10s budget by itself — up to ~10s re-fetching the
// target's quote, up to 20s each for the facilitator's Verify and Settle
// calls (internal/x402/facilitator.go), plus the outbound payment request
// to target (another ~10s budget). A caller-side timeout shorter than the
// callee's own worst case means "the orchestrator gave up waiting" and
// "the inbound leg genuinely never settled" become indistinguishable from
// a transport error, which is unsafe to resolve either way: assuming
// settlement risks billing for a payment that never happened, and assuming
// no settlement risks never billing for one that did (the underlying
// vector the X-Inbound-Settled header exists to close). Sized with
// headroom above the relay's own worst case rather than exactly matching
// it, so ordinary slow-but-real responses aren't cut off right at the
// boundary.
var relayHTTPClient = &http.Client{
	Timeout: 90 * time.Second,
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialFn(ctx, network, addr)
		},
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if err := validateURL(req.URL.String()); err != nil {
			return err
		}
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	},
}

// WalletSigner signs and submits an Algorand payment transaction.
// Satisfied by *wallet.Service.
type WalletSigner interface {
	SignAndSendPayment(ctx context.Context, encMnemonic, toAddress string, microAlgo uint64) (string, error)
}

func QuoteX402(ctx context.Context, rawURL string) (map[string]any, error) {
	if err := urlValidator(rawURL); err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Always attempt to parse payment info from header+body regardless of status.
	// Proxies (Cloudflare tunnels) may rewrite 402 → 200/503 or strip headers.
	quote := parsePaymentHeader(resp)
	if _, hasPrice := quote["price"]; hasPrice {
		return quote, nil
	}
	return map[string]any{"price": "0", "unit": "", "network": "", "recipient": ""}, nil
}

func ExecuteTool402(ctx context.Context, node models.WorkflowNode, rc RunContexter, wallet models.AgentWallet, signer WalletSigner) (any, error) {
	if err := urlValidator(node.Endpoint); err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, node.Endpoint, nil)
	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusPaymentRequired {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, httpResponseLimit))
		var result any
		if json.Unmarshal(b, &result) == nil {
			return result, nil
		}
		return string(b), nil
	}

	quote := parsePaymentHeader(resp) // reads body internally
	resp.Body.Close()

	if wallet.EncryptedMnemonic == "" || signer == nil {
		return map[string]any{"error": "payment required but no agent wallet configured", "quote": quote}, nil
	}

	priceStr, _ := quote["price"].(string)
	recipient, _ := quote["recipient"].(string)
	if recipient == "" {
		return nil, fmt.Errorf("x402: no recipient address in payment header")
	}
	priceFloat, err := strconv.ParseFloat(priceStr, 64)
	if err != nil || priceFloat <= 0 {
		return nil, fmt.Errorf("x402: invalid price %q", priceStr)
	}
	microAlgo := uint64(priceFloat * 1e6)

	txID, err := signer.SignAndSendPayment(ctx, wallet.EncryptedMnemonic, recipient, microAlgo)
	if err != nil {
		return nil, fmt.Errorf("x402 payment failed: %w", err)
	}

	algoAmount := fmt.Sprintf("%.6f", float64(microAlgo)/1e6)
	explorerURL := "https://lora.algokit.io/testnet/transaction/" + txID

	// Retry the original request with the payment proof header.
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, node.Endpoint, nil)
	req2.Header.Set("X-Payment-Txid", txID)
	resp2, err := toolHTTPClient.Do(req2)
	if err != nil {
		return map[string]any{"status": "payment_sent", "txId": txID, "amount": algoAmount, "explorerURL": explorerURL, "error": "retry request failed: " + err.Error()}, nil
	}
	defer resp2.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp2.Body, httpResponseLimit))
	var retryResult any
	if json.Unmarshal(b, &retryResult) == nil {
		return map[string]any{"status": "payment_sent", "txId": txID, "amount": algoAmount, "explorerURL": explorerURL, "response": retryResult}, nil
	}
	return map[string]any{"status": "payment_sent", "txId": txID, "amount": algoAmount, "explorerURL": explorerURL, "response": string(b)}, nil
}

func parsePaymentHeader(resp *http.Response) map[string]any {
	// Try header first (direct connections). Cloudflare and other proxies may
	// strip non-standard response headers, so fall back to the response body.
	header := resp.Header.Get("X-Payment-Required")
	if header == "" {
		header = resp.Header.Get("WWW-Authenticate")
	}
	var result map[string]any
	if header != "" {
		if err := json.Unmarshal([]byte(header), &result); err == nil {
			return result
		}
	}
	// Body fallback: our server returns {"error":"Payment required","payment":{...}}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, httpResponseLimit))
	var envelope struct {
		Payment map[string]any `json:"payment"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Payment != nil {
		return envelope.Payment
	}
	// Last resort: try parsing body directly as the payment object
	if err := json.Unmarshal(body, &result); err == nil {
		return result
	}
	return map[string]any{"raw": header}
}

// USDCGroupSigner signs a gasless USDC atomic-payment group for the relay's
// X-Payment header. Satisfied by *wallet.Service (SignUSDCPaymentGroup).
type USDCGroupSigner interface {
	SignUSDCPaymentGroup(ctx context.Context, encMnemonic, payTo string, assetID, amountMicros uint64, feePayerAddr string) ([]string, int, error)
}

// X402RelayConfig bundles what an agent-attached tool402 call needs to route
// through the AgentMesh relay (Wallet 1) exactly the way a standalone
// tool402 node already does via ExecuteTool402V2. Threaded from Runner
// through ExecuteAgent -> callOpenAICompat/callGemini -> executeFunctionCall.
type X402RelayConfig struct {
	USDCSigner               USDCGroupSigner
	PlatformSpendEncMnemonic string
	ExpectedAssetID          uint64
	RelayBaseURL             string
	// Ledger reserves/commits/releases credits for real on-chain tool402
	// payments made through this config. See PaymentLedger.
	Ledger PaymentLedger
}

// Tool402PaymentResult is what ExecuteTool402V2 returns. SettledUSDMicros
// and DebitKind describe a charge that has ALREADY been committed via the
// caller-supplied PaymentLedger by the time this returns — callers report
// these for logging/audit purposes and must not debit again. Both are zero
// when no payment was sent (e.g. the endpoint didn't require one, no wallet
// was configured, or a reservation was taken but released because the
// payment never actually settled).
type Tool402PaymentResult struct {
	Response         any
	SettledUSDMicros int64
	DebitKind        string
}

// ExecuteTool402V2 is the entry point runner.go calls for tool402 nodes. It
// inspects the target's 402 quote shape: a real x402 v2 challenge (accepts[])
// is routed through the AgentMesh relay so both payment legs are real,
// GoPlausible-settled, and attributable to us as an orchestrator entry, paid
// from the platform's own Wallet 1 spend wallet and gated/charged against
// the triggering user's credits for the real settled amount. The legacy
// flat-quote dialect (no accepts[]) bypasses the relay entirely and keeps
// today's direct-pay-from-the-agent's-own-wallet behavior, gated/charged at
// the fixed platform fee — it was never GoPlausible-compliant and isn't
// becoming so.
func ExecuteTool402V2(ctx context.Context, node models.WorkflowNode, rc RunContexter, aw models.AgentWallet, signer WalletSigner, usdcSigner USDCGroupSigner, platformSpendEncMnemonic string, expectedAssetID uint64, relayBaseURL string, ledger PaymentLedger) (Tool402PaymentResult, error) {
	if err := urlValidator(node.Endpoint); err != nil {
		return Tool402PaymentResult{}, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, node.Endpoint, nil)
	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return Tool402PaymentResult{}, err
	}

	if resp.StatusCode != http.StatusPaymentRequired {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, httpResponseLimit))
		var result any
		if json.Unmarshal(b, &result) == nil {
			return Tool402PaymentResult{Response: result}, nil
		}
		return Tool402PaymentResult{Response: string(b)}, nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, httpResponseLimit))
	resp.Body.Close()
	var v2Challenge struct {
		Accepts []map[string]any `json:"accepts"`
	}
	if json.Unmarshal(body, &v2Challenge) == nil && len(v2Challenge.Accepts) > 0 {
		return executeTool402V2Relay(ctx, node, usdcSigner, platformSpendEncMnemonic, expectedAssetID, relayBaseURL, ledger)
	}

	// Legacy flat-quote dialect: unchanged direct-pay path, flat-fee billing,
	// paid from the agent's own wallet (not Wallet 1). If no wallet is
	// configured, ExecuteTool402 degrades gracefully without attempting a
	// payment at all — check that first so a reservation is never taken for
	// a call that can't possibly pay.
	if aw.EncryptedMnemonic == "" || signer == nil {
		result, err := ExecuteTool402(ctx, node, rc, aw, signer)
		if err != nil {
			return Tool402PaymentResult{}, err
		}
		return Tool402PaymentResult{Response: result}, nil
	}
	if reserve := ledger.Reserve; reserve != nil {
		if err := reserve(ctx, models.X402PlatformFeeUSDMicros); err != nil {
			return Tool402PaymentResult{}, &ErrBalanceBlocked{Err: err}
		}
	}
	// settled tracks whether the reservation above has already been
	// resolved (via Commit or Release) by the normal control flow below. If
	// a panic unwinds through ExecuteTool402 before that happens (any
	// runtime panic, not just an explicit error return), the balance would
	// otherwise stay permanently decremented with no debit_ledger row and
	// no way to reconcile it -- releasing here on the way out (before
	// re-panicking, so the original panic/stack trace still propagates and
	// this is not mistaken for a handled error) closes that window.
	settled := false
	defer func() {
		if !settled {
			if release := ledger.Release; release != nil {
				release(ctx, models.X402PlatformFeeUSDMicros)
			}
		}
	}()
	result, err := ExecuteTool402(ctx, node, rc, aw, signer)
	if err != nil {
		settled = true
		if release := ledger.Release; release != nil {
			release(ctx, models.X402PlatformFeeUSDMicros)
		}
		return Tool402PaymentResult{}, err
	}
	out := Tool402PaymentResult{Response: result}
	if m, ok := result.(map[string]any); ok {
		if _, hasTx := m["txId"]; hasTx {
			out.SettledUSDMicros = models.X402PlatformFeeUSDMicros
			out.DebitKind = models.DebitKindX402PlatformFee
			settled = true
			if commit := ledger.Commit; commit != nil {
				commit(ctx, node.ID, models.X402PlatformFeeUSDMicros, models.DebitKindX402PlatformFee)
			}
			return out, nil
		}
	}
	// No payment was actually sent (e.g. the retried request came back
	// without a txId) -- release the reservation, nothing to charge for.
	settled = true
	if release := ledger.Release; release != nil {
		release(ctx, models.X402PlatformFeeUSDMicros)
	}
	return out, nil
}

func executeTool402V2Relay(ctx context.Context, node models.WorkflowNode, usdcSigner USDCGroupSigner, platformSpendEncMnemonic string, expectedAssetID uint64, relayBaseURL string, ledger PaymentLedger) (Tool402PaymentResult, error) {
	if platformSpendEncMnemonic == "" || usdcSigner == nil {
		return Tool402PaymentResult{Response: map[string]any{"error": "payment required but no platform spend wallet configured"}}, nil
	}

	relayURL := relayBaseURL + "/x402/relay?target=" + url.QueryEscape(node.Endpoint)

	quoteReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, relayURL, nil)
	quoteResp, err := relayHTTPClient.Do(quoteReq)
	if err != nil {
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay quote failed: %w", err)
	}
	quoteBody, _ := io.ReadAll(io.LimitReader(quoteResp.Body, httpResponseLimit))
	quoteResp.Body.Close()

	var relayChallenge struct {
		Accepts []map[string]any `json:"accepts"`
	}
	if json.Unmarshal(quoteBody, &relayChallenge) != nil || len(relayChallenge.Accepts) == 0 {
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay: invalid challenge response")
	}
	accept := relayChallenge.Accepts[0]
	payTo, _ := accept["payTo"].(string)
	assetStr, _ := accept["asset"].(string)
	amountStr, _ := accept["maxAmountRequired"].(string)
	var feePayer string
	if extra, ok := accept["extra"].(map[string]any); ok {
		feePayer, _ = extra["feePayer"].(string)
	}
	assetID, err := strconv.ParseUint(assetStr, 10, 64)
	if err != nil {
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay: invalid asset id %q", assetStr)
	}
	if assetID != expectedAssetID {
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay: unexpected asset id %d, want %d", assetID, expectedAssetID)
	}
	amount, err := strconv.ParseUint(amountStr, 10, 64)
	if err != nil || amount == 0 || amount > math.MaxInt64 {
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay: invalid settlement amount %q", amountStr)
	}

	// USDC's 6 decimals match credit_balance_usd_micros' scale exactly —
	// the relay's asset base-unit amount converts to USD micros 1:1.
	//
	// Reserve (atomically decrement) the exact amount now, before signing —
	// not just check it — so a second call racing this one (another
	// iteration of the same agent's tool loop, or a concurrent standalone
	// tool402 node) can't also pass a check against the same stale balance
	// and cause the platform to pay out more than the user can cover in
	// aggregate.
	if reserve := ledger.Reserve; reserve != nil {
		if err := reserve(ctx, int64(amount)); err != nil {
			return Tool402PaymentResult{}, &ErrBalanceBlocked{Err: err}
		}
	}
	// settled tracks whether the reservation above has already been
	// resolved (via Commit or Release) by the normal control flow below —
	// see the identical pattern and rationale in ExecuteTool402V2's legacy
	// branch above. Covers a panic unwinding through the signing call, the
	// relay HTTP round trip, or response parsing before Commit/Release runs.
	settled := false
	defer func() {
		if !settled {
			if release := ledger.Release; release != nil {
				release(ctx, int64(amount))
			}
		}
	}()
	releaseReservation := func() {
		settled = true
		if release := ledger.Release; release != nil {
			release(ctx, int64(amount))
		}
	}

	group, idx, err := usdcSigner.SignUSDCPaymentGroup(ctx, platformSpendEncMnemonic, payTo, assetID, amount, feePayer)
	if err != nil {
		releaseReservation()
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay payment signing failed: %w", err)
	}
	xPayment, _ := json.Marshal(map[string]any{
		"x402Version": 2, "scheme": "exact",
		"payload": map[string]any{"paymentGroup": group, "paymentIndex": idx},
	})

	payReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, relayURL, nil)
	payReq.Header.Set("X-Payment", string(xPayment))
	payResp, err := relayHTTPClient.Do(payReq)
	if err != nil {
		releaseReservation()
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay payment request failed: %w", err)
	}
	defer payResp.Body.Close()
	finalBody, _ := io.ReadAll(io.LimitReader(payResp.Body, httpResponseLimit))

	out := Tool402PaymentResult{}
	var result any
	if json.Unmarshal(finalBody, &result) == nil {
		out.Response = result
	} else {
		out.Response = string(finalBody)
	}
	// Bill based on X-Inbound-Settled, not the relay's overall HTTP status.
	// The relay only sets this header once both (a) the inbound leg (Wallet
	// 1 -> Wallet 2) has irreversibly settled via the facilitator, and (b) a
	// real signed outbound payment group now exists as a submittable claim
	// (see x402relay.go's payTargetAndRespond) — so a signing failure on the
	// platform's side never bills the caller, but once a group is signed the
	// outbound leg to the caller-controlled target can still fail afterward
	// (the target errors, or rejects) with no refund path, and that must
	// still bill: gating on the final composite status instead would let a
	// malicious target accept payment and then deliberately return a
	// non-2xx response to avoid ever being billed, while still being paid.
	if payResp.Header.Get("X-Inbound-Settled") == "true" {
		out.SettledUSDMicros = int64(amount)
		out.DebitKind = models.DebitKindX402RelayCost
		settled = true
		if commit := ledger.Commit; commit != nil {
			commit(ctx, node.ID, int64(amount), models.DebitKindX402RelayCost)
		}
	} else {
		releaseReservation()
	}
	return out, nil
}
