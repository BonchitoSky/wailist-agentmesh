package nodes

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// TestBuildPaymentHeaderUsesCanix402DialectForItsHost reproduces the real
// fix confirmed live 2026-08-01: canix402-api.compx.io rejected the
// standard X-Payment header/shape every other target accepts, but accepted
// a Payment-Signature header carrying base64 JSON with "accepted" and
// "paymentRequired" echoed back verbatim. Confirms buildPaymentHeader
// switches to that exact shape only for that host.
func TestBuildPaymentHeaderUsesCanix402DialectForItsHost(t *testing.T) {
	accept := map[string]any{"payTo": "TARGETADDR", "asset": "31566704", "amount": "10000", "scheme": "exact"}
	challenge := map[string]any{"accepts": []any{accept}, "resource": map[string]any{"url": "https://canix402-api.compx.io/opportunities"}}
	quote := TargetQuote{PayTo: "TARGETADDR", Asset: "31566704", MaxAmountRequired: "10000", RawAccept: accept, RawChallenge: challenge}

	name, value := buildPaymentHeader("https://canix402-api.compx.io/opportunities", "algorand:testnet", []string{"g0", "g1"}, 1, quote)

	if name != "Payment-Signature" {
		t.Fatalf("want header name Payment-Signature, got %q", name)
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("want header value to be valid base64, got error: %v", err)
	}
	var wrapper map[string]any
	if err := json.Unmarshal(decoded, &wrapper); err != nil {
		t.Fatalf("want decoded header value to be valid JSON, got error: %v", err)
	}
	if wrapper["network"] != "algorand-mainnet" {
		t.Fatalf("want network=\"algorand-mainnet\" (canix402's own short form, not CAIP-2), got %v", wrapper["network"])
	}
	if wrapper["accepted"] == nil {
		t.Fatal("want the chosen accept option echoed back under \"accepted\"")
	}
	if wrapper["paymentRequired"] == nil {
		t.Fatal("want the target's full original challenge echoed back under \"paymentRequired\"")
	}
	payload, ok := wrapper["payload"].(map[string]any)
	if !ok {
		t.Fatalf("want a payload object, got %T", wrapper["payload"])
	}
	if payload["paymentIndex"] != float64(1) {
		t.Fatalf("want paymentIndex=1 passed through, got %v", payload["paymentIndex"])
	}
}

// TestBuildPaymentHeaderUsesStandardDialectForOtherHosts confirms every
// other target keeps getting the plain, minimal X-Payment shape unchanged
// -- the canix402 special case must not leak into the default path.
func TestBuildPaymentHeaderUsesStandardDialectForOtherHosts(t *testing.T) {
	quote := TargetQuote{PayTo: "TARGETADDR", Asset: "31566704", MaxAmountRequired: "1000"}

	name, value := buildPaymentHeader("https://arbsignal-production.up.railway.app/arb/pair", "algorand:mainnet", []string{"g0", "g1"}, 1, quote)

	if name != "X-Payment" {
		t.Fatalf("want header name X-Payment for a non-canix402 target, got %q", name)
	}
	var wrapper map[string]any
	if err := json.Unmarshal([]byte(value), &wrapper); err != nil {
		t.Fatalf("want raw (non-base64) JSON for the standard dialect, got error: %v", err)
	}
	if wrapper["network"] != "algorand:mainnet" {
		t.Fatalf("want the network passed straight through unmodified, got %v", wrapper["network"])
	}
	if _, has := wrapper["accepted"]; has {
		t.Fatal("want no \"accepted\" field in the standard dialect")
	}
	if _, has := wrapper["paymentRequired"]; has {
		t.Fatal("want no \"paymentRequired\" field in the standard dialect")
	}
}

// TestBuildPaymentHeaderFallsBackToStandardDialectWithoutRawChallenge
// confirms the canix402 branch never fires without the raw challenge data
// it needs -- e.g. a caller that built TargetQuote by hand (not via a real
// probe) rather than risk sending an incomplete/malformed canix402 payload.
func TestBuildPaymentHeaderFallsBackToStandardDialectWithoutRawChallenge(t *testing.T) {
	quote := TargetQuote{PayTo: "TARGETADDR", Asset: "31566704", MaxAmountRequired: "10000"}

	name, _ := buildPaymentHeader("https://canix402-api.compx.io/opportunities", "algorand:mainnet", []string{"g0", "g1"}, 1, quote)

	if name != "X-Payment" {
		t.Fatalf("want fallback to X-Payment when RawAccept/RawChallenge are missing, got %q", name)
	}
}
