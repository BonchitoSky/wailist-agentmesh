package engine_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agentmesh/backend/internal/models"
	"github.com/agentmesh/backend/internal/sse"
)

// TestDeadLetterWrittenAfterRetriesExhausted verifies a node that never
// succeeds gets exactly one dead_letter_runs row once its retry budget is
// spent, with the attempt count covering every executeNode call made
// (1 initial + MaxRetries).
func TestDeadLetterWrittenAfterRetriesExhausted(t *testing.T) {
	runner, store := newTestRunner(t)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	wf, err := store.CreateWorkflow(ctx, "Dead Letter Exhausted Test", fundedTestUser(t, store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })

	graph := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			{ID: "n2", Type: models.NodeTypeTool, Template: "http", URL: srv.URL, Method: "GET", MaxRetries: 2},
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

	runner.Run(ctx, wf, run, 0)

	finalRun, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finalRun.Status != models.RunStatusFailed {
		t.Fatalf("run status = %s, want failed", finalRun.Status)
	}

	dls, err := store.GetDeadLetterRuns(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dls) != 1 {
		t.Fatalf("got %d dead_letter_runs rows, want 1", len(dls))
	}
	if dls[0].NodeID != "n2" {
		t.Errorf("dead letter node = %s, want n2", dls[0].NodeID)
	}
	if dls[0].AttemptCount != 3 {
		t.Errorf("attempt count = %d, want 3 (1 initial + 2 retries)", dls[0].AttemptCount)
	}
	if !strings.Contains(dls[0].Error, "500") {
		t.Errorf("dead letter error = %q, want it to mention the 500 status", dls[0].Error)
	}
}

// TestDeadLetterWrittenImmediatelyForNonRetryableError verifies a
// non-retryable failure (POST + 500) dead-letters after exactly one
// attempt even though the node asked for retries.
func TestDeadLetterWrittenImmediatelyForNonRetryableError(t *testing.T) {
	runner, store := newTestRunner(t)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	wf, err := store.CreateWorkflow(ctx, "Dead Letter Non-Retryable Test", fundedTestUser(t, store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })

	graph := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			{ID: "n2", Type: models.NodeTypeTool, Template: "http", URL: srv.URL, Method: "POST", MaxRetries: 3},
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

	runner.Run(ctx, wf, run, 0)

	dls, err := store.GetDeadLetterRuns(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dls) != 1 {
		t.Fatalf("got %d dead_letter_runs rows, want 1", len(dls))
	}
	if dls[0].AttemptCount != 1 {
		t.Errorf("attempt count = %d, want 1 (non-retryable, no retry attempted)", dls[0].AttemptCount)
	}
}

// TestResumeAfterDeadLetterSkipsSucceededUpstreamNode is the end-to-end
// dead-letter -> resume path: a run fails at n3 with n2 already succeeded,
// then (after the transient cause clears) Resume finishes the run without
// re-executing n2 -- proving dead-letter recovery really does build on
// Runner.Resume rather than restarting from scratch.
func TestResumeAfterDeadLetterSkipsSucceededUpstreamNode(t *testing.T) {
	runner, store := newTestRunner(t)
	ctx := context.Background()

	var failN3 atomic.Bool
	failN3.Store(true)
	var n3Requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n3Requests, 1)
		if failN3.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	wf, err := store.CreateWorkflow(ctx, "Resume After Dead Letter Test", fundedTestUser(t, store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })

	graph := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			{ID: "n2", Type: models.NodeTypeTool, Template: "calc", URL: "1+1"},
			{ID: "n3", Type: models.NodeTypeTool, Template: "http", URL: srv.URL, Method: "GET"},
			{ID: "n4", Type: models.NodeTypeEnd},
		},
		Edges: []models.WorkflowEdge{
			{ID: "e1", From: "n1", To: "n2", Kind: models.EdgeKindFlow},
			{ID: "e2", From: "n2", To: "n3", Kind: models.EdgeKindFlow},
			{ID: "e3", From: "n3", To: "n4", Kind: models.EdgeKindFlow},
		},
	}
	wf, _ = store.UpdateWorkflow(ctx, wf.ID, wf.Name, graph)

	run, err := store.CreateRun(ctx, wf.ID, "test", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	broker := sse.NewBroker()
	broker.Create(run.ID)

	runner.Run(ctx, wf, run, 0)

	failedRun, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failedRun.Status != models.RunStatusFailed {
		t.Fatalf("run status after first attempt = %s, want failed", failedRun.Status)
	}
	if got := atomic.LoadInt32(&n3Requests); got != 1 {
		t.Fatalf("n3 saw %d requests before resume, want 1", got)
	}

	// The transient cause clears; resume should now let n3 succeed without
	// n2 (calc) running again.
	failN3.Store(false)
	broker.Create(run.ID) // Resume calls broker.Close(run.ID) again on exit; needs a live stream to close.
	runner.Resume(ctx, wf, run, 0)

	finalRun, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finalRun.Status != models.RunStatusSuccess {
		t.Fatalf("run status after resume = %s, want success", finalRun.Status)
	}

	logs, err := store.GetRunLogs(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var n2Count int
	for _, l := range logs {
		if l.NodeID == "n2" {
			n2Count++
		}
	}
	if n2Count != 1 {
		t.Errorf("n2 has %d run_logs rows after resume, want exactly 1 (must not re-execute)", n2Count)
	}
	if got := atomic.LoadInt32(&n3Requests); got != 2 {
		t.Errorf("n3 saw %d requests total, want 2 (1 failed + 1 succeeded on resume)", got)
	}
}
