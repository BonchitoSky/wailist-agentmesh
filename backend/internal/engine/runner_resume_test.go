package engine_test

import (
	"context"
	"testing"

	"github.com/agentmesh/backend/internal/models"
	"github.com/agentmesh/backend/internal/sse"
)

// TestResumeSkipsAlreadySucceededNode verifies the core resume contract: a
// node already logged as success is never re-executed. n2's real output
// from a fresh "1+1" calc would be 2 -- seeding a deliberately different
// value (9999) and confirming it survives to n3 (End, which reports the
// most recently set output) proves n2 was skipped rather than recomputed.
// This is the guarantee retry/dead-letter (#87) builds on: a node that
// already paid or debited must not run twice on resume.
func TestResumeSkipsAlreadySucceededNode(t *testing.T) {
	runner, store := newTestRunner(t)
	ctx := context.Background()

	wf, err := store.CreateWorkflow(ctx, "Resume Skip Test", "test-user")
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

	// Seed n2 as already-succeeded from a prior attempt, with an output a
	// real "1+1" calc would never produce -- if Resume re-executes it, the
	// seeded 9999 gets overwritten with 2 and the assertions below fail.
	seedEntry, err := store.InsertRunLog(ctx, models.RunLog{
		RunID:    run.ID,
		NodeID:   "n2",
		NodeType: models.NodeTypeTool,
		Status:   models.LogStatusRunning,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRunLog(ctx, seedEntry.ID, models.LogStatusSuccess, []byte("9999"), 5); err != nil {
		t.Fatal(err)
	}

	// Resume's admission gate (MarkRunRunning) only accepts a run out of
	// "failed"/"stopped" -- CreateRun's own initial status is "running", so
	// a real dead-lettered run must have already transitioned before Resume
	// is ever called on it. Mirror that here rather than relying on the
	// pre-transition "running" default.
	if err := store.FinishRun(ctx, run.ID, models.RunStatusFailed); err != nil {
		t.Fatal(err)
	}

	broker := sse.NewBroker()
	broker.Create(run.ID)

	runner.Resume(ctx, wf, run, 0, false)

	finalRun, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finalRun.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success", finalRun.Status)
	}

	logs, err := store.GetRunLogs(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var n2Count int
	var n3Output any
	for _, l := range logs {
		if l.NodeID == "n2" {
			n2Count++
			if l.Output != float64(9999) {
				t.Errorf("n2 output = %v, want the seeded 9999 (node was re-executed)", l.Output)
			}
		}
		if l.NodeID == "n3" {
			n3Output = l.Output
		}
	}
	if n2Count != 1 {
		t.Fatalf("n2 has %d run_logs rows, want exactly 1 (the seed) -- Resume executed it again", n2Count)
	}
	// End's result is rc.Message(), which stringifies the most recent
	// output -- "9999" here, not the numeric 9999 n2's own row carries.
	if n3Output != "9999" {
		t.Errorf("n3 (End) output = %v, want \"9999\" propagated from the skipped n2", n3Output)
	}
}

// TestGetLatestNodeStatesReturnsMostRecentStatus verifies the resume query
// picks the latest logged row per node, not an arbitrary one -- a node can
// have multiple run_logs rows (e.g. a crashed attempt followed by a manual
// retry), and Resume must only treat it as done if the LATEST row says so.
func TestGetLatestNodeStatesReturnsMostRecentStatus(t *testing.T) {
	_, store := newTestRunner(t)
	ctx := context.Background()

	wf, err := store.CreateWorkflow(ctx, "GetLatestNodeStates Test", "test-user")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })

	graph := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{{ID: "n1", Type: models.NodeTypeTrigger}},
	}
	wf, _ = store.UpdateWorkflow(ctx, wf.ID, wf.Name, graph)

	run, err := store.CreateRun(ctx, wf.ID, "test", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}

	// First attempt: failed.
	first, err := store.InsertRunLog(ctx, models.RunLog{RunID: run.ID, NodeID: "n1", NodeType: models.NodeTypeTrigger, Status: models.LogStatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRunLog(ctx, first.ID, models.LogStatusFailed, []byte(`"boom"`), 1); err != nil {
		t.Fatal(err)
	}

	// Second attempt: succeeded. This is the row GetLatestNodeStates must
	// return, even though the failed row for the same node is older.
	second, err := store.InsertRunLog(ctx, models.RunLog{RunID: run.ID, NodeID: "n1", NodeType: models.NodeTypeTrigger, Status: models.LogStatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRunLog(ctx, second.ID, models.LogStatusSuccess, []byte(`"ok"`), 1); err != nil {
		t.Fatal(err)
	}

	states, err := store.GetLatestNodeStates(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := states["n1"]
	if !ok {
		t.Fatal("n1 missing from GetLatestNodeStates result")
	}
	if got.Status != models.LogStatusSuccess {
		t.Errorf("n1 status = %s, want success (the latest row, not the older failed one)", got.Status)
	}
	if got.Output != "ok" {
		t.Errorf("n1 output = %v, want \"ok\"", got.Output)
	}
}
