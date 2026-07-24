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

	checkBalance := func(context.Context, int64) error { return nil }
	paymentResult, err := nodes.ExecuteTool402V2(context.Background(), node, rc, aw, signer, usdcSigner, "platform-enc-mnemonic", uint64(10458941), relay.URL, checkBalance)
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

	checkBalance := func(context.Context, int64) error { return nil }
	paymentResult, err := nodes.ExecuteTool402V2(context.Background(), node, rc, aw, signer, usdcSigner, "platform-enc-mnemonic", uint64(10458941), relay.URL, checkBalance)
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

	checkBalance := func(context.Context, int64) error { return nil }
	paymentResult, err := nodes.ExecuteTool402V2(context.Background(), node, rc, aw, signer, usdcSigner, "platform-enc-mnemonic", uint64(10458941), relay.URL, checkBalance)
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
	checkBalance := func(_ context.Context, amount int64) error {
		checkedAmount = amount
		if amount > 500_000 {
			return fmt.Errorf("insufficient credits")
		}
		return nil
	}

	// maxAmountRequired (100000) is under the flat-fee-sized ceiling this
	// checkBalance allows, so this call should succeed and pay.
	_, err := nodes.ExecuteTool402V2(context.Background(), node, rc, aw, nil, usdcSigner, "platform-enc-mnemonic", uint64(10458941), relay.URL, checkBalance)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if checkedAmount != 100000 {
		t.Fatalf("want checkBalance called with real amount 100000, got %d", checkedAmount)
	}
	if !paid {
		t.Fatal("want relay to have been paid")
	}

	// Now make checkBalance reject anything over 50000 — below the real
	// 100000 cost — and verify the payment never happens.
	paid = false
	strictCheck := func(_ context.Context, amount int64) error {
		if amount > 50_000 {
			return fmt.Errorf("insufficient credits")
		}
		return nil
	}
	_, err = nodes.ExecuteTool402V2(context.Background(), node, rc, aw, nil, usdcSigner, "platform-enc-mnemonic", uint64(10458941), relay.URL, strictCheck)
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

	checkBalance := func(context.Context, int64) error { return nil }
	paymentResult, err := nodes.ExecuteTool402V2(context.Background(), node, rc, aw, signer, usdcSigner, "platform-enc-mnemonic", uint64(10458941), relay.URL, checkBalance)
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
}
