package nodes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
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
	// ContentType is what the outbound body is encoded as. Empty means the
	// historical default, application/json. A multipart body must carry the
	// exact content type it was generated with, boundary included, or the
	// target cannot parse a single field of it.
	ContentType string
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
	// OutboundTxID is the target's OWN settlement transaction id for this
	// specific payment (Wallet 2 -> target), best-effort extracted from its
	// Payment-Response/X-Payment-Response header when the target returns
	// one -- confirmed live 2026-08-01: canix402-api.compx.io returns
	// {"transaction":"..."} base64-encoded under this exact header. Empty
	// when the target doesn't return one (not every target does), which is
	// not itself an error -- Settled/StatusCode already say whether the
	// payment worked.
	OutboundTxID string
}

// outboundTxIDFromHeader best-effort extracts a settlement transaction id
// from a target's own Payment-Response header -- tolerant of both a raw
// JSON value and the base64-encoded form (matching Payment-Required's own
// encoding), and of the handful of field names real targets use for this.
func outboundTxIDFromHeader(h http.Header) string {
	raw := h.Get("Payment-Response")
	if raw == "" {
		raw = h.Get("X-Payment-Response")
	}
	if raw == "" {
		return ""
	}
	body := []byte(raw)
	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil {
		body = decoded
	}
	var parsed map[string]any
	if json.Unmarshal(body, &parsed) != nil {
		return ""
	}
	for _, key := range []string{"transaction", "txId", "txid", "tx"} {
		if v, ok := parsed[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
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

	// The target's OWN quote decides the signing scheme, not a blanket
	// assumption: a target that declares accepts[0].extra.feePayer is
	// asking for a fee-pooled 2-txn group cosigned by that exact shared
	// facilitator address (confirmed live against two independent real
	// mainnet targets -- arbsignal-production.up.railway.app and Prism --
	// both naming the identical shared GoPlausible feePayer our own relay
	// already uses: ZMFK2OI7ZBD2U27ISERZC4S6LKM6WMFJPZQ4MYNJDZ2VNBNMBA67RA22AA.
	// GoPlausible is a neutral facilitator every side already trusts, not
	// something specific to us, so nothing about a third party needing to
	// "cosign" a stub it never agreed to ever applies -- the facilitator
	// does that, exactly as for our own inbound settlement). A target with
	// no declared feePayer wants a plain, self-fee-paying single
	// transaction instead; the official @x402/avm client dispatches on
	// exactly this signal, per the Algorand Foundation's own reference
	// implementation. An earlier version of this function always used
	// SignUSDCPaymentSingle unconditionally, which is why every fee-
	// pooling-expecting target kept re-402'ing a payment that had, in fact,
	// already left the wallet.
	var group []string
	var idx int
	var err error
	if quote.FeePayer != "" {
		group, idx, err = cfg.USDCSigner.SignUSDCPaymentGroup(ctx, cfg.PlatformWalletEncMnemonic, quote.PayTo, assetID, amount, quote.FeePayer)
	} else {
		group, idx, err = cfg.USDCSigner.SignUSDCPaymentSingle(ctx, cfg.PlatformWalletEncMnemonic, quote.PayTo, assetID, amount)
	}
	if err != nil {
		return Wallet2PayResult{}, &Wallet2PayError{StatusCode: http.StatusInternalServerError, Msg: "failed to sign outbound payment: " + err.Error()}
	}

	paymentHeaderName, paymentHeaderValue := buildPaymentHeader(target, cfg.RelayNetwork, group, idx, quote)
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
		contentType := cfg.ContentType
		if contentType == "" {
			contentType = "application/json"
		}
		payReq.Header.Set("Content-Type", contentType)
	}
	payReq.Header.Set(paymentHeaderName, paymentHeaderValue)
	payResp, err := SafeOutboundPayHTTPClient().Do(payReq)
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
		// Never log paymentHeaderValue itself -- it carries the signed
		// transaction bytes, and those must not enter application logs even
		// though the underlying payment is headed for a public ledger:
		// anyone with log access could submit/replay it ahead of us. A
		// fingerprint (not reversible to the signed bytes) is enough to
		// correlate this rejection with what was actually sent, without
		// ever writing the signed payload itself anywhere.
		fingerprint := sha256.Sum256([]byte(paymentHeaderValue))
		log.Printf("wallet2 outbound pay %s %s rejected: status=%d body=%s payment-required-header=%s paymentIndex=%d xPaymentFingerprint=%s", method, strconv.Quote(target), payResp.StatusCode, strconv.Quote(string(bodySnippet)), strconv.Quote(headerSnippet), idx, hex.EncodeToString(fingerprint[:4]))
	}

	return Wallet2PayResult{
		Signed:       true,
		StatusCode:   payResp.StatusCode,
		ResponseBody: finalBody,
		Settled:      payResp.StatusCode >= 200 && payResp.StatusCode < 300,
		OutboundTxID: outboundTxIDFromHeader(payResp.Header),
	}, nil
}

// canix402Host is the one target confirmed live (2026-08-01, real settled
// mainnet payment, real data returned) to speak a non-standard x402
// dialect: the payment header is named Payment-Signature (not X-Payment),
// its value is base64-encoded JSON (matching Payment-Required's own
// encoding, not raw JSON), network is the short string "algorand-mainnet"
// (not the CAIP-2 id every other target and the official SDK use), and the
// wrapper must echo back both the chosen accept option (as "accepted") and
// the target's entire original challenge (as "paymentRequired") -- none of
// which is part of the x402 v2 spec itself, confirmed by cross-checking
// against Prism, arbsignal, and the official @x402/core SDK, all of which
// use the plain {x402Version,scheme,network,payload} shape over X-Payment.
// This is intentionally special-cased by hostname rather than generalized
// into buildPaymentHeader's default branch: it's one vendor's own gateway
// behavior, not a protocol convention, and baking a proprietary dialect
// into the default path would risk breaking every standards-compliant
// target instead.
const canix402Host = "canix402-api.compx.io"

// buildPaymentHeader picks the outbound payment header name/value for
// target, matching its own dialect. quote.RawAccept/RawChallenge (the
// target's own challenge, kept verbatim from the probe that fetched it)
// are required for the canix402 branch, and also used (best-effort) in the
// default branch below to echo the target's own resource/extensions back
// onto the payload -- a real v2 client is expected to copy these verbatim
// from the challenge it received, and it's what lets a spec-compliant
// target catalog OUR platform wallet's payment the same way we now catalog
// payments made to us (see x402relay.go's resourceInfo/bazaarDiscoveryExtension
// doc comments for the mechanism this mirrors).
func buildPaymentHeader(target, relayNetwork string, group []string, idx int, quote TargetQuote) (name, value string) {
	if u, err := url.Parse(target); err == nil && u.Hostname() == canix402Host && quote.RawAccept != nil && quote.RawChallenge != nil {
		wrapper := map[string]any{
			"x402Version":     2,
			"scheme":          "exact",
			"network":         "algorand-mainnet",
			"accepted":        quote.RawAccept,
			"payload":         map[string]any{"paymentGroup": group, "paymentIndex": idx},
			"paymentRequired": quote.RawChallenge,
		}
		b, _ := json.Marshal(wrapper)
		return "Payment-Signature", base64.StdEncoding.EncodeToString(b)
	}
	xPaymentFields := map[string]any{
		"x402Version": 2, "scheme": "exact", "network": relayNetwork,
		"payload": map[string]any{"paymentGroup": group, "paymentIndex": idx},
	}
	if quote.RawChallenge != nil {
		if res, ok := quote.RawChallenge["resource"]; ok {
			xPaymentFields["resource"] = res
		}
		if ext, ok := quote.RawChallenge["extensions"]; ok {
			xPaymentFields["extensions"] = ext
		}
	}
	xPaymentOut, _ := json.Marshal(xPaymentFields)
	return "X-Payment", string(xPaymentOut)
}
