package nodes_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agentmesh/backend/internal/engine/nodes"
)

// fakeWallet2Signer is a minimal USDCGroupSigner double for
// PayTargetFromWallet2 tests -- records every call it received and returns
// a canned group/error.
type fakeWallet2Signer struct {
	group []string
	idx   int
	err   error
	calls int
}

func (f *fakeWallet2Signer) SignUSDCPaymentGroup(_ context.Context, _, _ string, _, _ uint64, _ string) ([]string, int, error) {
	f.calls++
	if f.err != nil {
		return nil, 0, f.err
	}
	return f.group, f.idx, nil
}

func baseWallet2Cfg(signer nodes.USDCGroupSigner) nodes.Wallet2PayConfig {
	return nodes.Wallet2PayConfig{
		USDCSigner:                signer,
		PlatformWalletEncMnemonic: "enc-mnemonic",
		USDCAssetID:               10458941,
		RelayFeePayer:             "FEEPAYERADDR",
		RelayNetwork:              "algorand:testnet",
	}
}

func TestPayTargetFromWallet2RejectsUnexpectedAsset(t *testing.T) {
	signer := &fakeWallet2Signer{group: []string{"g0", "g1"}, idx: 0}
	cfg := baseWallet2Cfg(signer)

	quote := nodes.TargetQuote{PayTo: "TARGETADDR", Asset: "99999999", MaxAmountRequired: "50000"}
	result, err := nodes.PayTargetFromWallet2(context.Background(), cfg, "http://unused.example", quote)

	if err == nil {
		t.Fatal("want error for mismatched asset id")
	}
	var payErr *nodes.Wallet2PayError
	if !errors.As(err, &payErr) {
		t.Fatalf("want *nodes.Wallet2PayError, got %T", err)
	}
	if payErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", payErr.StatusCode)
	}
	if result.Signed {
		t.Fatal("want Signed=false when asset id is rejected before signing")
	}
	if signer.calls != 0 {
		t.Fatalf("want signer never called, got %d calls", signer.calls)
	}
}

func TestPayTargetFromWallet2RejectsOverCap(t *testing.T) {
	signer := &fakeWallet2Signer{group: []string{"g0", "g1"}, idx: 0}
	cfg := baseWallet2Cfg(signer)
	cfg.MaxRelayOutboundUSDMicros = 10000

	quote := nodes.TargetQuote{PayTo: "TARGETADDR", Asset: "10458941", MaxAmountRequired: "50000"}
	result, err := nodes.PayTargetFromWallet2(context.Background(), cfg, "http://unused.example", quote)

	if err == nil {
		t.Fatal("want error for amount exceeding cap")
	}
	var payErr *nodes.Wallet2PayError
	if !errors.As(err, &payErr) {
		t.Fatalf("want *nodes.Wallet2PayError, got %T", err)
	}
	if payErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", payErr.StatusCode)
	}
	if result.Signed {
		t.Fatal("want Signed=false when amount exceeds cap, checked before signing")
	}
	if signer.calls != 0 {
		t.Fatalf("want signer never called, got %d calls", signer.calls)
	}
}

func TestPayTargetFromWallet2SignFailureReturns500(t *testing.T) {
	signer := &fakeWallet2Signer{err: errors.New("invalid receiver address")}
	cfg := baseWallet2Cfg(signer)

	quote := nodes.TargetQuote{PayTo: "TARGETADDR", Asset: "10458941", MaxAmountRequired: "50000"}
	result, err := nodes.PayTargetFromWallet2(context.Background(), cfg, "http://unused.example", quote)

	if err == nil {
		t.Fatal("want error when signing fails")
	}
	var payErr *nodes.Wallet2PayError
	if !errors.As(err, &payErr) {
		t.Fatalf("want *nodes.Wallet2PayError, got %T", err)
	}
	if payErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", payErr.StatusCode)
	}
	if result.Signed {
		t.Fatal("want Signed=false when signing itself fails")
	}
}

// TestPayTargetFromWallet2TargetNetworkFailureStillSigned is a
// reproduce-then-fix regression test: a network failure reaching the target
// AFTER a real signed payment group already exists must still report
// Signed=true, matching the pre-existing billing philosophy in
// x402relay.go -- money already committed isn't unwound by a downstream
// network hiccup. (Verified by temporarily changing the target-request-error
// branch in walletpay.go to return Wallet2PayResult{Signed: false} --
// confirmed this test fails that way, then restored Signed: true and
// confirmed it passes.)
func TestPayTargetFromWallet2TargetNetworkFailureStillSigned(t *testing.T) {
	signer := &fakeWallet2Signer{group: []string{"g0", "g1"}, idx: 0}
	cfg := baseWallet2Cfg(signer)

	// A target server that immediately closes so the outbound request fails
	// at the transport level rather than getting a normal HTTP response.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	target.Close() // closed before use -- guarantees a connection-refused error

	quote := nodes.TargetQuote{PayTo: "TARGETADDR", Asset: "10458941", MaxAmountRequired: "50000"}
	result, err := nodes.PayTargetFromWallet2(context.Background(), cfg, target.URL, quote)

	if err == nil {
		t.Fatal("want error when the paid request to target fails")
	}
	var payErr *nodes.Wallet2PayError
	if !errors.As(err, &payErr) {
		t.Fatalf("want *nodes.Wallet2PayError, got %T", err)
	}
	if payErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", payErr.StatusCode)
	}
	if !result.Signed {
		t.Fatal("want Signed=true even though the target request failed -- a real signed group already exists by this point")
	}
	if signer.calls != 1 {
		t.Fatalf("want exactly one sign call, got %d", signer.calls)
	}
}

func TestPayTargetFromWallet2SuccessReturnsTargetResponse(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment") == "" {
			t.Fatal("want request to target to carry the signed X-Payment header")
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":"paid response from target"}`))
	}))
	defer target.Close()

	signer := &fakeWallet2Signer{group: []string{"g0", "g1"}, idx: 0}
	cfg := baseWallet2Cfg(signer)

	quote := nodes.TargetQuote{PayTo: "TARGETADDR", Asset: "10458941", MaxAmountRequired: "50000"}
	result, err := nodes.PayTargetFromWallet2(context.Background(), cfg, target.URL, quote)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Signed {
		t.Fatal("want Signed=true on success")
	}
	if result.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", result.StatusCode)
	}
	if !result.Settled {
		t.Fatal("want Settled=true for a 2xx target response")
	}
	if string(result.ResponseBody) != `{"data":"paid response from target"}` {
		t.Fatalf("want target's response body relayed through, got %q", result.ResponseBody)
	}
	if signer.calls != 1 {
		t.Fatalf("want exactly one sign call, got %d", signer.calls)
	}
}

func TestPayTargetFromWallet2NonSuccessStatusNotSettled(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"error":"still not enough"}`))
	}))
	defer target.Close()

	signer := &fakeWallet2Signer{group: []string{"g0", "g1"}, idx: 0}
	cfg := baseWallet2Cfg(signer)

	quote := nodes.TargetQuote{PayTo: "TARGETADDR", Asset: "10458941", MaxAmountRequired: "50000"}
	result, err := nodes.PayTargetFromWallet2(context.Background(), cfg, target.URL, quote)

	// A non-2xx from the target is not itself a Wallet2PayError -- the paid
	// HTTP request to target succeeded at the transport level; it's the
	// target's own status code that says the payment wasn't accepted. The
	// caller (payTargetAndRespond) still relays this status/body through.
	if err != nil {
		t.Fatalf("want no error for a non-2xx target response (transport succeeded), got %v", err)
	}
	if !result.Signed {
		t.Fatal("want Signed=true -- a real signed group exists regardless of target's response status")
	}
	if result.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("want target's real status code (402) surfaced, got %d", result.StatusCode)
	}
	if result.Settled {
		t.Fatal("want Settled=false for a non-2xx target response")
	}
}
