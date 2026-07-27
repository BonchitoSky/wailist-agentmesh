package nodes_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/agentmesh/backend/internal/engine"
	"github.com/agentmesh/backend/internal/engine/nodes"
	"github.com/agentmesh/backend/internal/models"
)

// TestMain sets the permissive URL validator once for the whole nodes_test
// binary, so every test that dials an httptest.NewServer target works
// regardless of file/test execution order. No test in this package exercises
// the real SSRF-blocking validator, so there's nothing to preserve by
// toggling it per-test.
func TestMain(m *testing.M) {
	nodes.SetURLValidatorForTest(func(string) error { return nil })
	os.Exit(m.Run())
}

type mockSigner struct {
	txID string
	err  error
}

func (m *mockSigner) SignAndSendPayment(_ context.Context, _, _ string, _ uint64) (string, error) {
	return m.txID, m.err
}

func TestX402FreeEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":"free response"}`))
	}))
	defer srv.Close()
	node := models.WorkflowNode{ID: "x1", Type: models.NodeTypeTool402, Endpoint: srv.URL}
	rc := engine.NewRunContext("r1", nil)
	result, err := nodes.ExecuteTool402(context.Background(), node, rc, models.AgentWallet{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := result.(map[string]any)
	if !ok || m["data"] != "free response" {
		t.Fatalf("unexpected result: %v", result)
	}
}

func TestX402ParseQuote(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Payment-Required", `{"price":"0.001","unit":"call","network":"algorand-testnet","recipient":"ALGO123"}`)
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()
	price, err := nodes.QuoteX402(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if price["price"] != "0.001" {
		t.Fatalf("want price 0.001 got %v", price["price"])
	}
}

// TestX402ProbeQuoteValid verifies that ProbeX402Quote (and by extension
// ProbeX402Price) correctly parses a well-formed real v2 challenge's
// payTo/asset/maxAmountRequired, exercising probeTool402Endpoint directly
// rather than only through ExecuteTool402V2 (which never surfaces the parsed
// quote to its caller).
func TestX402ProbeQuoteValid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"TARGETADDR","asset":"10458941","maxAmountRequired":"250000"}]}`))
	}))
	defer srv.Close()

	isV2, quote, err := nodes.ProbeX402Quote(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isV2 {
		t.Fatal("want isV2=true for a real accepts[] challenge")
	}
	if quote.PayTo != "TARGETADDR" {
		t.Fatalf("want payTo TARGETADDR, got %q", quote.PayTo)
	}
	if quote.Asset != "10458941" {
		t.Fatalf("want asset 10458941, got %q", quote.Asset)
	}
	if quote.MaxAmountRequired != "250000" {
		t.Fatalf("want maxAmountRequired 250000, got %q", quote.MaxAmountRequired)
	}

	priceIsV2, amount, err := nodes.ProbeX402Price(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error from ProbeX402Price: %v", err)
	}
	if !priceIsV2 {
		t.Fatal("want isV2=true from ProbeX402Price")
	}
	if amount != 250000 {
		t.Fatalf("want amount 250000, got %d", amount)
	}
}

// TestX402ProbeQuoteMalformedAmount verifies that a real v2 challenge
// (accepts[] present) whose maxAmountRequired is non-numeric produces an
// error rather than silently parsing to amount=0 — a silent 0 would be
// indistinguishable from a genuinely free tool, and Task 5 sizes a real
// credit reservation and on-chain payment off this value.
func TestX402ProbeQuoteMalformedAmount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"TARGETADDR","asset":"10458941","maxAmountRequired":"not-a-number"}]}`))
	}))
	defer srv.Close()

	_, _, err := nodes.ProbeX402Quote(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("want an error for a malformed (non-numeric) maxAmountRequired, got nil")
	}

	_, amount, err := nodes.ProbeX402Price(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("want an error from ProbeX402Price for a malformed maxAmountRequired, got nil")
	}
	if amount != 0 {
		t.Fatalf("want zero-valued amount alongside the error, got %d", amount)
	}
}

// TestX402ProbeQuoteMissingAmount verifies that a real v2 challenge with
// maxAmountRequired missing entirely (not merely malformed) is treated the
// same as a malformed one: an error, not a silent zero. There's no reason a
// missing field should be trusted more than a garbled one — both are
// evidence of a broken/non-compliant challenge, and either way Task 5 must
// not size a reservation or payment off an unverified zero.
func TestX402ProbeQuoteMissingAmount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"TARGETADDR","asset":"10458941"}]}`))
	}))
	defer srv.Close()

	isV2, _, err := nodes.ProbeX402Quote(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("want an error for a missing maxAmountRequired, got nil")
	}
	if !isV2 {
		t.Fatal("want isV2=true still reported — it IS a real v2 challenge, just malformed")
	}
}

// TestX402PaymentSigned verifies the full sign-and-retry flow: the runner
// receives a 402, calls the signer, and retries with X-Payment-Txid.
func TestX402PaymentSigned(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("X-Payment-Txid"); h != "" {
			gotHeader = h
			w.Write([]byte(`{"ok":true}`))
			return
		}
		w.Header().Set("X-Payment-Required", `{"price":"0.001","unit":"call","network":"algorand-testnet","recipient":"ALGO123"}`)
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()

	node := models.WorkflowNode{ID: "x1", Type: models.NodeTypeTool402, Endpoint: srv.URL}
	rc := engine.NewRunContext("r1", nil)
	signer := &mockSigner{txID: "TX-SIGNED-123"}
	aw := models.AgentWallet{AgentNodeID: "a1", EncryptedMnemonic: "enc-mnemonic"}

	result, err := nodes.ExecuteTool402(context.Background(), node, rc, aw, signer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type %T: %v", result, result)
	}
	if m["txId"] != "TX-SIGNED-123" {
		t.Fatalf("want txId TX-SIGNED-123, got %v", m["txId"])
	}
	if gotHeader != "TX-SIGNED-123" {
		t.Fatalf("retry request missing X-Payment-Txid header, got %q", gotHeader)
	}
	resp, _ := m["response"].(map[string]any)
	if resp == nil || resp["ok"] != true {
		t.Fatalf("want response.ok=true, got %v", m["response"])
	}
}

// TestX402NoWallet verifies that a 402 response with no wallet configured
// returns a graceful error map (not a Go error that would fail the run).
func TestX402NoWallet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Payment-Required", `{"price":"0.001","unit":"call","network":"algorand-testnet","recipient":"ALGO123"}`)
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()

	node := models.WorkflowNode{ID: "x1", Type: models.NodeTypeTool402, Endpoint: srv.URL}
	rc := engine.NewRunContext("r1", nil)

	result, err := nodes.ExecuteTool402(context.Background(), node, rc, models.AgentWallet{}, nil)
	if err != nil {
		t.Fatalf("want nil Go error (graceful degradation), got %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok || m["error"] == nil {
		t.Fatalf("want error key in result map, got %v", result)
	}
	if !strings.Contains(m["error"].(string), "no agent wallet") {
		t.Fatalf("want 'no agent wallet' in error message, got %v", m["error"])
	}
}

// TestX402SignerError verifies that a signer failure (e.g. insufficient funds)
// propagates as a Go error so the run log marks the node as failed.
func TestX402SignerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Payment-Required", `{"price":"0.001","unit":"call","network":"algorand-testnet","recipient":"ALGO123"}`)
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()

	node := models.WorkflowNode{ID: "x1", Type: models.NodeTypeTool402, Endpoint: srv.URL}
	rc := engine.NewRunContext("r1", nil)
	signer := &mockSigner{err: errors.New("insufficient balance")}
	aw := models.AgentWallet{AgentNodeID: "a1", EncryptedMnemonic: "enc-mnemonic"}

	_, err := nodes.ExecuteTool402(context.Background(), node, rc, aw, signer)
	if err == nil {
		t.Fatal("want error from signer failure, got nil")
	}
	if !strings.Contains(err.Error(), "insufficient balance") {
		t.Fatalf("want 'insufficient balance' in error, got %v", err)
	}
}

type mockUSDCGroupSigner struct {
	group []string
	idx   int
}

func (m *mockUSDCGroupSigner) SignUSDCPaymentGroup(_ context.Context, _, _ string, _, _ uint64, _ string) ([]string, int, error) {
	return m.group, m.idx, nil
}

// noopLedger is a permissive PaymentLedger for tests that need a real
// payment to go through without asserting anything about reserve/commit/
// release themselves.
func noopLedger() nodes.PaymentLedger {
	return nodes.PaymentLedger{
		Reserve: func(context.Context, int64) error { return nil },
		Commit:  func(context.Context, string, int64, string) {},
		Release: func(context.Context, int64) {},
	}
}

// TestX402V2TargetRoutesThroughRelay verifies that a target advertising the
// real x402 v2 shape (accepts[]) is never paid directly — the agent pays the
// relay instead, which is what earns orchestrator-entry attribution.
func TestX402V2TargetRoutesThroughRelay(t *testing.T) {
	var targetHit, relayHit bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit = true
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"TARGETADDR","asset":"10458941","maxAmountRequired":"100000"}]}`))
	}))
	defer target.Close()

	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relayHit = true
		if r.Header.Get("X-Payment") != "" {
			w.Header().Set("X-Inbound-Settled", "true")
			w.Write([]byte(`{"data":"relayed paid response"}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"PLATFORMADDR","asset":"10458941","maxAmountRequired":"100000"}]}`))
	}))
	defer relay.Close()

	node := models.WorkflowNode{ID: "x1", Type: models.NodeTypeTool402, Endpoint: target.URL}
	rc := engine.NewRunContext("r1", nil)
	aw := models.AgentWallet{AgentNodeID: "a1", EncryptedMnemonic: "enc-mnemonic"}
	signer := &mockSigner{txID: "unused-legacy-path"}
	usdcSigner := &mockUSDCGroupSigner{group: []string{"g0", "g1"}, idx: 0}

	relayCfg := nodes.X402RelayConfig{USDCSigner: usdcSigner, PlatformSpendEncMnemonic: "platform-enc-mnemonic", ExpectedAssetID: uint64(10458941), RelayBaseURL: relay.URL, Ledger: noopLedger()}
	paymentResult, err := nodes.ExecuteTool402V2(context.Background(), node, rc, aw, signer, relayCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !targetHit {
		t.Fatal("want relay to have queried target's real price first")
	}
	if !relayHit {
		t.Fatal("want relay to have been called")
	}
	if paymentResult.SettledUSDMicros != 100000 {
		t.Fatalf("want settled amount 100000 (matches maxAmountRequired), got %d", paymentResult.SettledUSDMicros)
	}
	if paymentResult.DebitKind != models.DebitKindX402RelayCost {
		t.Fatalf("want debit kind %q, got %q", models.DebitKindX402RelayCost, paymentResult.DebitKind)
	}
	m, ok := paymentResult.Response.(map[string]any)
	if !ok || m["data"] != "relayed paid response" {
		t.Fatalf("want relayed response, got %v", paymentResult.Response)
	}
}

// TestX402LegacyTargetBypassesRelay verifies the existing flat-quote dialect
// (no accepts[]) still pays the target directly — unchanged behavior.
func TestX402LegacyTargetBypassesRelay(t *testing.T) {
	var relayHit bool
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relayHit = true
	}))
	defer relay.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("X-Payment-Txid"); h != "" {
			w.Write([]byte(`{"ok":true}`))
			return
		}
		w.Header().Set("X-Payment-Required", `{"price":"0.001","unit":"call","network":"algorand-testnet","recipient":"ALGO123"}`)
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()

	node := models.WorkflowNode{ID: "x1", Type: models.NodeTypeTool402, Endpoint: srv.URL}
	rc := engine.NewRunContext("r1", nil)
	aw := models.AgentWallet{AgentNodeID: "a1", EncryptedMnemonic: "enc-mnemonic"}
	signer := &mockSigner{txID: "TX-SIGNED-123"}
	usdcSigner := &mockUSDCGroupSigner{}

	relayCfg := nodes.X402RelayConfig{USDCSigner: usdcSigner, PlatformSpendEncMnemonic: "platform-enc-mnemonic", ExpectedAssetID: uint64(10458941), RelayBaseURL: relay.URL, Ledger: noopLedger()}
	paymentResult, err := nodes.ExecuteTool402V2(context.Background(), node, rc, aw, signer, relayCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if relayHit {
		t.Fatal("legacy target must bypass the relay entirely")
	}
	if paymentResult.SettledUSDMicros != models.X402PlatformFeeUSDMicros {
		t.Fatalf("want flat fee %d, got %d", models.X402PlatformFeeUSDMicros, paymentResult.SettledUSDMicros)
	}
	if paymentResult.DebitKind != models.DebitKindX402PlatformFee {
		t.Fatalf("want debit kind %q, got %q", models.DebitKindX402PlatformFee, paymentResult.DebitKind)
	}
	m := paymentResult.Response.(map[string]any)
	if m["txId"] != "TX-SIGNED-123" {
		t.Fatalf("want legacy direct-pay path unchanged, got %v", m)
	}
}

// TestX402V2TargetWithAmpersandInQueryString verifies that endpoint URLs
// containing & (e.g. model=gpt4&format=json) are properly URL-encoded when
// passed to the relay, so the relay's parsing of the target parameter receives
// the full original URL, not a truncated prefix at the first &.
func TestX402V2TargetWithAmpersandInQueryString(t *testing.T) {
	var capturedTargetParam string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"TARGETADDR","asset":"10458941","maxAmountRequired":"100000"}]}`))
	}))
	defer target.Close()

	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedTargetParam = r.URL.Query().Get("target")
		if r.Header.Get("X-Payment") != "" {
			w.Header().Set("X-Inbound-Settled", "true")
			w.Write([]byte(`{"data":"relayed paid response"}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"PLATFORMADDR","asset":"10458941","maxAmountRequired":"100000"}]}`))
	}))
	defer relay.Close()

	// Create an endpoint URL with & in the query string
	endpointWithQuery := target.URL + "?model=gpt4&format=json"
	node := models.WorkflowNode{ID: "x1", Type: models.NodeTypeTool402, Endpoint: endpointWithQuery}
	rc := engine.NewRunContext("r1", nil)
	aw := models.AgentWallet{AgentNodeID: "a1", EncryptedMnemonic: "enc-mnemonic"}
	signer := &mockSigner{txID: "unused-legacy-path"}
	usdcSigner := &mockUSDCGroupSigner{group: []string{"g0", "g1"}, idx: 0}

	relayCfg := nodes.X402RelayConfig{USDCSigner: usdcSigner, PlatformSpendEncMnemonic: "platform-enc-mnemonic", ExpectedAssetID: uint64(10458941), RelayBaseURL: relay.URL, Ledger: noopLedger()}
	paymentResult, err := nodes.ExecuteTool402V2(context.Background(), node, rc, aw, signer, relayCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the relay received the full endpoint URL, not truncated at &
	if capturedTargetParam != endpointWithQuery {
		t.Fatalf("want target param %q, got %q (was truncated at &)", endpointWithQuery, capturedTargetParam)
	}

	m, ok := paymentResult.Response.(map[string]any)
	if !ok || m["data"] != "relayed paid response" {
		t.Fatalf("want relayed response, got %v", paymentResult.Response)
	}
}

// TestX402V2RelayPreflightUsesRealAmount verifies the balance check gates on
// the relay's real maxAmountRequired, not the flat platform fee — a
// checkBalance that only tolerates the flat fee must reject a costlier call.
func TestX402V2RelayPreflightUsesRealAmount(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"TARGETADDR","asset":"10458941","maxAmountRequired":"100000"}]}`))
	}))
	defer target.Close()

	var paid bool
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment") != "" {
			paid = true
			w.Header().Set("X-Inbound-Settled", "true")
			w.Write([]byte(`{"data":"relayed paid response"}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"PLATFORMADDR","asset":"10458941","maxAmountRequired":"100000"}]}`))
	}))
	defer relay.Close()

	node := models.WorkflowNode{ID: "x1", Type: models.NodeTypeTool402, Endpoint: target.URL}
	rc := engine.NewRunContext("r1", nil)
	aw := models.AgentWallet{}
	usdcSigner := &mockUSDCGroupSigner{group: []string{"g0", "g1"}, idx: 0}

	var checkedAmount int64
	ledger := nodes.PaymentLedger{
		Reserve: func(_ context.Context, amount int64) error {
			checkedAmount = amount
			if amount > 500_000 {
				return fmt.Errorf("insufficient credits")
			}
			return nil
		},
		Commit:  func(context.Context, string, int64, string) {},
		Release: func(context.Context, int64) {},
	}

	// maxAmountRequired (100000) is under the flat-fee-sized ceiling this
	// ledger.Reserve allows, so this call should succeed and pay.
	relayCfg := nodes.X402RelayConfig{USDCSigner: usdcSigner, PlatformSpendEncMnemonic: "platform-enc-mnemonic", ExpectedAssetID: uint64(10458941), RelayBaseURL: relay.URL, Ledger: ledger}
	_, err := nodes.ExecuteTool402V2(context.Background(), node, rc, aw, nil, relayCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if checkedAmount != 100000 {
		t.Fatalf("want ledger.Reserve called with real amount 100000, got %d", checkedAmount)
	}
	if !paid {
		t.Fatal("want relay to have been paid")
	}

	// Now make Reserve reject anything over 50000 — below the real 100000
	// cost — and verify the payment never happens.
	paid = false
	strictLedger := nodes.PaymentLedger{
		Reserve: func(_ context.Context, amount int64) error {
			if amount > 50_000 {
				return fmt.Errorf("insufficient credits")
			}
			return nil
		},
		Commit:  func(context.Context, string, int64, string) {},
		Release: func(context.Context, int64) {},
	}
	strictRelayCfg := nodes.X402RelayConfig{USDCSigner: usdcSigner, PlatformSpendEncMnemonic: "platform-enc-mnemonic", ExpectedAssetID: uint64(10458941), RelayBaseURL: relay.URL, Ledger: strictLedger}
	_, err = nodes.ExecuteTool402V2(context.Background(), node, rc, aw, nil, strictRelayCfg)
	if err == nil {
		t.Fatal("want insufficient-credits error when real amount exceeds balance")
	}
	if paid {
		t.Fatal("want no payment sent when preflight rejects the real amount")
	}
}

// TestX402V2RelayRejectsPayment verifies that when the relay rejects a payment
// (returns non-2xx status despite X-Payment being present, e.g. expired/invalid
// payment or verification failure), the result is returned to the caller but
// nothing is billed — SettledUSDMicros remains 0 and DebitKind remains empty.
func TestX402V2RelayRejectsPayment(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"TARGETADDR","asset":"10458941","maxAmountRequired":"100000"}]}`))
	}))
	defer target.Close()

	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment") != "" {
			// Relay rejects the payment (expired/invalid/verification failed)
			w.WriteHeader(http.StatusPaymentRequired)
			w.Write([]byte(`{"error":"payment verification failed"}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"PLATFORMADDR","asset":"10458941","maxAmountRequired":"100000"}]}`))
	}))
	defer relay.Close()

	node := models.WorkflowNode{ID: "x1", Type: models.NodeTypeTool402, Endpoint: target.URL}
	rc := engine.NewRunContext("r1", nil)
	aw := models.AgentWallet{AgentNodeID: "a1", EncryptedMnemonic: "enc-mnemonic"}
	signer := &mockSigner{txID: "unused"}
	usdcSigner := &mockUSDCGroupSigner{group: []string{"g0", "g1"}, idx: 0}

	var reserved, released bool
	var committed bool
	ledger := nodes.PaymentLedger{
		Reserve: func(context.Context, int64) error { reserved = true; return nil },
		Commit:  func(context.Context, string, int64, string) { committed = true },
		Release: func(context.Context, int64) { released = true },
	}
	relayCfg := nodes.X402RelayConfig{USDCSigner: usdcSigner, PlatformSpendEncMnemonic: "platform-enc-mnemonic", ExpectedAssetID: uint64(10458941), RelayBaseURL: relay.URL, Ledger: ledger}
	paymentResult, err := nodes.ExecuteTool402V2(context.Background(), node, rc, aw, signer, relayCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The response should be returned to the caller (the error message)
	m, ok := paymentResult.Response.(map[string]any)
	if !ok {
		t.Fatalf("want response to be parsed JSON, got %T: %v", paymentResult.Response, paymentResult.Response)
	}
	if m["error"] != "payment verification failed" {
		t.Fatalf("want error message in response, got %v", m)
	}

	// But nothing should be billed — the payment was rejected, so nothing
	// actually settled on-chain.
	if paymentResult.SettledUSDMicros != 0 {
		t.Fatalf("want SettledUSDMicros=0 (payment rejected), got %d", paymentResult.SettledUSDMicros)
	}
	if paymentResult.DebitKind != "" {
		t.Fatalf("want DebitKind empty (payment rejected), got %q", paymentResult.DebitKind)
	}

	// The reservation must have been taken (before signing) and then
	// released (no X-Inbound-Settled on the relay's response), never
	// committed to a permanent charge.
	if !reserved {
		t.Fatal("want the balance to have been reserved before signing")
	}
	if !released {
		t.Fatal("want the reservation to have been released since nothing settled")
	}
	if committed {
		t.Fatal("want no commit — the payment was rejected, nothing settled")
	}
}

type panickingUSDCGroupSigner struct{}

func (p *panickingUSDCGroupSigner) SignUSDCPaymentGroup(_ context.Context, _, _ string, _, _ uint64, _ string) ([]string, int, error) {
	panic("simulated panic mid-payment")
}

// TestX402V2RelayReleasesReservationOnPanic is a regression test: a
// reservation taken by Reserve is a real balance decrement with no durable
// record of its own (no debit_ledger row exists until Commit runs). If a
// panic unwinds through executeTool402V2Relay after Reserve succeeds but
// before Commit or Release runs -- here simulated via a signer whose
// SignUSDCPaymentGroup panics instead of erroring -- the reservation must
// still be released via a deferred cleanup, or the user's credits are
// permanently and silently stranded with nothing to reconcile against.
func TestX402V2RelayReleasesReservationOnPanic(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"TARGETADDR","asset":"10458941","maxAmountRequired":"100000"}]}`))
	}))
	defer target.Close()

	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"PLATFORMADDR","asset":"10458941","maxAmountRequired":"100000"}]}`))
	}))
	defer relay.Close()

	node := models.WorkflowNode{ID: "x1", Type: models.NodeTypeTool402, Endpoint: target.URL}
	rc := engine.NewRunContext("r1", nil)
	aw := models.AgentWallet{}
	usdcSigner := &panickingUSDCGroupSigner{}

	var reserved, released, committed bool
	ledger := nodes.PaymentLedger{
		Reserve: func(context.Context, int64) error { reserved = true; return nil },
		Commit:  func(context.Context, string, int64, string) { committed = true },
		Release: func(context.Context, int64) { released = true },
	}

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("want the panic to propagate out of ExecuteTool402V2, got none")
			}
		}()
		relayCfg := nodes.X402RelayConfig{USDCSigner: usdcSigner, PlatformSpendEncMnemonic: "platform-enc-mnemonic", ExpectedAssetID: uint64(10458941), RelayBaseURL: relay.URL, Ledger: ledger}
		nodes.ExecuteTool402V2(context.Background(), node, rc, aw, nil, relayCfg)
		t.Fatal("unreachable: ExecuteTool402V2 should have panicked")
	}()

	if !reserved {
		t.Fatal("want the balance to have been reserved before the panic")
	}
	if !released {
		t.Fatal("want the reservation released via deferred cleanup despite the panic — otherwise the balance is permanently stranded")
	}
	if committed {
		t.Fatal("want no commit — the payment never completed")
	}
}

// TestX402RunLevelCommitsNotReleasesOnTargetNetworkFailure is a regression
// test for the run-level path (RunFundingID != "", dispatched by
// ExecuteTool402V2 into executeTool402RunLevel): PayTargetFromWallet2's
// Signed field becomes true the instant a real payment group is signed and
// submitted from Wallet 2 -- real money has already moved -- and stays true
// even when the subsequent HTTP call to the target fails at the network
// level (see Wallet2PayResult's doc comment in walletpay.go). Before this
// fix, executeTool402RunLevel released the ledger reservation whenever
// payErr != nil, regardless of Signed, which would understate real spend:
// a later call in the same agent turn could Reserve phantom headroom this
// pool doesn't actually have. This test hijacks and closes the connection
// on the paid request specifically (the quote probe beforehand must still
// succeed normally) to simulate a genuine transport failure after signing.
func TestX402RunLevelCommitsNotReleasesOnTargetNetworkFailure(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment") != "" {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("test server does not support hijacking")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatal(err)
			}
			conn.Close()
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"TARGETADDR","asset":"10458941","maxAmountRequired":"100000"}]}`))
	}))
	defer target.Close()

	node := models.WorkflowNode{ID: "x1", Type: models.NodeTypeTool402, Endpoint: target.URL}
	rc := engine.NewRunContext("r1", nil)
	aw := models.AgentWallet{}
	usdcSigner := &mockUSDCGroupSigner{group: []string{"g0", "g1"}, idx: 0}

	var reservedAmount int64
	var released, committed bool
	var committedAmount int64
	ledger := nodes.PaymentLedger{
		Reserve: func(_ context.Context, amount int64) error { reservedAmount = amount; return nil },
		Commit:  func(_ context.Context, _ string, amount int64, _ string) { committed = true; committedAmount = amount },
		Release: func(context.Context, int64) { released = true },
	}

	var recordSettlementCalled bool
	relayCfg := nodes.X402RelayConfig{
		RunFundingID: "test-run-funding-1",
		Ledger:       ledger,
		Wallet2: nodes.Wallet2PayConfig{
			USDCSigner:                usdcSigner,
			PlatformWalletEncMnemonic: "platform-wallet-enc-mnemonic",
			USDCAssetID:               uint64(10458941),
			RelayFeePayer:             "FEEPAYERADDR",
			RelayNetwork:              "algorand:testnet",
		},
		RecordSettlement: func(_ context.Context, _ string, _ int64, _ bool) error {
			recordSettlementCalled = true
			return nil
		},
	}

	_, err := nodes.ExecuteTool402V2(context.Background(), node, rc, aw, nil, relayCfg)
	if err == nil {
		t.Fatal("want an error surfaced for the network failure reaching the target")
	}
	if reservedAmount != 100000 {
		t.Fatalf("want 100000 reserved, got %d", reservedAmount)
	}
	if !recordSettlementCalled {
		t.Fatal("want RecordSettlement to still be called even though the target request failed at the network level")
	}
	if released {
		t.Fatal("want the reservation NEVER released -- money already left Wallet 2 once the payment group was signed")
	}
	if !committed || committedAmount != 100000 {
		t.Fatalf("want the reservation committed for 100000 (money already left Wallet 2), got committed=%v amount=%d", committed, committedAmount)
	}
}

// TestX402RunLevelCommitsNotReleasesOnRecordSettlementFailure is the second
// half of the same regression: when the outbound payment succeeds (the
// target responds 2xx) but the audit-write call (RecordSettlement, e.g.
// RecordRunFundedSettlement/RecordOutboundSettlement in production) fails
// for an unrelated reason (DB blip), the ledger reservation must still be
// Committed, never Released -- this is a bookkeeping failure, not a payment
// failure, matching the identical distinction reserveAndFundRun already
// makes when RecordRunFunding fails after a successful FundRunReserve.
func TestX402RunLevelCommitsNotReleasesOnRecordSettlementFailure(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment") != "" {
			w.Write([]byte(`{"data":"paid tool response"}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"TARGETADDR","asset":"10458941","maxAmountRequired":"100000"}]}`))
	}))
	defer target.Close()

	node := models.WorkflowNode{ID: "x1", Type: models.NodeTypeTool402, Endpoint: target.URL}
	rc := engine.NewRunContext("r1", nil)
	aw := models.AgentWallet{}
	usdcSigner := &mockUSDCGroupSigner{group: []string{"g0", "g1"}, idx: 0}

	var released, committed bool
	var committedAmount int64
	ledger := nodes.PaymentLedger{
		Reserve: func(context.Context, int64) error { return nil },
		Commit:  func(_ context.Context, _ string, amount int64, _ string) { committed = true; committedAmount = amount },
		Release: func(context.Context, int64) { released = true },
	}

	relayCfg := nodes.X402RelayConfig{
		RunFundingID: "test-run-funding-2",
		Ledger:       ledger,
		Wallet2: nodes.Wallet2PayConfig{
			USDCSigner:                usdcSigner,
			PlatformWalletEncMnemonic: "platform-wallet-enc-mnemonic",
			USDCAssetID:               uint64(10458941),
			RelayFeePayer:             "FEEPAYERADDR",
			RelayNetwork:              "algorand:testnet",
		},
		RecordSettlement: func(context.Context, string, int64, bool) error {
			return errors.New("db unavailable")
		},
	}

	paymentResult, err := nodes.ExecuteTool402V2(context.Background(), node, rc, aw, nil, relayCfg)
	if err != nil {
		t.Fatalf("want nil error -- a failed audit write doesn't undo a real settled payment, got %v", err)
	}
	if released {
		t.Fatal("want the reservation NEVER released -- the payment settled, only the audit write failed")
	}
	if !committed || committedAmount != 100000 {
		t.Fatalf("want the reservation committed for 100000 despite the RecordSettlement failure, got committed=%v amount=%d", committed, committedAmount)
	}
	if paymentResult.SettledUSDMicros != 100000 {
		t.Fatalf("want SettledUSDMicros 100000, got %d", paymentResult.SettledUSDMicros)
	}
}
