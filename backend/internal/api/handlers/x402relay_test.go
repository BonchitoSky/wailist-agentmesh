package handlers_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/agentmesh/backend/internal/api/handlers"
	"github.com/agentmesh/backend/internal/db"
	"github.com/agentmesh/backend/internal/engine/nodes"
	"github.com/agentmesh/backend/internal/x402"
)

// TestMain relaxes the SSRF guard for this package's tests, mirroring the
// identical override in internal/engine/nodes and internal/engine's own
// TestMain — without it, the relay's SSRF check (added specifically because
// this route is public and unauthenticated) blocks every httptest.NewServer
// target (127.0.0.1), which is exactly what these tests use as fake
// downstream targets and a fake facilitator. No test in this package
// exercises the real SSRF-blocking validator.
func TestMain(m *testing.M) {
	nodes.SetURLValidatorForTest(func(string) error { return nil })
	os.Exit(m.Run())
}

func TestX402RelayNoPaymentMirrorsTargetPriceAsChallengeTag(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment") != "" {
			w.Write([]byte(`{"data":"paid response"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		json.NewEncoder(w).Encode(map[string]any{
			"x402Version": 2,
			"accepts": []map[string]any{{
				"scheme":            "exact",
				"network":           "algorand:SGO1GKSzyE7IEPItTxCByw9x8FmnrCDexi9/cOUJOiI=",
				"maxAmountRequired": "100000",
				"payTo":             "TARGETADDR",
				"asset":             "10458941",
			}},
		})
	}))
	defer target.Close()

	d := &handlers.Deps{
		PlatformWalletAddress: "PLATFORMADDR",
		USDCAssetID:           10458941,
		RelayNetwork:          "algorand:SGO1GKSzyE7IEPItTxCByw9x8FmnrCDexi9/cOUJOiI=",
		RelayFeePayer:         "FEEPAYERADDR",
	}
	req := httptest.NewRequest(http.MethodGet, "/x402/relay?target="+target.URL, nil)
	w := httptest.NewRecorder()

	d.X402Relay(w, req)

	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("want 402, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Accepts []map[string]any `json:"accepts"`
	}
	json.Unmarshal(w.Body.Bytes(), &body)
	if len(body.Accepts) != 1 {
		t.Fatalf("want 1 accepts entry, got %d", len(body.Accepts))
	}
	extra, _ := body.Accepts[0]["extra"].(map[string]any)
	if extra["tag"] != "x402-global-challenge" {
		t.Fatalf("want tag x402-global-challenge in extra, got %v", extra)
	}
	if body.Accepts[0]["payTo"] != "PLATFORMADDR" {
		t.Fatalf("want payTo=PLATFORMADDR (our own wallet, not the target's), got %v", body.Accepts[0]["payTo"])
	}
	if body.Accepts[0]["amount"] != "100000" {
		t.Fatalf("want price mirrored from target (100000), got %v", body.Accepts[0]["amount"])
	}
	_ = x402.PaymentPayload{} // referenced so import stays used once payment-path test is added below
}

// TestX402RelayAcceptsTargetAmountFieldDialect is a reproduce-then-fix
// regression test for the real bug found live against our own Prism-schema
// demo merchant (backend/cmd/x402demo) on 2026-07-31: its challenge uses
// `amount` (the current real-world x402 dialect — confirmed against Prism's
// own live endpoint and the official @x402/core v2.20 SDK), not
// `maxAmountRequired` (this codebase's own historical convention). The
// relay's own outward-facing challenge now emits `amount` too, matching
// GoPlausible's facilitator wire format. fetchTargetPriceQuote must still
// correctly parse a target
// that only sends `amount`.
func TestX402RelayAcceptsTargetAmountFieldDialect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		json.NewEncoder(w).Encode(map[string]any{
			"x402Version": 2,
			"accepts": []map[string]any{{
				"scheme":  "exact",
				"network": "algorand:SGO1GKSzyE7IEPItTxCByw9x8FmnrCDexi9/cOUJOiI=",
				"amount":  "250000",
				"payTo":   "TARGETADDR",
				"asset":   "10458941",
			}},
		})
	}))
	defer target.Close()

	d := &handlers.Deps{
		PlatformWalletAddress: "PLATFORMADDR",
		USDCAssetID:           10458941,
		RelayNetwork:          "algorand:SGO1GKSzyE7IEPItTxCByw9x8FmnrCDexi9/cOUJOiI=",
		RelayFeePayer:         "FEEPAYERADDR",
	}
	req := httptest.NewRequest(http.MethodGet, "/x402/relay?target="+target.URL, nil)
	w := httptest.NewRecorder()

	d.X402Relay(w, req)

	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("want 402, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Accepts []map[string]any `json:"accepts"`
	}
	json.Unmarshal(w.Body.Bytes(), &body)
	if len(body.Accepts) != 1 || body.Accepts[0]["amount"] != "250000" {
		t.Fatalf("want price parsed from target's `amount` field and mirrored as our own amount=250000, got %+v", body.Accepts)
	}
}

// TestX402RunFundingInfoReturnsStaticJSON pins the contract of the static,
// informational route FundRunReserve's PaymentRequirements.Resource points
// at (runfund.go: cfg.PublicBaseURL + "/x402/relay/run-funding") — a real,
// reachable route on our own domain, matching what a real Bazaar-catalog
// crawler would expect to find there. The exact path is load-bearing (it's
// embedded in every run-level pre-fund's on-chain payment requirements), so
// a 200 with the expected static shape is worth pinning even though there's
// no payment logic behind it.
func TestX402RunFundingInfoReturnsStaticJSON(t *testing.T) {
	d := &handlers.Deps{}
	req := httptest.NewRequest(http.MethodGet, "/x402/relay/run-funding", nil)
	w := httptest.NewRecorder()

	d.X402RunFundingInfo(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("want valid JSON body: %v", err)
	}
	if body["description"] == "" {
		t.Fatalf("want a non-empty description field, got %v", body)
	}
}

func newTestStoreForHandlers(t *testing.T) *db.Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	store, err := db.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}

// signCall captures one invocation of fakeUSDCSigner.SignUSDCPaymentGroup, so
// tests can assert on exactly what the relay actually signed and broadcast
// for the outbound platform-wallet payment.
type signCall struct {
	payTo        string
	assetID      uint64
	amountMicros uint64
}

type fakeUSDCSigner struct {
	group []string
	idx   int
	err   error

	calls []signCall
}

func (f *fakeUSDCSigner) SignUSDCPaymentGroup(_ context.Context, _, payTo string, assetID, amountMicros uint64, _ string) ([]string, int, error) {
	f.calls = append(f.calls, signCall{payTo: payTo, assetID: assetID, amountMicros: amountMicros})
	return f.group, f.idx, f.err
}

func TestX402RelayPaysTargetFromPlatformWalletAfterInboundSettles(t *testing.T) {
	var targetGotXPayment string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("X-Payment"); h != "" {
			targetGotXPayment = h
			w.Write([]byte(`{"data":"paid response from target"}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		json.NewEncoder(w).Encode(map[string]any{
			"accepts": []map[string]any{{"payTo": "TARGETADDR", "asset": "10458941", "maxAmountRequired": "50000"}},
		})
	}))
	defer target.Close()

	store := newTestStoreForHandlers(t) // TEST_DATABASE_URL-gated, see helper below

	// inbound_tx_id has a uniqueness constraint; target.URL's ephemeral port
	// can be reused across test runs against a long-lived Postgres, so a
	// txid keyed off it alone is not collision-proof over time -- suffix
	// with a monotonic timestamp (matches the fix applied to
	// x402_orchestrator_integration_test.go).
	inboundTxID := fmt.Sprintf("INBOUND-TX-%s-%d", target.URL, time.Now().UnixNano())

	// Captures the paymentRequirements the relay sent on /verify and /settle
	// so the test can assert the real target-quoted amount (50000, from the
	// fake target above) was actually threaded through and enforced, rather
	// than the previous hardcoded-0/no-enforcement behavior.
	var verifyReqs, settleReqs struct {
		PaymentRequirements struct {
			MaxAmountRequired string `json:"amount"`
		} `json:"paymentRequirements"`
	}
	facilitator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/verify" {
			json.Unmarshal(body, &verifyReqs)
			json.NewEncoder(w).Encode(map[string]any{"isValid": true})
			return
		}
		json.Unmarshal(body, &settleReqs)
		json.NewEncoder(w).Encode(map[string]any{"success": true, "transaction": inboundTxID})
	}))
	defer facilitator.Close()

	d := &handlers.Deps{
		Store:                     store,
		PlatformWalletAddress:     "PLATFORMADDR",
		PlatformWalletEncMnemonic: "enc-mnemonic",
		FacilitatorClient:         x402.NewFacilitatorClient(facilitator.URL),
		USDCAssetID:               10458941,
		RelayNetwork:              "algorand:testnet",
		RelayFeePayer:             "FEEPAYERADDR",
		USDCSigner:                &fakeUSDCSigner{group: []string{"g0", "g1"}, idx: 0},
	}

	payload, _ := json.Marshal(map[string]any{"x402Version": 2, "scheme": "exact", "network": "algorand:testnet"})
	req := httptest.NewRequest(http.MethodGet, "/x402/relay?target="+target.URL, nil)
	req.Header.Set("X-Payment", string(payload))
	w := httptest.NewRecorder()

	d.X402Relay(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if targetGotXPayment == "" {
		t.Fatal("want relay to have paid the target with its own X-Payment header")
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("paid response from target")) {
		t.Fatalf("want target's response relayed back, got %s", w.Body.String())
	}

	if verifyReqs.PaymentRequirements.MaxAmountRequired != "50000" {
		t.Fatalf("want facilitator Verify called with MaxAmountRequired=50000 (the target's real quote, for price enforcement), got %q", verifyReqs.PaymentRequirements.MaxAmountRequired)
	}
	if settleReqs.PaymentRequirements.MaxAmountRequired != "50000" {
		t.Fatalf("want facilitator Settle called with MaxAmountRequired=50000, got %q", settleReqs.PaymentRequirements.MaxAmountRequired)
	}

	row, err := store.GetX402RelaySettlementByInboundTx(context.Background(), inboundTxID)
	if err != nil {
		t.Fatalf("want to find the recorded ledger row: %v", err)
	}
	if row.AmountAssetMicros != 50000 {
		t.Fatalf("want ledger row to record the real settled amount (50000), got %d", row.AmountAssetMicros)
	}
}

// TestX402RelayForwardsConfiguredTargetMethodAndBody is a reproduce-then-fix
// regression test for the real bug found live against Prism
// (https://prism-99h2.onrender.com/resume-screen-accurate) on 2026-07-31: a
// POST-only x402 target 404s a bare GET before it ever considers payment
// state, so the relay's pre-existing hardcoded-GET/nil-body target fetch
// could never reach it at all. The fake target below mimics that shape (404
// on GET, real handling on POST+body) and asserts BOTH the unauthenticated
// challenge-mirror leg and the paid leg actually reach target as POST with
// the caller-supplied body -- confirming X-Relay-Method/X-Relay-Body (set
// once per relay call, mirroring how X-Payment already works) are threaded
// all the way through relayInboundChallenge, relaySettleAndForward, and
// payTargetAndRespond, not just one of the three.
func TestX402RelayForwardsConfiguredTargetMethodAndBody(t *testing.T) {
	const wantBody = `{"task_description":"test","files":[]}`
	var gotChallengeBody, gotPaidBody string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		if h := r.Header.Get("X-Payment"); h != "" {
			gotPaidBody = string(b)
			w.Write([]byte(`{"data":"paid response from target"}`))
			return
		}
		gotChallengeBody = string(b)
		w.WriteHeader(http.StatusPaymentRequired)
		json.NewEncoder(w).Encode(map[string]any{
			"accepts": []map[string]any{{"payTo": "TARGETADDR", "asset": "10458941", "maxAmountRequired": "50000"}},
		})
	}))
	defer target.Close()

	store := newTestStoreForHandlers(t)
	inboundTxID := fmt.Sprintf("INBOUND-TX-METHODBODY-%s-%d", target.URL, time.Now().UnixNano())

	facilitator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/verify" {
			json.NewEncoder(w).Encode(map[string]any{"isValid": true})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "transaction": inboundTxID})
	}))
	defer facilitator.Close()

	d := &handlers.Deps{
		Store:                     store,
		PlatformWalletAddress:     "PLATFORMADDR",
		PlatformWalletEncMnemonic: "enc-mnemonic",
		FacilitatorClient:         x402.NewFacilitatorClient(facilitator.URL),
		USDCAssetID:               10458941,
		RelayNetwork:              "algorand:testnet",
		RelayFeePayer:             "FEEPAYERADDR",
		USDCSigner:                &fakeUSDCSigner{group: []string{"g0", "g1"}, idx: 0},
	}

	// Unauthenticated leg: relayInboundChallenge must also use the
	// configured method/body to even see a real 402 back from a POST-only
	// target, not a 404.
	challengeReq := httptest.NewRequest(http.MethodGet, "/x402/relay?target="+target.URL, nil)
	challengeReq.Header.Set("X-Relay-Method", http.MethodPost)
	challengeReq.Header.Set("X-Relay-Body", base64.StdEncoding.EncodeToString([]byte(wantBody)))
	challengeW := httptest.NewRecorder()
	d.X402Relay(challengeW, challengeReq)

	if challengeW.Code != http.StatusPaymentRequired {
		t.Fatalf("want 402 mirrored from the target's real POST-only challenge, got %d: %s", challengeW.Code, challengeW.Body.String())
	}
	if gotChallengeBody != wantBody {
		t.Fatalf("want target to receive the configured body on the unauthenticated leg, got %q", gotChallengeBody)
	}

	// Paid leg: same X-Relay-Method/X-Relay-Body, now alongside X-Payment.
	payload, _ := json.Marshal(map[string]any{"x402Version": 2, "scheme": "exact", "network": "algorand:testnet"})
	paidReq := httptest.NewRequest(http.MethodGet, "/x402/relay?target="+target.URL, nil)
	paidReq.Header.Set("X-Payment", string(payload))
	paidReq.Header.Set("X-Relay-Method", http.MethodPost)
	paidReq.Header.Set("X-Relay-Body", base64.StdEncoding.EncodeToString([]byte(wantBody)))
	paidW := httptest.NewRecorder()
	d.X402Relay(paidW, paidReq)

	if paidW.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", paidW.Code, paidW.Body.String())
	}
	if gotPaidBody != wantBody {
		t.Fatalf("want target to receive the configured body on the paid leg, got %q", gotPaidBody)
	}
	if !bytes.Contains(paidW.Body.Bytes(), []byte("paid response from target")) {
		t.Fatalf("want target's response relayed back, got %s", paidW.Body.String())
	}
}

// TestX402RelayUsesSingleQuoteForBothSettlementAndOutboundPayment is a
// regression test for a fund-drain vector: relaySettleAndForward used to
// fetch the target's price quote once (to enforce/record it), and
// payTargetAndRespond then independently re-fetched the SAME caller-supplied
// target URL a second time to learn what to actually sign and pay. Since
// `target` is arbitrary and attacker-controlled on this public,
// unauthenticated route, a malicious target could answer the first
// (price-enforcement) fetch with a cheap price and the second (pay-time)
// fetch with an inflated amount and/or a different payTo — causing the
// platform wallet to sign and broadcast a payment for more than was ever
// collected from the caller, to an address the caller chose.
//
// The fake target below tracks how many unauthenticated (no X-Payment)
// requests it has received and answers the first one cheaply
// (TARGETADDR-CHEAP / 50000) and every subsequent one expensively
// (ATTACKERADDR / 999000000). Under the old buggy code this is exactly two
// independent fetches — one in relaySettleAndForward, one in
// payTargetAndRespond — so the outbound payment would be signed for
// 999000000 to ATTACKERADDR while the ledger recorded only 50000. Under the
// fixed code there is only one fetch, reused for both purposes, so the
// signed outbound payment must match the cheap, recorded amount and address.
func TestX402RelayUsesSingleQuoteForBothSettlementAndOutboundPayment(t *testing.T) {
	var quoteFetches int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment") != "" {
			w.Write([]byte(`{"data":"paid response from target"}`))
			return
		}
		quoteFetches++
		w.WriteHeader(http.StatusPaymentRequired)
		if quoteFetches == 1 {
			json.NewEncoder(w).Encode(map[string]any{
				"accepts": []map[string]any{{"payTo": "TARGETADDR-CHEAP", "asset": "10458941", "maxAmountRequired": "50000"}},
			})
			return
		}
		// A malicious target would only do this on a second, pay-time-only
		// fetch that should never happen under the fix.
		json.NewEncoder(w).Encode(map[string]any{
			"accepts": []map[string]any{{"payTo": "ATTACKERADDR", "asset": "10458941", "maxAmountRequired": "999000000"}},
		})
	}))
	defer target.Close()

	store := newTestStoreForHandlers(t)

	inboundTxID := fmt.Sprintf("INBOUND-TX-SINGLEQUOTE-%s-%d", target.URL, time.Now().UnixNano())
	facilitator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/verify" {
			_ = body
			json.NewEncoder(w).Encode(map[string]any{"isValid": true})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "transaction": inboundTxID})
	}))
	defer facilitator.Close()

	signer := &fakeUSDCSigner{group: []string{"g0", "g1"}, idx: 0}
	d := &handlers.Deps{
		Store:                     store,
		PlatformWalletAddress:     "PLATFORMADDR",
		PlatformWalletEncMnemonic: "enc-mnemonic",
		FacilitatorClient:         x402.NewFacilitatorClient(facilitator.URL),
		USDCAssetID:               10458941,
		RelayNetwork:              "algorand:testnet",
		RelayFeePayer:             "FEEPAYERADDR",
		USDCSigner:                signer,
	}

	payload, _ := json.Marshal(map[string]any{"x402Version": 2, "scheme": "exact", "network": "algorand:testnet"})
	req := httptest.NewRequest(http.MethodGet, "/x402/relay?target="+target.URL, nil)
	req.Header.Set("X-Payment", string(payload))
	w := httptest.NewRecorder()

	d.X402Relay(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	row, err := store.GetX402RelaySettlementByInboundTx(context.Background(), inboundTxID)
	if err != nil {
		t.Fatalf("want to find the recorded ledger row: %v", err)
	}
	if row.AmountAssetMicros != 50000 {
		t.Fatalf("want ledger row to record the cheap quoted amount (50000), got %d", row.AmountAssetMicros)
	}

	if quoteFetches != 1 {
		t.Fatalf("want exactly one price-quote fetch of the caller-supplied target per relay cycle, got %d — a second independent fetch is exactly the fund-drain gap this test guards against", quoteFetches)
	}

	if len(signer.calls) != 1 {
		t.Fatalf("want exactly one outbound sign call, got %d", len(signer.calls))
	}
	got := signer.calls[0]
	if uint64(row.AmountAssetMicros) != got.amountMicros {
		t.Fatalf("want the amount actually signed for the outbound payment (%d) to match the amount recorded in the ledger (%d) — a mismatch means the relay paid a different amount than it collected/recorded", got.amountMicros, row.AmountAssetMicros)
	}
	if got.payTo != "TARGETADDR-CHEAP" {
		t.Fatalf("want outbound payment signed to the same payTo used for the recorded settlement (TARGETADDR-CHEAP), got %q — this is the attacker-address-substitution half of the fund-drain vector", got.payTo)
	}
}

// TestX402RelayRejectsOutboundPaymentInWrongAsset is a regression test for a
// gap where payTargetAndRespond blindly trusted quote.Asset (parsed straight
// out of the target's own, caller-supplied, unauthenticated 402 response) as
// the asset id to sign and broadcast the outbound platform-wallet payment in
// — with no check that it matches d.USDCAssetID, the platform's own
// designated USDC asset id (already anchored and enforced on the inbound
// settlement side via reqs.Asset in relaySettleAndForward). A malicious
// target could consistently, in its single quote response, claim some other
// asset id it controls (or one that doesn't exist/has no value) and the
// relay would still sign and send a real payment in that asset.
//
// The fake target here claims asset "99999999" (not d.USDCAssetID,
// 10458941) consistently on every unauthenticated fetch — so this is not the
// two-different-fetches fund-drain vector from the test above, just a single
// quote that names the wrong asset throughout.
//
// The check now runs in relaySettleAndForward, before the facilitator's
// Verify/Settle are ever called — not just before paying the target in
// payTargetAndRespond. Checking it only in payTargetAndRespond (after
// RecordInboundSettlement) would have set X-Inbound-Settled on the response
// and let tool402.go bill the caller in full for a call that was never
// going to reach the target; catching it earlier means neither the inbound
// leg nor an outbound-settlement row exist at all — there is nothing to
// bill and nothing to reconcile.
func TestX402RelayRejectsOutboundPaymentInWrongAsset(t *testing.T) {
	var targetGotPaidRequest bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment") != "" {
			targetGotPaidRequest = true
			w.Write([]byte(`{"data":"paid response from target"}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		json.NewEncoder(w).Encode(map[string]any{
			"accepts": []map[string]any{{"payTo": "TARGETADDR", "asset": "99999999", "maxAmountRequired": "50000"}},
		})
	}))
	defer target.Close()

	store := newTestStoreForHandlers(t)

	inboundTxID := fmt.Sprintf("INBOUND-TX-WRONGASSET-%s-%d", target.URL, time.Now().UnixNano())
	var facilitatorHitPaths []string
	facilitator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		facilitatorHitPaths = append(facilitatorHitPaths, r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/verify" {
			_ = body
			json.NewEncoder(w).Encode(map[string]any{"isValid": true})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "transaction": inboundTxID})
	}))
	defer facilitator.Close()

	signer := &fakeUSDCSigner{group: []string{"g0", "g1"}, idx: 0}
	d := &handlers.Deps{
		Store:                     store,
		PlatformWalletAddress:     "PLATFORMADDR",
		PlatformWalletEncMnemonic: "enc-mnemonic",
		FacilitatorClient:         x402.NewFacilitatorClient(facilitator.URL),
		USDCAssetID:               10458941, // does NOT match the target's claimed asset (99999999)
		RelayNetwork:              "algorand:testnet",
		RelayFeePayer:             "FEEPAYERADDR",
		USDCSigner:                signer,
	}

	payload, _ := json.Marshal(map[string]any{"x402Version": 2, "scheme": "exact", "network": "algorand:testnet"})
	req := httptest.NewRequest(http.MethodGet, "/x402/relay?target="+target.URL, nil)
	req.Header.Set("X-Payment", string(payload))
	w := httptest.NewRecorder()

	d.X402Relay(w, req)

	if w.Code == http.StatusOK {
		t.Fatalf("want relay to refuse to pay the target in a non-USDC asset, got 200: %s", w.Body.String())
	}
	if w.Header().Get("X-Inbound-Settled") == "true" {
		t.Fatal("want X-Inbound-Settled unset -- nothing settled, the caller must not be billed")
	}

	if len(facilitatorHitPaths) != 0 {
		t.Fatalf("want the facilitator (verify/settle) to never be called when the target's quoted asset is rejected up front, got calls to %v", facilitatorHitPaths)
	}
	if len(signer.calls) != 0 {
		t.Fatalf("want the USDC signer to never be called when the target's quoted asset does not match d.USDCAssetID, got %d call(s): %+v", len(signer.calls), signer.calls)
	}
	if targetGotPaidRequest {
		t.Fatal("want the relay to never send a paid request to the target when the quoted asset does not match d.USDCAssetID")
	}

	if _, err := store.GetX402RelaySettlementByInboundTx(context.Background(), inboundTxID); err == nil {
		t.Fatal("want no settlement row at all -- the inbound leg was never settled, so there is nothing to record")
	}
}

// TestX402RelayDoesNotBillWhenOutboundSigningFails is a regression test: the
// relay used to set X-Inbound-Settled as soon as the inbound leg settled,
// before the outbound payment to target was ever signed. If signing then
// failed (bad payTo, an algod outage, ...) the target received nothing at
// all, but the header was already set -- tool402.go would still commit the
// full amount, over-billing the caller for a call where no money reached
// the target and no submittable claim against the platform wallet was ever
// created. The header must only be set after SignUSDCPaymentGroup succeeds.
func TestX402RelayDoesNotBillWhenOutboundSigningFails(t *testing.T) {
	var targetGotPaidRequest bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment") != "" {
			targetGotPaidRequest = true
			w.Write([]byte(`{"data":"paid response from target"}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		json.NewEncoder(w).Encode(map[string]any{
			"accepts": []map[string]any{{"payTo": "TARGETADDR", "asset": "10458941", "maxAmountRequired": "50000"}},
		})
	}))
	defer target.Close()

	store := newTestStoreForHandlers(t)

	inboundTxID := fmt.Sprintf("INBOUND-TX-SIGNFAIL-%s-%d", target.URL, time.Now().UnixNano())
	facilitator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/verify" {
			json.NewEncoder(w).Encode(map[string]any{"isValid": true})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "transaction": inboundTxID})
	}))
	defer facilitator.Close()

	signer := &fakeUSDCSigner{err: fmt.Errorf("invalid receiver address")}
	d := &handlers.Deps{
		Store:                     store,
		PlatformWalletAddress:     "PLATFORMADDR",
		PlatformWalletEncMnemonic: "enc-mnemonic",
		FacilitatorClient:         x402.NewFacilitatorClient(facilitator.URL),
		USDCAssetID:               10458941,
		RelayNetwork:              "algorand:testnet",
		RelayFeePayer:             "FEEPAYERADDR",
		USDCSigner:                signer,
	}

	payload, _ := json.Marshal(map[string]any{"x402Version": 2, "scheme": "exact", "network": "algorand:testnet"})
	req := httptest.NewRequest(http.MethodGet, "/x402/relay?target="+target.URL, nil)
	req.Header.Set("X-Payment", string(payload))
	w := httptest.NewRecorder()

	d.X402Relay(w, req)

	if w.Code == http.StatusOK {
		t.Fatalf("want relay to report the signing failure, got 200: %s", w.Body.String())
	}
	if w.Header().Get("X-Inbound-Settled") == "true" {
		t.Fatal("want X-Inbound-Settled unset when outbound signing fails -- no signed payment group exists, so the caller must not be billed")
	}
	if targetGotPaidRequest {
		t.Fatal("want the relay to never send a paid request to the target when signing the outbound payment failed")
	}

	row, err := store.GetX402RelaySettlementByInboundTx(context.Background(), inboundTxID)
	if err != nil {
		t.Fatalf("want the inbound settlement row to still exist (that leg genuinely settled): %v", err)
	}
	if row.Status != "failed" {
		t.Fatalf("want the outbound leg recorded as failed, got status %q", row.Status)
	}
}

// TestX402RelayRecordsFailedWhenTargetRejectsOutboundPayment is a regression
// test: payTargetAndRespond used to record the outbound leg as "settled" as
// soon as the paid HTTP request to the target completed without a transport
// error, without ever checking the target's actual status code. A target
// that rejects the platform wallet's payment (e.g. it still returns 402, or
// errors with a 5xx) would then be recorded as a successful settlement even
// though the downstream leg the whole Orchestrator entry depends on never
// actually happened. The fake target below always answers a paid
// (X-Payment-bearing) request with 402 — simulating a rejected outbound
// payment — and the test asserts the ledger row ends up "failed", and that
// the relay surfaces the target's real (non-200) status code to the caller
// rather than claiming success.
func TestX402RelayRecordsFailedWhenTargetRejectsOutboundPayment(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		json.NewEncoder(w).Encode(map[string]any{
			"accepts": []map[string]any{{"payTo": "TARGETADDR", "asset": "10458941", "maxAmountRequired": "50000"}},
		})
	}))
	defer target.Close()

	store := newTestStoreForHandlers(t)

	inboundTxID := fmt.Sprintf("INBOUND-TX-REJECTED-%s-%d", target.URL, time.Now().UnixNano())
	facilitator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/verify" {
			json.NewEncoder(w).Encode(map[string]any{"isValid": true})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "transaction": inboundTxID})
	}))
	defer facilitator.Close()

	d := &handlers.Deps{
		Store:                     store,
		PlatformWalletAddress:     "PLATFORMADDR",
		PlatformWalletEncMnemonic: "enc-mnemonic",
		FacilitatorClient:         x402.NewFacilitatorClient(facilitator.URL),
		USDCAssetID:               10458941,
		RelayNetwork:              "algorand:testnet",
		RelayFeePayer:             "FEEPAYERADDR",
		USDCSigner:                &fakeUSDCSigner{group: []string{"g0", "g1"}, idx: 0},
	}

	payload, _ := json.Marshal(map[string]any{"x402Version": 2, "scheme": "exact", "network": "algorand:testnet"})
	req := httptest.NewRequest(http.MethodGet, "/x402/relay?target="+target.URL, nil)
	req.Header.Set("X-Payment", string(payload))
	w := httptest.NewRecorder()

	d.X402Relay(w, req)

	if w.Code == http.StatusOK {
		t.Fatalf("want the relay to surface the target's real rejection status, not claim success, got %d: %s", w.Code, w.Body.String())
	}

	row, err := store.GetX402RelaySettlementByInboundTx(context.Background(), inboundTxID)
	if err != nil {
		t.Fatalf("want to find the recorded ledger row: %v", err)
	}
	if row.Status != "failed" {
		t.Fatalf("want the outbound settlement recorded as failed when the target rejects the payment, got status %q", row.Status)
	}
}
