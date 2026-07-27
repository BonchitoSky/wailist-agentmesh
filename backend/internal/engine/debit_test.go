package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentmesh/backend/internal/db"
	"github.com/agentmesh/backend/internal/engine/nodes"
	"github.com/agentmesh/backend/internal/models"
	"github.com/agentmesh/backend/internal/sse"
)

// TestMain sets the permissive URL validator once for the whole engine_test
// binary, mirroring the identical override in internal/engine/nodes'
// TestMain (tool402_test.go). Without it, the SSRF guard in nodes.ExecuteTool
// blocks every httptest.NewServer target (127.0.0.1) with "requests to
// private/internal addresses are not allowed" — unrelated to billing, but it
// otherwise prevents these tests from ever observing a successful HTTP call.
// No test in this package exercises the real SSRF-blocking validator.
func TestMain(m *testing.M) {
	nodes.SetURLValidatorForTest(func(string) error { return nil })
	os.Exit(m.Run())
}

// fundUser mirrors the identical helper in internal/db/debit_test.go — kept
// separate since it's a different package and this is the only place it's
// needed here.
func fundUser(t *testing.T, store *db.Store, userID string, micros int64) {
	t.Helper()
	ctx := context.Background()
	orderID := fmt.Sprintf("fund_%s_%d", userID, time.Now().UnixNano())
	fxRate := float64(micros) / 1e6
	if _, err := store.CreateCreditTransaction(ctx, userID, orderID, 100, fxRate); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CompleteCreditTransaction(ctx, "razorpay", orderID, "pay_"+orderID); err != nil {
		t.Fatal(err)
	}
}

func waitForRunDone(t *testing.T, store *db.Store, runID string) models.Run {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, err := store.GetRun(context.Background(), runID)
		if err == nil && run.Status != models.RunStatusRunning {
			return run
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run did not finish in time")
	return models.Run{}
}

func TestByokFlatFeeChargedOnHTTPToolSuccess(t *testing.T) {
	runner, store := newTestRunner(t)
	ctx := context.Background()

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	email := fmt.Sprintf("byok-tool-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(ctx, email, "hash")
	if err != nil {
		t.Fatal(err)
	}
	fundUser(t, store, user.ID, 100000) // 10 cents

	wf, err := store.CreateWorkflow(ctx, "BYOK Tool Test", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })

	graph := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			{ID: "n2", Type: models.NodeTypeTool, Template: "http", URL: srv.URL, Method: "GET"},
			{ID: "n3", Type: models.NodeTypeEnd},
		},
		Edges: []models.WorkflowEdge{
			{ID: "e1", From: "n1", To: "n2", Kind: models.EdgeKindFlow},
			{ID: "e2", From: "n2", To: "n3", Kind: models.EdgeKindFlow},
		},
	}
	wf, _ = store.UpdateWorkflow(ctx, wf.ID, wf.Name, graph)

	run, err := store.CreateRun(ctx, wf.ID, "test", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	broker := sse.NewBroker()
	broker.Create(run.ID)

	runner.Start(wf, run)
	final := waitForRunDone(t, store, run.ID)
	if final.Status != models.RunStatusSuccess {
		t.Fatalf("want success got %s", final.Status)
	}
	if hits != 1 {
		t.Fatalf("want exactly 1 request to the test server, got %d", hits)
	}

	balance, err := store.GetCreditBalance(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance != 90000 {
		t.Fatalf("want balance 90000 got %d", balance)
	}

	entries, err := store.ListDebitLedger(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Kind != models.DebitKindByokFlatFee || entries[0].AmountUSDMicros != 10000 {
		t.Fatalf("unexpected ledger entries: %+v", entries)
	}
}

func TestToolCalcNodeNotCharged(t *testing.T) {
	runner, store := newTestRunner(t)
	ctx := context.Background()

	email := fmt.Sprintf("free-calc-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(ctx, email, "hash")
	if err != nil {
		t.Fatal(err)
	}
	fundUser(t, store, user.ID, 10000) // exactly one flat fee's worth

	wf, err := store.CreateWorkflow(ctx, "Free Calc Test", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })

	graph := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			{ID: "n2", Type: models.NodeTypeTool, Template: "calc", URL: "1+1"},
			{ID: "n3", Type: models.NodeTypeEnd},
		},
		Edges: []models.WorkflowEdge{
			{ID: "e1", From: "n1", To: "n2", Kind: models.EdgeKindFlow},
			{ID: "e2", From: "n2", To: "n3", Kind: models.EdgeKindFlow},
		},
	}
	wf, _ = store.UpdateWorkflow(ctx, wf.ID, wf.Name, graph)

	run, err := store.CreateRun(ctx, wf.ID, "test", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	broker := sse.NewBroker()
	broker.Create(run.ID)

	runner.Start(wf, run)
	final := waitForRunDone(t, store, run.ID)
	if final.Status != models.RunStatusSuccess {
		t.Fatalf("want success got %s", final.Status)
	}

	balance, err := store.GetCreditBalance(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance != 10000 {
		t.Fatalf("calc node must stay free: want balance unchanged at 10000, got %d", balance)
	}
}

func TestInsufficientBalanceBlocksToolNodeBeforeExecution(t *testing.T) {
	runner, store := newTestRunner(t)
	ctx := context.Background()

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	email := fmt.Sprintf("broke-tool-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(ctx, email, "hash")
	if err != nil {
		t.Fatal(err)
	}
	// No funding — balance starts at 0, below the 10000-micros flat fee.

	wf, err := store.CreateWorkflow(ctx, "Broke Tool Test", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })

	graph := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			{ID: "n2", Type: models.NodeTypeTool, Template: "http", URL: srv.URL, Method: "GET"},
			{ID: "n3", Type: models.NodeTypeEnd},
		},
		Edges: []models.WorkflowEdge{
			{ID: "e1", From: "n1", To: "n2", Kind: models.EdgeKindFlow},
			{ID: "e2", From: "n2", To: "n3", Kind: models.EdgeKindFlow},
		},
	}
	wf, _ = store.UpdateWorkflow(ctx, wf.ID, wf.Name, graph)

	run, err := store.CreateRun(ctx, wf.ID, "test", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	broker := sse.NewBroker()
	broker.Create(run.ID)

	runner.Start(wf, run)
	final := waitForRunDone(t, store, run.ID)
	if final.Status != models.RunStatusFailed {
		t.Fatalf("want failed got %s", final.Status)
	}
	if hits != 0 {
		t.Fatalf("want zero requests to the test server (blocked pre-flight), got %d", hits)
	}

	balance, err := store.GetCreditBalance(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance != 0 {
		t.Fatalf("want balance unchanged at 0, got %d", balance)
	}
}

func TestAgentNodeChargesOwnFeeAndAttachedToolCalls(t *testing.T) {
	runner, store := newTestRunner(t)
	ctx := context.Background()

	toolSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":"tool ran"}`))
	}))
	defer toolSrv.Close()

	callCount := 0
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"search_tool","arguments":"{}"}}]}}]}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer llmSrv.Close()
	nodes.SetOpenAIBaseURL(llmSrv.URL)
	defer nodes.SetOpenAIBaseURL("https://api.openai.com")

	email := fmt.Sprintf("agent-fee-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(ctx, email, "hash")
	if err != nil {
		t.Fatal(err)
	}
	fundUser(t, store, user.ID, 100000) // 10 cents: 1 agent fee + 1 tool fee = 20000, plenty of headroom

	wf, err := store.CreateWorkflow(ctx, "Agent Fee Test", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })

	graph := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			{ID: "agent1", Type: models.NodeTypeAgent},
			{ID: "provider1", Type: models.NodeTypeProvider, Template: "openai", APIKey: "test-key", Model: "gpt-4o"},
			{ID: "tool1", Type: models.NodeTypeTool, Name: "search_tool", Template: "http", URL: toolSrv.URL, Method: "GET"},
			{ID: "n3", Type: models.NodeTypeEnd},
		},
		Edges: []models.WorkflowEdge{
			{ID: "e1", From: "n1", To: "agent1", Kind: models.EdgeKindFlow},
			{ID: "e2", From: "agent1", To: "n3", Kind: models.EdgeKindFlow},
			{ID: "e3", From: "provider1", To: "agent1", Kind: models.EdgeKindAttach, ToPort: "model"},
			{ID: "e4", From: "tool1", To: "agent1", Kind: models.EdgeKindAttach, ToPort: "tools"},
		},
	}
	wf, _ = store.UpdateWorkflow(ctx, wf.ID, wf.Name, graph)

	run, err := store.CreateRun(ctx, wf.ID, "test", []byte(`{"message":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	broker := sse.NewBroker()
	broker.Create(run.ID)

	runner.Start(wf, run)
	final := waitForRunDone(t, store, run.ID)
	if final.Status != models.RunStatusSuccess {
		t.Fatalf("want success got %s", final.Status)
	}

	balance, err := store.GetCreditBalance(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 100000 - 10000 (agent's own fee) - 10000 (attached tool call) = 80000
	if balance != 80000 {
		t.Fatalf("want balance 80000 got %d", balance)
	}

	entries, err := store.ListDebitLedger(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 ledger entries (agent fee + tool fee), got %d: %+v", len(entries), entries)
	}
	var sawAgentFee, sawToolFee bool
	for _, e := range entries {
		if e.NodeID == "agent1" && e.Kind == models.DebitKindByokFlatFee {
			sawAgentFee = true
		}
		if e.NodeID == "tool1" && e.Kind == models.DebitKindByokFlatFee {
			sawToolFee = true
		}
	}
	if !sawAgentFee || !sawToolFee {
		t.Fatalf("want one ledger entry for agent1 and one for tool1, got %+v", entries)
	}
}

func TestStandaloneTool402ChargesFeeOnlyWhenPaymentSent(t *testing.T) {
	runner, store := newTestRunner(t)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment-Txid") != "" {
			w.Write([]byte(`{"ok":true}`))
			return
		}
		w.Header().Set("X-Payment-Required", `{"price":"0.001","unit":"call","network":"algorand-testnet","recipient":"ALGO123"}`)
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()

	email := fmt.Sprintf("x402-standalone-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(ctx, email, "hash")
	if err != nil {
		t.Fatal(err)
	}
	fundUser(t, store, user.ID, 1000000) // $1, plenty for one $0.50 fee

	wf, err := store.CreateWorkflow(ctx, "X402 Standalone Test", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })

	// Deliberately no agent wallet and no attach edge for x1 — this exercises
	// the "no signer configured" path (see the note after this test).

	graph := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			{ID: "x1", Type: models.NodeTypeTool402, Endpoint: srv.URL},
			{ID: "n3", Type: models.NodeTypeEnd},
		},
		Edges: []models.WorkflowEdge{
			{ID: "e1", From: "n1", To: "x1", Kind: models.EdgeKindFlow},
			{ID: "e2", From: "x1", To: "n3", Kind: models.EdgeKindFlow},
		},
	}
	wf, _ = store.UpdateWorkflow(ctx, wf.ID, wf.Name, graph)

	run, err := store.CreateRun(ctx, wf.ID, "test", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	broker := sse.NewBroker()
	broker.Create(run.ID)

	runner.Start(wf, run)
	final := waitForRunDone(t, store, run.ID)
	if final.Status != models.RunStatusSuccess {
		t.Fatalf("want success got %s", final.Status)
	}

	// No agent attach edge targets x1, so runner.executeNode resolves an empty
	// AgentWallet for it — ExecuteTool402 degrades gracefully (no signer
	// configured), so no payment is sent and no fee should be charged.
	balance, err := store.GetCreditBalance(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance != 1000000 {
		t.Fatalf("want balance unchanged at 1000000 (no wallet configured, no payment sent), got %d", balance)
	}
	entries, err := store.ListDebitLedger(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("want 0 ledger entries, got %d", len(entries))
	}
}

func TestAgentBlocksAttachedX402CallWhenBalanceInsufficientForFee(t *testing.T) {
	runner, store := newTestRunner(t)
	ctx := context.Background()

	var x402Hits int
	x402Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		x402Hits++
		w.Header().Set("X-Payment-Required", `{"price":"0.001","unit":"call","network":"algorand-testnet","recipient":"ALGO123"}`)
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer x402Srv.Close()

	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"role": "assistant",
				"tool_calls": []map[string]any{{
					"id":       "call_1",
					"type":     "function",
					"function": map[string]any{"name": "paid_tool", "arguments": "{}"},
				}},
			}}},
		})
	}))
	defer llmSrv.Close()

	email := fmt.Sprintf("agent-x402-broke-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(ctx, email, "hash")
	if err != nil {
		t.Fatal(err)
	}
	// Exactly enough for the agent's own $0.01 fee, nowhere near the attached
	// tool402 call's $0.50 fee.
	fundUser(t, store, user.ID, 10_000)

	wf, err := store.CreateWorkflow(ctx, "Agent X402 Broke Test", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })

	nodes.SetOpenAIBaseURL(llmSrv.URL)

	graph := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			{ID: "p1", Type: models.NodeTypeProvider, Template: "openai", APIKey: "test-key", Model: "gpt-4o"},
			{ID: "a1", Type: models.NodeTypeAgent},
			{ID: "x1", Type: models.NodeTypeTool402, Name: "paid_tool", Endpoint: x402Srv.URL},
			{ID: "n3", Type: models.NodeTypeEnd},
		},
		Edges: []models.WorkflowEdge{
			{ID: "e1", From: "n1", To: "a1", Kind: models.EdgeKindFlow},
			{ID: "e2", From: "a1", To: "n3", Kind: models.EdgeKindFlow},
			{ID: "e3", From: "p1", To: "a1", Kind: models.EdgeKindAttach, ToPort: "model"},
			{ID: "e4", From: "x1", To: "a1", Kind: models.EdgeKindAttach, ToPort: "tools"},
		},
	}
	wf, _ = store.UpdateWorkflow(ctx, wf.ID, wf.Name, graph)

	run, err := store.CreateRun(ctx, wf.ID, "test", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	broker := sse.NewBroker()
	broker.Create(run.ID)

	runner.Start(wf, run)
	final := waitForRunDone(t, store, run.ID)
	if final.Status != models.RunStatusFailed {
		t.Fatalf("want failed got %s", final.Status)
	}
	// Exactly 1, not 0: reserveAndFundRun (Task 5) now probes every attached
	// tool402 node's price up front, before the agent's LLM turn even
	// starts, to size the run-level reservation -- this is a real HTTP GET
	// to the endpoint, distinct from actually attempting to pay it. This
	// endpoint speaks the legacy flat-quote dialect (no accepts[]), so the
	// probe correctly reports isV2=false and contributes nothing to the
	// estimate (which stays 0, so this agent never gets a run-level funding
	// id) -- the pre-existing floor guard below still blocks the real
	// attached call before any second request.
	if x402Hits != 1 {
		t.Fatalf("want exactly 1 request to the x402 server (reserveAndFundRun's price probe; the real attached call is still blocked before execution), got %d", x402Hits)
	}

	balance, err := store.GetCreditBalance(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance != 0 {
		t.Fatalf("want balance 0 (agent's own $0.01 fee charged, attached call blocked before it could spend anything else), got %d", balance)
	}
	entries, err := store.ListDebitLedger(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want exactly 1 ledger entry (the agent's own fee), got %d", len(entries))
	}
	if entries[0].Kind != models.DebitKindByokFlatFee || entries[0].NodeID != "a1" {
		t.Fatalf("want a single byok_flat_fee entry for node a1, got kind=%s node=%s", entries[0].Kind, entries[0].NodeID)
	}
}

func TestActionSkipPathNotBilled(t *testing.T) {
	runner, store := newTestRunner(t)
	ctx := context.Background()

	email := fmt.Sprintf("action-skip-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(ctx, email, "hash")
	if err != nil {
		t.Fatal(err)
	}
	fundUser(t, store, user.ID, 10_000) // exactly $0.01, would cover the fee if charged

	wf, err := store.CreateWorkflow(ctx, "Action Skip Test", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })

	graph := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			// Slack action node with no webhook URL configured — skip path.
			{ID: "a1", Type: models.NodeTypeAction, Template: "slack"},
			{ID: "n3", Type: models.NodeTypeEnd},
		},
		Edges: []models.WorkflowEdge{
			{ID: "e1", From: "n1", To: "a1", Kind: models.EdgeKindFlow},
			{ID: "e2", From: "a1", To: "n3", Kind: models.EdgeKindFlow},
		},
	}
	wf, _ = store.UpdateWorkflow(ctx, wf.ID, wf.Name, graph)

	run, err := store.CreateRun(ctx, wf.ID, "test", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	broker := sse.NewBroker()
	broker.Create(run.ID)

	runner.Start(wf, run)
	final := waitForRunDone(t, store, run.ID)
	if final.Status != models.RunStatusSuccess {
		t.Fatalf("want success got %s", final.Status)
	}

	balance, err := store.GetCreditBalance(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance != 10_000 {
		t.Fatalf("want balance unchanged at 10000 (skipped action, no billable work), got %d", balance)
	}
	entries, err := store.ListDebitLedger(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("want 0 ledger entries (skipped action not billed), got %d", len(entries))
	}
}

func TestInsufficientBalanceBlocksTool402BeforeExecution(t *testing.T) {
	runner, store := newTestRunner(t)
	ctx := context.Background()

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("X-Payment-Required", `{"price":"0.001","unit":"call","network":"algorand-testnet","recipient":"ALGO123"}`)
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()

	email := fmt.Sprintf("x402-broke-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(ctx, email, "hash")
	if err != nil {
		t.Fatal(err)
	}
	fundUser(t, store, user.ID, 100000) // 10 cents, below the $0.50 fee

	wf, err := store.CreateWorkflow(ctx, "X402 Broke Test", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })

	graph := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			{ID: "x1", Type: models.NodeTypeTool402, Endpoint: srv.URL},
			{ID: "n3", Type: models.NodeTypeEnd},
		},
		Edges: []models.WorkflowEdge{
			{ID: "e1", From: "n1", To: "x1", Kind: models.EdgeKindFlow},
			{ID: "e2", From: "x1", To: "n3", Kind: models.EdgeKindFlow},
		},
	}
	wf, _ = store.UpdateWorkflow(ctx, wf.ID, wf.Name, graph)

	run, err := store.CreateRun(ctx, wf.ID, "test", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	broker := sse.NewBroker()
	broker.Create(run.ID)

	runner.Start(wf, run)
	final := waitForRunDone(t, store, run.ID)
	if final.Status != models.RunStatusFailed {
		t.Fatalf("want failed got %s", final.Status)
	}
	if hits != 0 {
		t.Fatalf("want zero requests to the test server (blocked pre-flight), got %d", hits)
	}
	balance, err := store.GetCreditBalance(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance != 100000 {
		t.Fatalf("want balance unchanged at 100000, got %d", balance)
	}
}

// TestAgentAttachedRelayToolBillsOnInboundSettlementDespiteOutboundFailure is
// a regression test for a real fund-drain vector: without it, an x402
// endpoint could accept the relay's outbound payment (Wallet 2 -> target)
// and then deliberately return a non-2xx response, and the orchestrator
// would never bill the triggering user for it — even though the inbound leg
// (Wallet 1 -> Wallet 2) had already irreversibly settled. Billing must key
// off the relay's X-Inbound-Settled signal, not the final composite HTTP
// status.
// TestAgentAttachedRelayToolBillsOnInboundSettlementDespiteOutboundFailure is
// a regression test for a real fund-drain vector: without it, an x402
// endpoint could accept the platform's outbound payment (Wallet 2 -> target)
// and then deliberately return a non-2xx response, and the orchestrator
// would never bill the triggering user for it -- even though the inbound leg
// (Wallet 1 -> Wallet 2) had already irreversibly settled.
//
// Since Task 5, an agent-attached v2 tool402 node ALWAYS goes through the
// run-level path (reserveAndFundRun's estimate is nonzero the moment a real
// v2 target is attached), so this now exercises executeTool402RunLevel
// directly against target rather than going through the public
// /x402/relay endpoint -- there is no more separate "relay" double for this
// scenario; target IS the endpoint reserveAndFundRun probes AND the one
// executeTool402RunLevel pays. Billing must still happen once the payment
// is actually signed and sent (PayTargetFromWallet2's Signed==true) even
// though the target's own response is a 500, matching the same billing
// philosophy the old X-Inbound-Settled-gated relay path used.
func TestAgentAttachedRelayToolBillsOnInboundSettlementDespiteOutboundFailure(t *testing.T) {
	ctx := context.Background()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment") != "" {
			// Payment already signed and sent (money already left Wallet 2)
			// but the target deliberately errors/rejects the paid request.
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"target rejected the paid request"}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"TARGETADDR","asset":"10458941","maxAmountRequired":"250000"}]}`))
	}))
	defer target.Close()

	inboundTxID := fmt.Sprintf("RUNFUND-OUTBOUND-FAIL-%d", time.Now().UnixNano())
	facilitator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/verify" {
			json.NewEncoder(w).Encode(map[string]any{"isValid": true})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "transaction": inboundTxID})
	}))
	defer facilitator.Close()

	llmCallCount := 0
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmCallCount++
		w.Header().Set("Content-Type", "application/json")
		if llmCallCount == 1 {
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{{
						"id":       "call_1",
						"type":     "function",
						"function": map[string]any{"name": "paid_tool", "arguments": "{}"},
					}},
				}}},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "done"}}},
		})
	}))
	defer llmSrv.Close()

	// relayBaseURL is a dead placeholder here -- the run-level path never
	// touches it (see TestRunLevelPathNeverCallsPublicRelayEndpoint).
	runner, store := newTestRunnerWithRunFunding(t, "http://localhost:65535", facilitator.URL)
	nodes.SetOpenAIBaseURL(llmSrv.URL)
	defer nodes.SetOpenAIBaseURL("https://api.openai.com")

	email := fmt.Sprintf("relay-inbound-settled-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(ctx, email, "hash")
	if err != nil {
		t.Fatal(err)
	}
	// 800000 so that even after reserveAndFundRun's upfront
	// ReserveCredits(250000), the remaining live DB balance (550000) still
	// clears executeFunctionCall's unrelated, pre-existing floor guard
	// (500000) that runs before every attached tool402 call.
	fundUser(t, store, user.ID, 800_000)

	wf, err := store.CreateWorkflow(ctx, "Relay Inbound Settled Test", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })

	graph := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			{ID: "p1", Type: models.NodeTypeProvider, Template: "openai", APIKey: "test-key", Model: "gpt-4o"},
			{ID: "a1", Type: models.NodeTypeAgent},
			{ID: "x1", Type: models.NodeTypeTool402, Name: "paid_tool", Endpoint: target.URL},
			{ID: "n3", Type: models.NodeTypeEnd},
		},
		Edges: []models.WorkflowEdge{
			{ID: "e1", From: "n1", To: "a1", Kind: models.EdgeKindFlow},
			{ID: "e2", From: "a1", To: "n3", Kind: models.EdgeKindFlow},
			{ID: "e3", From: "p1", To: "a1", Kind: models.EdgeKindAttach, ToPort: "model"},
			{ID: "e4", From: "x1", To: "a1", Kind: models.EdgeKindAttach, ToPort: "tools"},
		},
	}
	wf, _ = store.UpdateWorkflow(ctx, wf.ID, wf.Name, graph)

	run, err := store.CreateRun(ctx, wf.ID, "test", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	broker := sse.NewBroker()
	broker.Create(run.ID)

	runner.Start(wf, run)
	final := waitForRunDone(t, store, run.ID)
	if final.Status != models.RunStatusSuccess {
		t.Fatalf("want success got %s", final.Status)
	}

	balance, err := store.GetCreditBalance(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Agent's own $0.01 fee + the real 250000 run-funded cost, billed
	// despite the target's 500 response to the paid request.
	wantBalance := int64(800_000 - 10_000 - 250_000)
	if balance != wantBalance {
		t.Fatalf("want balance %d (billed for the signed outbound payment despite the target's failure), got %d", wantBalance, balance)
	}

	entries, err := store.ListDebitLedger(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sawRelayCost bool
	for _, e := range entries {
		if e.Kind == models.DebitKindX402RelayCost {
			sawRelayCost = true
			if e.AmountUSDMicros != 250_000 {
				t.Fatalf("want relay cost entry of 250000, got %d", e.AmountUSDMicros)
			}
		}
	}
	if !sawRelayCost {
		t.Fatalf("want an %s ledger entry, got %+v", models.DebitKindX402RelayCost, entries)
	}

	fundings, err := store.ListX402RunFundingsByRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fundings) != 1 {
		t.Fatalf("want exactly 1 x402_run_fundings row, got %d", len(fundings))
	}
	settlements, err := store.ListX402RelaySettlementsByRunFunding(ctx, fundings[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(settlements) != 1 {
		t.Fatalf("want exactly 1 x402_relay_settlements row, got %d", len(settlements))
	}
	// status is "failed" (target's own response was a 500, so Settled was
	// false) even though the user was still billed above -- the audit row
	// distinguishes "the target's response" from "whether real money moved",
	// which is the whole point of this test.
	if settlements[0].Status != "failed" {
		t.Fatalf("want settlement status failed (target rejected the paid request), got %q", settlements[0].Status)
	}
	if settlements[0].InboundTxID != nil {
		t.Fatalf("want inbound_tx_id null for a run-funded settlement row, got %v", *settlements[0].InboundTxID)
	}
}

// TestSequentialRelayToolCallsCannotOverspendPastBalance is a regression
// test for the original C2 symptom -- batching every x402 debit until after
// the whole agent turn completed, instead of debiting synchronously as each
// payment settles. It proves the second of two sequential calls to the same
// attached tool never pays a second time.
//
// Since Task 5, an agent-attached v2 tool402 node's cost is reserved ONCE,
// up front, as a run-level in-memory pool sized off a single quote
// (reserveAndFundRun sums per attached NODE, not per anticipated call count)
// -- so a second call to the SAME node exhausts that pool on
// cfg.Ledger.Reserve inside executeTool402RunLevel. That error is a plain
// error, not wrapped in *nodes.ErrBalanceBlocked (unlike the legacy
// per-call path's Reserve failures) -- see executeTool402RunLevel's
// "if err := cfg.Ledger.Reserve(...); err != nil { return
// Tool402PaymentResult{}, err }". A plain (non-ErrBalanceBlocked) tool error
// does not hard-stop the agent's function-calling loop (see
// callOpenAICompat: only errors.As-matching ErrBalanceBlocked returns
// early); instead it's reported back to the LLM as a "error: ..." tool
// result and the loop continues. So this test's fake LLM calls the tool
// exactly twice, then answers "done" -- the run ends in SUCCESS overall
// (the agent gracefully absorbs the second failure), and the invariant that
// actually matters -- at most one real payment ever reaches the target --
// still holds.
func TestSequentialRelayToolCallsCannotOverspendPastBalance(t *testing.T) {
	ctx := context.Background()

	var paidHits int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment") != "" {
			atomic.AddInt32(&paidHits, 1)
			w.Write([]byte(`{"data":"paid tool response"}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"TARGETADDR","asset":"10458941","maxAmountRequired":"250000"}]}`))
	}))
	defer target.Close()

	inboundTxID := fmt.Sprintf("RUNFUND-SEQUENTIAL-%d", time.Now().UnixNano())
	facilitator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/verify" {
			json.NewEncoder(w).Encode(map[string]any{"isValid": true})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "transaction": inboundTxID})
	}))
	defer facilitator.Close()

	// Calls the same paid tool exactly twice (forcing the pool-exhaustion
	// path on the second attempt), then answers "done" on the third turn.
	llmCallCount := 0
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmCallCount++
		w.Header().Set("Content-Type", "application/json")
		if llmCallCount <= 2 {
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{{
						"id":       fmt.Sprintf("call_%d", llmCallCount),
						"type":     "function",
						"function": map[string]any{"name": "paid_tool", "arguments": "{}"},
					}},
				}}},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "done"}}},
		})
	}))
	defer llmSrv.Close()

	runner, store := newTestRunnerWithRunFunding(t, "http://localhost:65535", facilitator.URL)
	nodes.SetOpenAIBaseURL(llmSrv.URL)
	defer nodes.SetOpenAIBaseURL("https://api.openai.com")

	email := fmt.Sprintf("relay-sequential-overspend-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(ctx, email, "hash")
	if err != nil {
		t.Fatal(err)
	}
	// 800000 so that even after reserveAndFundRun's upfront
	// ReserveCredits(250000), the remaining live DB balance (550000) still
	// clears executeFunctionCall's pre-existing floor guard (500000) on
	// both attempts (that guard is unaffected by the run-level pool).
	fundUser(t, store, user.ID, 800_000)

	wf, err := store.CreateWorkflow(ctx, "Relay Sequential Overspend Test", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })

	graph := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			{ID: "p1", Type: models.NodeTypeProvider, Template: "openai", APIKey: "test-key", Model: "gpt-4o"},
			{ID: "a1", Type: models.NodeTypeAgent},
			{ID: "x1", Type: models.NodeTypeTool402, Name: "paid_tool", Endpoint: target.URL},
			{ID: "n3", Type: models.NodeTypeEnd},
		},
		Edges: []models.WorkflowEdge{
			{ID: "e1", From: "n1", To: "a1", Kind: models.EdgeKindFlow},
			{ID: "e2", From: "a1", To: "n3", Kind: models.EdgeKindFlow},
			{ID: "e3", From: "p1", To: "a1", Kind: models.EdgeKindAttach, ToPort: "model"},
			{ID: "e4", From: "x1", To: "a1", Kind: models.EdgeKindAttach, ToPort: "tools"},
		},
	}
	wf, _ = store.UpdateWorkflow(ctx, wf.ID, wf.Name, graph)

	run, err := store.CreateRun(ctx, wf.ID, "test", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	broker := sse.NewBroker()
	broker.Create(run.ID)

	runner.Start(wf, run)
	final := waitForRunDone(t, store, run.ID)
	// The agent's turn completes successfully overall: the second attempt's
	// pool-exhaustion error is reported back to the LLM as a tool error, not
	// a hard stop (see the function doc comment above) -- the fake LLM then
	// answers "done" on its third turn.
	if final.Status != models.RunStatusSuccess {
		t.Fatalf("want success (agent absorbs the second call's pool-exhaustion error and finishes) got %s", final.Status)
	}

	if got := atomic.LoadInt32(&paidHits); got != 1 {
		t.Fatalf("want exactly 1 real payment sent to the target, got %d — the run-level pool failed to block the second attempt before it paid", got)
	}

	balance, err := store.GetCreditBalance(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantBalance := int64(800_000 - 10_000 - 250_000)
	if balance != wantBalance {
		t.Fatalf("want balance %d (exactly one payment + the agent's own fee, no overspend), got %d", wantBalance, balance)
	}

	entries, err := store.ListDebitLedger(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	relayEntries := 0
	for _, e := range entries {
		if e.Kind == models.DebitKindX402RelayCost {
			relayEntries++
		}
	}
	if relayEntries != 1 {
		t.Fatalf("want exactly 1 x402_relay_cost ledger entry, got %d (entries: %+v)", relayEntries, entries)
	}

	fundings, err := store.ListX402RunFundingsByRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fundings) != 1 {
		t.Fatalf("want exactly 1 x402_run_fundings row, got %d", len(fundings))
	}
	settlements, err := store.ListX402RelaySettlementsByRunFunding(ctx, fundings[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(settlements) != 1 {
		t.Fatalf("want exactly 1 x402_relay_settlements row (the second attempt never got past the exhausted pool to reach PayTargetFromWallet2/RecordSettlement), got %d", len(settlements))
	}
}

// TestConcurrentTool402NodesCannotOverspend is a regression test for the
// concurrent half of the TOCTOU C2 fix: two standalone tool402 nodes with no
// edge between them execute in the same topological level, in separate
// goroutines (see runner.go's Run, which launches one goroutine per node per
// level and waits for the whole level before advancing). Without
// ReserveCredits' atomic SELECT...FOR UPDATE, both goroutines could read the
// same pre-decrement balance and both pay -- this proves only one can.
//
// Cost (400000) and starting balance (600000) are deliberately chosen so
// both goroutines' cheap upfront floor preflight (X402PlatformFeeUSDMicros,
// 500000) can plausibly pass before either has reserved anything -- both
// read the same original 600000 balance, since the floor check happens
// before either node's outbound HTTP round trip to the relay, well before
// either Reserve call. That leaves ReserveCredits' atomicity, not the floor
// gate, as what actually has to block the second payment: 600000 covers one
// 400000 reservation but not two.
func TestConcurrentTool402NodesCannotOverspend(t *testing.T) {
	ctx := context.Background()

	var relayPaidHits int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment") != "" {
			atomic.AddInt32(&relayPaidHits, 1)
			w.Header().Set("X-Inbound-Settled", "true")
			w.Write([]byte(`{"data":"paid tool response"}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"PLATFORMADDR","asset":"10458941","maxAmountRequired":"400000"}]}`))
	}))
	defer relay.Close()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"TARGETADDR","asset":"10458941","maxAmountRequired":"400000"}]}`))
	}))
	defer target.Close()

	runner, store := newTestRunnerWithRelay(t, relay.URL)

	email := fmt.Sprintf("relay-concurrent-overspend-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(ctx, email, "hash")
	if err != nil {
		t.Fatal(err)
	}
	// Covers one 400000 relay payment (plus the 500000 floor preflight, read
	// before either reservation happens) but not two concurrent ones.
	fundUser(t, store, user.ID, 600_000)

	wf, err := store.CreateWorkflow(ctx, "Relay Concurrent Overspend Test", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })

	// x1 and x2 are both standalone tool402 nodes fed directly from the
	// trigger with no edge between them -- TopologicalSort places both in
	// the same level, so runner.go's Run executes them concurrently.
	graph := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			{ID: "x1", Type: models.NodeTypeTool402, Endpoint: target.URL},
			{ID: "x2", Type: models.NodeTypeTool402, Endpoint: target.URL},
			{ID: "n3", Type: models.NodeTypeEnd},
		},
		Edges: []models.WorkflowEdge{
			{ID: "e1", From: "n1", To: "x1", Kind: models.EdgeKindFlow},
			{ID: "e2", From: "n1", To: "x2", Kind: models.EdgeKindFlow},
			{ID: "e3", From: "x1", To: "n3", Kind: models.EdgeKindFlow},
			{ID: "e4", From: "x2", To: "n3", Kind: models.EdgeKindFlow},
		},
	}
	wf, _ = store.UpdateWorkflow(ctx, wf.ID, wf.Name, graph)

	run, err := store.CreateRun(ctx, wf.ID, "test", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	broker := sse.NewBroker()
	broker.Create(run.ID)

	runner.Start(wf, run)
	waitForRunDone(t, store, run.ID)
	// One of the two nodes fails (balance exhausted by the other), so the
	// overall run fails -- that's expected and not what this test is
	// checking; the invariant under test is that at most one payment ever
	// actually reaches the relay.

	if got := atomic.LoadInt32(&relayPaidHits); got != 1 {
		t.Fatalf("want exactly 1 real payment sent to the relay despite 2 concurrent nodes racing the same balance, got %d", got)
	}

	balance, err := store.GetCreditBalance(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance != 200_000 {
		t.Fatalf("want balance 200000 (exactly one 400000 payment against a 600000 balance, no overspend), got %d", balance)
	}

	entries, err := store.ListDebitLedger(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	relayEntries := 0
	for _, e := range entries {
		if e.Kind == models.DebitKindX402RelayCost {
			relayEntries++
		}
	}
	if relayEntries != 1 {
		t.Fatalf("want exactly 1 x402_relay_cost ledger entry, got %d (entries: %+v)", relayEntries, entries)
	}
}

// TestAgentBranchingBetweenTwoPricedToolsDoesNotBlockMidRun is the
// failing-then-passing regression test for Task 5's run-level wiring: an
// agent with two attached v2 tool402 targets, priced differently, must be
// able to call both in the same turn off a single up-front run-level
// reservation, without the second call ever being blocked mid-run.
//
// tool_a is deliberately engineered to quote a HIGHER price
// (400000) at estimate time (reserveAndFundRun's first, pre-agent-turn
// probe) than at pay time (350000, every probe after the first) -- real
// price drift between "estimate" and "actual settle", which is exactly what
// executeTool402RunLevel's fresh re-probe-before-paying is there to handle.
// tool_b's price (350000) never drifts. This lets the test also prove the
// leftover pool (the 50000 difference between the 750000 estimate and the
// 700000 actually spent) gets released back to the user's balance once the
// agent's turn ends, instead of being stranded.
//
// The starting balance (1300000) is chosen so that even after
// reserveAndFundRun's upfront ReserveCredits(750000) reservation, the
// user's real DB balance (550000) still comfortably clears
// executeFunctionCall's pre-existing, unrelated cheap conservative floor
// guard (checkBalance against models.X402PlatformFeeUSDMicros, 500000) that
// runs before every attached tool402 call regardless of dialect or
// run-funding -- that guard is untouched by Task 5 and checks the live DB
// balance, not the run-level pool, so it must independently keep passing
// for both calls in the same turn.
func TestAgentBranchingBetweenTwoPricedToolsDoesNotBlockMidRun(t *testing.T) {
	ctx := context.Background()

	var toolAHits int32
	toolA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment") != "" {
			w.Write([]byte(`{"data":"tool a result"}`))
			return
		}
		n := atomic.AddInt32(&toolAHits, 1)
		amount := "350000"
		if n == 1 {
			amount = "400000" // estimate-time quote only; every later probe drifts down
		}
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"TOOLAADDR","asset":"10458941","maxAmountRequired":"` + amount + `"}]}`))
	}))
	defer toolA.Close()

	toolB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment") != "" {
			w.Write([]byte(`{"data":"tool b result"}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"TOOLBADDR","asset":"10458941","maxAmountRequired":"350000"}]}`))
	}))
	defer toolB.Close()

	inboundTxID := fmt.Sprintf("RUNFUND-BRANCH-%d", time.Now().UnixNano())
	facilitator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/verify" {
			json.NewEncoder(w).Encode(map[string]any{"isValid": true})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "transaction": inboundTxID})
	}))
	defer facilitator.Close()

	llmCallCount := 0
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmCallCount++
		w.Header().Set("Content-Type", "application/json")
		if llmCallCount == 1 {
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "call_1", "type": "function", "function": map[string]any{"name": "tool_a", "arguments": "{}"}},
						{"id": "call_2", "type": "function", "function": map[string]any{"name": "tool_b", "arguments": "{}"}},
					},
				}}},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "done"}}},
		})
	}))
	defer llmSrv.Close()

	runner, store := newTestRunnerWithRunFunding(t, "http://localhost:65535", facilitator.URL)
	nodes.SetOpenAIBaseURL(llmSrv.URL)
	defer nodes.SetOpenAIBaseURL("https://api.openai.com")

	email := fmt.Sprintf("run-fund-branch-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(ctx, email, "hash")
	if err != nil {
		t.Fatal(err)
	}
	fundUser(t, store, user.ID, 1_300_000)

	wf, err := store.CreateWorkflow(ctx, "Run Fund Branch Test", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })

	graph := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			{ID: "p1", Type: models.NodeTypeProvider, Template: "openai", APIKey: "test-key", Model: "gpt-4o"},
			{ID: "a1", Type: models.NodeTypeAgent},
			{ID: "toolA", Type: models.NodeTypeTool402, Name: "tool_a", Endpoint: toolA.URL},
			{ID: "toolB", Type: models.NodeTypeTool402, Name: "tool_b", Endpoint: toolB.URL},
			{ID: "n3", Type: models.NodeTypeEnd},
		},
		Edges: []models.WorkflowEdge{
			{ID: "e1", From: "n1", To: "a1", Kind: models.EdgeKindFlow},
			{ID: "e2", From: "a1", To: "n3", Kind: models.EdgeKindFlow},
			{ID: "e3", From: "p1", To: "a1", Kind: models.EdgeKindAttach, ToPort: "model"},
			{ID: "e4", From: "toolA", To: "a1", Kind: models.EdgeKindAttach, ToPort: "tools"},
			{ID: "e5", From: "toolB", To: "a1", Kind: models.EdgeKindAttach, ToPort: "tools"},
		},
	}
	wf, _ = store.UpdateWorkflow(ctx, wf.ID, wf.Name, graph)

	run, err := store.CreateRun(ctx, wf.ID, "test", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	broker := sse.NewBroker()
	broker.Create(run.ID)

	runner.Start(wf, run)
	final := waitForRunDone(t, store, run.ID)
	if final.Status != models.RunStatusSuccess {
		t.Fatalf("want success (neither tool402 call should be blocked mid-run) got %s", final.Status)
	}

	fundings, err := store.ListX402RunFundingsByRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fundings) != 1 {
		t.Fatalf("want exactly 1 x402_run_fundings row for this run, got %d: %+v", len(fundings), fundings)
	}

	settlements, err := store.ListX402RelaySettlementsByRunFunding(ctx, fundings[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(settlements) != 2 {
		t.Fatalf("want exactly 2 x402_relay_settlements rows linked to the run funding, got %d: %+v", len(settlements), settlements)
	}
	for _, s := range settlements {
		if s.InboundTxID != nil {
			t.Fatalf("want inbound_tx_id null for a run-funded settlement row (funded in bulk, not per-call), got %v", *s.InboundTxID)
		}
	}

	balance, err := store.GetCreditBalance(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 1300000 - 750000 (reserved+funded for the run, sum of both estimated
	// prices) - 10000 (agent's own flat fee) + 50000 (unused pool released:
	// estimated 400000+350000=750000, actually paid 350000+350000=700000
	// because tool_a's price drifted down between estimate and settle time).
	wantBalance := int64(1_300_000 - 750_000 - 10_000 + 50_000)
	if balance != wantBalance {
		t.Fatalf("want balance %d, got %d", wantBalance, balance)
	}
}

// TestExactBalanceRunLevelAttachedCallsNotBlockedByFloorGuard is the
// regression test for the gap TestAgentBranchingBetweenTwoPricedToolsDoesNotBlockMidRun
// above deliberately masks: that test funds the user with 1300000 against a
// 750000 estimate, leaving 550000 of live DB balance after
// reserveAndFundRun's upfront ReserveCredits -- comfortably above
// executeFunctionCall's flat 500000 floor guard, so it never actually
// exercises the floor guard's own live-DB-balance check post-reservation.
//
// This test instead funds the user with EXACTLY enough to cover the run:
// the agent's own flat fee (10000) plus the sum of both attached tools'
// real quotes (100000 + 100000 = 200000), for a total of 210000. Once
// reserveAndFundRun reserves the 200000 estimate up front, the live DB
// balance left for the rest of the run is only 10000 -- far below the
// floor guard's 500000 threshold. Before the fix, executeFunctionCall's
// floor guard re-checked this live DB balance before every attached
// tool402 dispatch regardless of RunFundingID, so it would spuriously
// block both attached calls with *nodes.ErrBalanceBlocked even though the
// run-level in-memory pool (sized off the same 200000 estimate) has ample
// headroom for exactly what's left to spend. The fix skips that live-DB
// floor check specifically when RunFundingID != "", since
// reserveAndFundRun's upfront ReserveCredits already satisfied it once.
func TestExactBalanceRunLevelAttachedCallsNotBlockedByFloorGuard(t *testing.T) {
	ctx := context.Background()

	toolA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment") != "" {
			w.Write([]byte(`{"data":"tool a result"}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"TOOLAADDR","asset":"10458941","maxAmountRequired":"100000"}]}`))
	}))
	defer toolA.Close()

	toolB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment") != "" {
			w.Write([]byte(`{"data":"tool b result"}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"TOOLBADDR","asset":"10458941","maxAmountRequired":"100000"}]}`))
	}))
	defer toolB.Close()

	inboundTxID := fmt.Sprintf("RUNFUND-EXACTBAL-%d", time.Now().UnixNano())
	facilitator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/verify" {
			json.NewEncoder(w).Encode(map[string]any{"isValid": true})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "transaction": inboundTxID})
	}))
	defer facilitator.Close()

	llmCallCount := 0
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmCallCount++
		w.Header().Set("Content-Type", "application/json")
		if llmCallCount == 1 {
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "call_1", "type": "function", "function": map[string]any{"name": "tool_a", "arguments": "{}"}},
					},
				}}},
			})
			return
		}
		if llmCallCount == 2 {
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{"id": "call_2", "type": "function", "function": map[string]any{"name": "tool_b", "arguments": "{}"}},
					},
				}}},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "done"}}},
		})
	}))
	defer llmSrv.Close()

	runner, store := newTestRunnerWithRunFunding(t, "http://localhost:65535", facilitator.URL)
	nodes.SetOpenAIBaseURL(llmSrv.URL)
	defer nodes.SetOpenAIBaseURL("https://api.openai.com")

	email := fmt.Sprintf("run-fund-exact-balance-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(ctx, email, "hash")
	if err != nil {
		t.Fatal(err)
	}
	// Exactly enough: 10000 (agent's own flat fee) + 200000 (sum of both
	// tools' real quotes) -- no headroom above the estimate, so the live DB
	// balance after reserveAndFundRun's upfront reservation is only 10000,
	// deliberately below the old floor guard's 500000 threshold.
	fundUser(t, store, user.ID, 210_000)

	wf, err := store.CreateWorkflow(ctx, "Run Fund Exact Balance Test", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })

	graph := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			{ID: "p1", Type: models.NodeTypeProvider, Template: "openai", APIKey: "test-key", Model: "gpt-4o"},
			{ID: "a1", Type: models.NodeTypeAgent},
			{ID: "toolA", Type: models.NodeTypeTool402, Name: "tool_a", Endpoint: toolA.URL},
			{ID: "toolB", Type: models.NodeTypeTool402, Name: "tool_b", Endpoint: toolB.URL},
			{ID: "n3", Type: models.NodeTypeEnd},
		},
		Edges: []models.WorkflowEdge{
			{ID: "e1", From: "n1", To: "a1", Kind: models.EdgeKindFlow},
			{ID: "e2", From: "a1", To: "n3", Kind: models.EdgeKindFlow},
			{ID: "e3", From: "p1", To: "a1", Kind: models.EdgeKindAttach, ToPort: "model"},
			{ID: "e4", From: "toolA", To: "a1", Kind: models.EdgeKindAttach, ToPort: "tools"},
			{ID: "e5", From: "toolB", To: "a1", Kind: models.EdgeKindAttach, ToPort: "tools"},
		},
	}
	wf, _ = store.UpdateWorkflow(ctx, wf.ID, wf.Name, graph)

	run, err := store.CreateRun(ctx, wf.ID, "test", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	broker := sse.NewBroker()
	broker.Create(run.ID)

	runner.Start(wf, run)
	final := waitForRunDone(t, store, run.ID)
	if final.Status != models.RunStatusSuccess {
		t.Fatalf("want success (an exactly-funded run's attached calls must not be spuriously blocked by the floor guard) got %s", final.Status)
	}

	balance, err := store.GetCreditBalance(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantBalance := int64(210_000 - 10_000 - 100_000 - 100_000)
	if balance != wantBalance {
		t.Fatalf("want balance %d, got %d", wantBalance, balance)
	}

	entries, err := store.ListDebitLedger(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	relayEntries := 0
	for _, e := range entries {
		if e.Kind == models.DebitKindX402RelayCost {
			relayEntries++
		}
	}
	if relayEntries != 2 {
		t.Fatalf("want exactly 2 x402_relay_cost ledger entries (both tools billed), got %d (entries: %+v)", relayEntries, entries)
	}
}

// TestAgentBranchingBetweenTwoPricedToolsBlocksUpfrontWhenBalanceInsufficient
// is the second case of the same regression: when the user's balance can't
// cover the sum of both attached tools' real quotes, reserveAndFundRun's
// upfront ReserveCredits must fail before the agent's LLM is ever called --
// neither tool is ever invoked, no facilitator round trip happens, and the
// balance is left completely untouched (ReserveCredits is one atomic
// transaction, not a partial deduction).
func TestAgentBranchingBetweenTwoPricedToolsBlocksUpfrontWhenBalanceInsufficient(t *testing.T) {
	ctx := context.Background()

	var toolAAuthHits, toolBAuthHits int32
	toolA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment") != "" {
			atomic.AddInt32(&toolAAuthHits, 1)
			w.Write([]byte(`{"data":"should never happen"}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"TOOLAADDR","asset":"10458941","maxAmountRequired":"400000"}]}`))
	}))
	defer toolA.Close()

	toolB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment") != "" {
			atomic.AddInt32(&toolBAuthHits, 1)
			w.Write([]byte(`{"data":"should never happen"}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"TOOLBADDR","asset":"10458941","maxAmountRequired":"350000"}]}`))
	}))
	defer toolB.Close()

	var facilitatorHits int32
	facilitator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&facilitatorHits, 1)
		json.NewEncoder(w).Encode(map[string]any{"isValid": true, "success": true})
	}))
	defer facilitator.Close()

	llmCallCount := 0
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmCallCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "should never be reached"}}},
		})
	}))
	defer llmSrv.Close()

	runner, store := newTestRunnerWithRunFunding(t, "http://localhost:65535", facilitator.URL)
	nodes.SetOpenAIBaseURL(llmSrv.URL)
	defer nodes.SetOpenAIBaseURL("https://api.openai.com")

	email := fmt.Sprintf("run-fund-block-upfront-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(ctx, email, "hash")
	if err != nil {
		t.Fatal(err)
	}
	// Covers neither tool's real quote sum (750000), though it would cover
	// either one alone.
	fundUser(t, store, user.ID, 600_000)

	wf, err := store.CreateWorkflow(ctx, "Run Fund Block Upfront Test", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })

	graph := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			{ID: "p1", Type: models.NodeTypeProvider, Template: "openai", APIKey: "test-key", Model: "gpt-4o"},
			{ID: "a1", Type: models.NodeTypeAgent},
			{ID: "toolA", Type: models.NodeTypeTool402, Name: "tool_a", Endpoint: toolA.URL},
			{ID: "toolB", Type: models.NodeTypeTool402, Name: "tool_b", Endpoint: toolB.URL},
			{ID: "n3", Type: models.NodeTypeEnd},
		},
		Edges: []models.WorkflowEdge{
			{ID: "e1", From: "n1", To: "a1", Kind: models.EdgeKindFlow},
			{ID: "e2", From: "a1", To: "n3", Kind: models.EdgeKindFlow},
			{ID: "e3", From: "p1", To: "a1", Kind: models.EdgeKindAttach, ToPort: "model"},
			{ID: "e4", From: "toolA", To: "a1", Kind: models.EdgeKindAttach, ToPort: "tools"},
			{ID: "e5", From: "toolB", To: "a1", Kind: models.EdgeKindAttach, ToPort: "tools"},
		},
	}
	wf, _ = store.UpdateWorkflow(ctx, wf.ID, wf.Name, graph)

	run, err := store.CreateRun(ctx, wf.ID, "test", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	broker := sse.NewBroker()
	broker.Create(run.ID)

	runner.Start(wf, run)
	final := waitForRunDone(t, store, run.ID)
	if final.Status != models.RunStatusFailed {
		t.Fatalf("want failed (upfront reservation for the sum of both tools should fail) got %s", final.Status)
	}

	if llmCallCount != 0 {
		t.Fatalf("want zero LLM calls -- reserveAndFundRun must fail before ExecuteAgent ever runs, got %d", llmCallCount)
	}
	if got := atomic.LoadInt32(&toolAAuthHits); got != 0 {
		t.Fatalf("want zero authenticated requests to tool_a, got %d", got)
	}
	if got := atomic.LoadInt32(&toolBAuthHits); got != 0 {
		t.Fatalf("want zero authenticated requests to tool_b, got %d", got)
	}
	if got := atomic.LoadInt32(&facilitatorHits); got != 0 {
		t.Fatalf("want zero facilitator calls -- FundRunReserve must never be reached, got %d", got)
	}

	balance, err := store.GetCreditBalance(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance != 600_000 {
		t.Fatalf("want balance unchanged at 600000 (ReserveCredits is atomic, fails without partial deduction), got %d", balance)
	}

	fundings, err := store.ListX402RunFundingsByRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fundings) != 0 {
		t.Fatalf("want zero x402_run_fundings rows, got %d", len(fundings))
	}
}

// TestRunLevelPathNeverCallsPublicRelayEndpoint is the direct regression
// test for the bug Task 5 exists to fix: a run-funded tool402 call must
// settle entirely in-process (one bulk inbound settle via
// reserveAndFundRun, then a direct Wallet-2-signed outbound payment via
// executeTool402RunLevel) and must make zero HTTP requests to the public
// /x402/relay endpoint that the legacy per-call path uses.
func TestRunLevelPathNeverCallsPublicRelayEndpoint(t *testing.T) {
	ctx := context.Background()

	var relayHits int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&relayHits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer relay.Close()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Payment") != "" {
			w.Write([]byte(`{"data":"real result"}`))
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"accepts":[{"scheme":"exact","payTo":"TARGETADDR","asset":"10458941","maxAmountRequired":"300000"}]}`))
	}))
	defer target.Close()

	inboundTxID := fmt.Sprintf("RUNFUND-NORELAY-%d", time.Now().UnixNano())
	facilitator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/verify" {
			json.NewEncoder(w).Encode(map[string]any{"isValid": true})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "transaction": inboundTxID})
	}))
	defer facilitator.Close()

	llmCallCount := 0
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmCallCount++
		w.Header().Set("Content-Type", "application/json")
		if llmCallCount == 1 {
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{{
						"id": "call_1", "type": "function",
						"function": map[string]any{"name": "paid_tool", "arguments": "{}"},
					}},
				}}},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "done"}}},
		})
	}))
	defer llmSrv.Close()

	// relayBaseURL points at the instrumented relay server -- proving zero
	// requests reach it is the whole point of this test.
	runner, store := newTestRunnerWithRunFunding(t, relay.URL, facilitator.URL)
	nodes.SetOpenAIBaseURL(llmSrv.URL)
	defer nodes.SetOpenAIBaseURL("https://api.openai.com")

	email := fmt.Sprintf("run-level-no-relay-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(ctx, email, "hash")
	if err != nil {
		t.Fatal(err)
	}
	fundUser(t, store, user.ID, 1_000_000)

	wf, err := store.CreateWorkflow(ctx, "Run Level No Relay Test", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })

	graph := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			{ID: "p1", Type: models.NodeTypeProvider, Template: "openai", APIKey: "test-key", Model: "gpt-4o"},
			{ID: "a1", Type: models.NodeTypeAgent},
			{ID: "x1", Type: models.NodeTypeTool402, Name: "paid_tool", Endpoint: target.URL},
			{ID: "n3", Type: models.NodeTypeEnd},
		},
		Edges: []models.WorkflowEdge{
			{ID: "e1", From: "n1", To: "a1", Kind: models.EdgeKindFlow},
			{ID: "e2", From: "a1", To: "n3", Kind: models.EdgeKindFlow},
			{ID: "e3", From: "p1", To: "a1", Kind: models.EdgeKindAttach, ToPort: "model"},
			{ID: "e4", From: "x1", To: "a1", Kind: models.EdgeKindAttach, ToPort: "tools"},
		},
	}
	wf, _ = store.UpdateWorkflow(ctx, wf.ID, wf.Name, graph)

	run, err := store.CreateRun(ctx, wf.ID, "test", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	broker := sse.NewBroker()
	broker.Create(run.ID)

	runner.Start(wf, run)
	final := waitForRunDone(t, store, run.ID)
	if final.Status != models.RunStatusSuccess {
		t.Fatalf("want success got %s", final.Status)
	}

	if got := atomic.LoadInt32(&relayHits); got != 0 {
		t.Fatalf("want zero requests to the public /x402/relay endpoint from the run-funded path, got %d", got)
	}

	fundings, err := store.ListX402RunFundingsByRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fundings) != 1 {
		t.Fatalf("want exactly 1 x402_run_fundings row, got %d", len(fundings))
	}
}
