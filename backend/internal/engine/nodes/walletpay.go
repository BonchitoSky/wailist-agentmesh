package nodes

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
)

// Wallet2PayConfig carries what's needed to sign and send a real outbound
// x402 payment from the platform's Wallet 2 settlement wallet to a real
// target. Used by BOTH the public relay handler (x402relay.go's
// payTargetAndRespond, for genuine external x402 clients) and the run-level
// in-process path (Task 5) — there is exactly one function that signs from
// Wallet 2's mnemonic; both callers hold their own copy of the same
// already-in-memory secret (threaded from main.go) and reach the same code.
type Wallet2PayConfig struct {
	USDCSigner                USDCGroupSigner
	PlatformWalletEncMnemonic string
	USDCAssetID               uint64
	RelayNetwork              string
	MaxRelayOutboundUSDMicros int64
}

// TargetQuote is defined in tool402.go (used by ProbeX402Quote) — walletpay.go
// uses it, doesn't own it.

// Wallet2PayError carries the exact HTTP status the original inline
// handler used per failure kind, so payTargetAndRespond's refactored
// wrapper can reproduce identical response codes.
type Wallet2PayError struct {
	StatusCode int
	Msg        string
}

func (e *Wallet2PayError) Error() string { return e.Msg }

// Wallet2PayResult mirrors what payTargetAndRespond needs to reconstruct
// its existing HTTP response exactly. Signed becomes true the instant
// SignUSDCPaymentGroup succeeds — the exact moment the public handler sets
// X-Inbound-Settled today — and stays true regardless of what happens next
// (a target-request failure still counts as "money already committed",
// matching the existing billing philosophy documented in x402relay.go).
type Wallet2PayResult struct {
	Signed       bool
	StatusCode   int
	ResponseBody []byte
	Settled      bool // true only when target's own response was 2xx
}

// PayTargetFromWallet2 signs and sends a real outbound x402 payment from
// Wallet 2 to target, using quote's payTo/asset/amount, then relays target's
// raw response back to the caller. This is the sole place that signs from
// Wallet 2's mnemonic — reached both by the public relay handler
// (x402relay.go's payTargetAndRespond) and, later, engine's own trusted,
// in-process, in-memory-pool-gated run-level path — never from any other,
// independently-maintained copy of this logic.
//
// method/body are the HTTP call actually made to target for the paid
// retry — empty method defaults to GET (unchanged default), body only ever
// sent when method isn't GET. Real x402 endpoints are not guaranteed to be
// GET-compatible (e.g. a POST-only resource 404s a bare GET before payment
// state is even considered), so this can't stay hardcoded to GET.
func PayTargetFromWallet2(ctx context.Context, cfg Wallet2PayConfig, target, method string, body []byte, quote TargetQuote) (Wallet2PayResult, error) {
	assetID, assetErr := strconv.ParseUint(quote.Asset, 10, 64)
	amount, amountErr := strconv.ParseUint(quote.MaxAmountRequired, 10, 64)
	if assetErr != nil || amountErr != nil {
		return Wallet2PayResult{}, &Wallet2PayError{StatusCode: http.StatusBadGateway, Msg: "target quote had an unparseable asset or amount"}
	}

	if assetID != cfg.USDCAssetID {
		log.Printf("wallet2 pay asset mismatch: quote.Asset=%q assetID=%d want=%d", quote.Asset, assetID, cfg.USDCAssetID)
		return Wallet2PayResult{}, &Wallet2PayError{StatusCode: http.StatusBadGateway, Msg: "target quoted an unexpected asset id"}
	}
	if cfg.MaxRelayOutboundUSDMicros > 0 && amount > uint64(cfg.MaxRelayOutboundUSDMicros) {
		return Wallet2PayResult{}, &Wallet2PayError{StatusCode: http.StatusBadGateway, Msg: "target quoted an amount exceeding the relay's per-call cap"}
	}

	// SignUSDCPaymentSingle, not SignUSDCPaymentGroup: this leg pays an
	// arbitrary third-party target directly over its own X-Payment header,
	// never through our own FacilitatorClient — no one is ever going to
	// cosign a fee-pooled stub on the target's end, so this must be a plain,
	// self-fee-paying, single signed transaction (confirmed live 2026-07-31:
	// a real mainnet target generically re-402'd every fee-pooled attempt).
	group, idx, err := cfg.USDCSigner.SignUSDCPaymentSingle(ctx, cfg.PlatformWalletEncMnemonic, quote.PayTo, assetID, amount)
	if err != nil {
		return Wallet2PayResult{}, &Wallet2PayError{StatusCode: http.StatusInternalServerError, Msg: "failed to sign outbound payment: " + err.Error()}
	}

	xPaymentOut, _ := json.Marshal(map[string]any{
		"x402Version": 2, "scheme": "exact", "network": cfg.RelayNetwork,
		"payload": map[string]any{"paymentGroup": group, "paymentIndex": idx},
	})
	if method == "" {
		method = http.MethodGet
	}
	var bodyReader io.Reader
	if method != http.MethodGet && len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}
	payReq, err := http.NewRequestWithContext(ctx, method, target, bodyReader)
	if err != nil {
		// Signed==true: SignUSDCPaymentGroup above already succeeded, so a
		// real signed group exists even though the paid request to target
		// was never sent -- same "money already committed" accounting as
		// the transport-failure case below, just failing before the
		// request even goes out instead of after.
		return Wallet2PayResult{Signed: true}, &Wallet2PayError{StatusCode: http.StatusBadGateway, Msg: "failed to build paid request to target: " + err.Error()}
	}
	if bodyReader != nil {
		payReq.Header.Set("Content-Type", "application/json")
	}
	payReq.Header.Set("X-Payment", string(xPaymentOut))
	payResp, err := SafeHTTPClient().Do(payReq)
	if err != nil {
		// Signed==true is deliberate here: a real signed group already
		// exists by this point, matching the pre-existing billing
		// philosophy in x402relay.go — a network failure reaching the
		// target doesn't unwind a payment that's already committed.
		return Wallet2PayResult{Signed: true}, &Wallet2PayError{StatusCode: http.StatusBadGateway, Msg: "paid request to target failed: " + err.Error()}
	}
	defer payResp.Body.Close()
	finalBody, _ := io.ReadAll(io.LimitReader(payResp.Body, 5<<20))

	if payResp.StatusCode < 200 || payResp.StatusCode >= 300 {
		// target and its response are externally controlled (the workflow's
		// configured endpoint, and whatever that endpoint returns) -- quote
		// and cap both before logging so neither can inject control
		// characters/forge log lines or blow up log volume.
		const logSnippetLimit = 512
		bodySnippet := finalBody
		if len(bodySnippet) > logSnippetLimit {
			bodySnippet = bodySnippet[:logSnippetLimit]
		}
		headerSnippet := payResp.Header.Get("Payment-Required")
		if len(headerSnippet) > logSnippetLimit {
			headerSnippet = headerSnippet[:logSnippetLimit]
		}
		log.Printf("wallet2 outbound pay to %s rejected: status=%d body=%s payment-required-header=%s", strconv.Quote(target), payResp.StatusCode, strconv.Quote(string(bodySnippet)), strconv.Quote(headerSnippet))
	}

	return Wallet2PayResult{
		Signed:       true,
		StatusCode:   payResp.StatusCode,
		ResponseBody: finalBody,
		Settled:      payResp.StatusCode >= 200 && payResp.StatusCode < 300,
	}, nil
}
