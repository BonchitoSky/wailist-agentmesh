package engine_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentmesh/backend/internal/models"
	"github.com/agentmesh/backend/internal/sse"
)

// TestStartResumeRefusesInsteadOfCancellingUnrelatedInFlightRun is a
// regression test for a review finding: StartResume used to call the
// registry's register(), which unconditionally cancels whatever run is
// currently registered for the workflow -- correct for Start (a user
// deliberately re-triggering their own workflow), but wrong here. Resuming
// run R (an old failed/stopped run) says nothing about whether the SAME
// workflow has some OTHER run S currently in flight (a manual trigger, a
// schedule, or another resume). Before this fix, resuming R would silently
// cancel S mid-execution -- possibly mid-payment -- exactly the class of
// bug registerIfAbsent/StartIfNotRunning already closed for the scheduler,
// left open here for resume's own caller.
func TestStartResumeRefusesInsteadOfCancellingUnrelatedInFlightRun(t *testing.T) {
	runner, store := newTestRunner(t)
	ctx := context.Background()

	release := make(chan struct{})
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		<-release
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	wf, err := store.CreateWorkflow(ctx, "Resume No Supersede Test", fundedTestUser(t, store))
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

	// runS: the "currently in flight" run -- started and blocked mid-node.
	runS, err := store.CreateRun(ctx, wf.ID, "manual", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	broker := sse.NewBroker()
	broker.Create(runS.ID)
	runner.Start(wf, runS)

	deadline := time.Now().Add(3 * time.Second)
	for atomic.LoadInt32(&requests) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("server saw %d requests before StartResume, want 1 (runS genuinely in-flight)", got)
	}

	// runR: a completely unrelated OLD failed run of the SAME workflow,
	// being resumed while runS is still executing.
	runR, err := store.CreateRun(ctx, wf.ID, "manual", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, runR.ID, models.RunStatusFailed); err != nil {
		t.Fatal(err)
	}

	claimed, err := runner.StartResume(ctx, wf, runR, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claimed {
		t.Fatal("StartResume should refuse (false) when the workflow already has an unrelated run in flight")
	}

	// runR's own claim must have been reverted -- not left stuck reading
	// "running" forever with nothing executing it.
	revertedRun, err := store.GetRun(ctx, runR.ID)
	if err != nil {
		t.Fatal(err)
	}
	if revertedRun.Status != models.RunStatusFailed {
		t.Fatalf("runR status after refused resume = %s, want failed (claim must be reverted, not left stuck as running)", revertedRun.Status)
	}

	// runS must be completely untouched -- no second request, no cancellation.
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("server saw %d requests after the refused StartResume, want still 1 (runS must not be cancelled)", got)
	}

	close(release)
	final := waitForRunDone(t, store, runS.ID)
	if final.Status != models.RunStatusSuccess {
		t.Fatalf("runS status = %s, want success (it was never cancelled by resuming runR)", final.Status)
	}
}
