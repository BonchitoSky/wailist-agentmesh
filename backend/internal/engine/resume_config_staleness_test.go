package engine

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/agentmesh/backend/internal/db"
	"github.com/agentmesh/backend/internal/models"
	"github.com/agentmesh/backend/internal/sse"
)

// newConfigStalenessTestRunner is a package-local twin of engine_test's
// newTestRunner (unreachable from here, a white-box test file) -- same
// setup, just usable from package engine so nodeConfigHash/
// nodeMayHaveRealSideEffect (both unexported) can be called directly.
func newConfigStalenessTestRunner(t *testing.T) (*Runner, *db.Store) {
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
	broker := sse.NewBroker()
	return NewRunner(store, broker, &fakeUSDCSignerForLedgerTest{}, "http://localhost:8080", "", "", X402Config{USDCAssetID: 10458941}), store
}

// TestResumeReExecutesPureComputeNodeWhenConfigChanged is a regression test
// for a review finding: Resume used to skip ANY already-succeeded node
// unconditionally, even one whose configuration had since been edited --
// silently re-feeding downstream steps its OLD, now-stale output instead of
// recomputing it under the new config. For a node type with no real side
// effect (Tool "calc" here), editing it between a dead-letter and a Resume
// must make it re-execute with the new config, not replay the old result.
func TestResumeReExecutesPureComputeNodeWhenConfigChanged(t *testing.T) {
	runner, store := newConfigStalenessTestRunner(t)
	ctx := context.Background()

	wf, err := store.CreateWorkflow(ctx, "Config Staleness Recompute Test", fmt.Sprintf("user-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })

	n2Old := models.WorkflowNode{ID: "n2", Type: models.NodeTypeTool, Template: "calc", URL: "1+1"}
	graphOld := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			n2Old,
			{ID: "n3", Type: models.NodeTypeEnd},
		},
		Edges: []models.WorkflowEdge{
			{ID: "e1", From: "n1", To: "n2", Kind: models.EdgeKindFlow},
			{ID: "e2", From: "n2", To: "n3", Kind: models.EdgeKindFlow},
		},
	}
	wf, _ = store.UpdateWorkflow(ctx, wf.ID, wf.Name, graphOld)

	run, err := store.CreateRun(ctx, wf.ID, "test", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}

	// Seed n2 as already succeeded under the OLD config ("1+1" -> 2), with
	// its real config hash from that attempt -- exactly what a genuine
	// prior success would have recorded.
	seedEntry, err := store.InsertRunLog(ctx, models.RunLog{RunID: run.ID, NodeID: "n2", NodeType: models.NodeTypeTool, Status: models.LogStatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRunLog(ctx, seedEntry.ID, models.LogStatusSuccess, []byte("2"), 1, nodeConfigHash(n2Old)); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, run.ID, models.RunStatusFailed); err != nil {
		t.Fatal(err)
	}

	// The user edits n2's expression before resuming.
	n2New := models.WorkflowNode{ID: "n2", Type: models.NodeTypeTool, Template: "calc", URL: "2+2"}
	graphNew := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			n2New,
			{ID: "n3", Type: models.NodeTypeEnd},
		},
		Edges: graphOld.Edges,
	}
	wf, err = store.UpdateWorkflow(ctx, wf.ID, wf.Name, graphNew)
	if err != nil {
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
	var n2LatestOutput any
	for _, l := range logs {
		if l.NodeID == "n2" {
			n2Count++
			n2LatestOutput = l.Output
		}
	}
	if n2Count != 2 {
		t.Fatalf("n2 has %d run_logs rows, want exactly 2 (the stale seed + a real re-execution under the new config) -- config change was not detected", n2Count)
	}
	// calc's real result for "2+2" is 4 -- confirms n2 was actually
	// recomputed under the NEW config, not merely re-logged with the old
	// (2) value.
	if fmt.Sprint(n2LatestOutput) != "4" {
		t.Fatalf("n2's latest output = %v, want 4 (the real result of the edited \"2+2\" expression)", n2LatestOutput)
	}
}

// TestResumeSkipsSideEffectNodeEvenWhenConfigChanged is the other half of
// the same fix: a node type that can move real money or trigger a real
// external effect (an Action/connector node here) must keep being skipped
// unconditionally on resume, even if its config changed since its prior
// success -- re-executing it under new config could otherwise repeat a
// real send under different settings. A user who wants an edited
// payment-adjacent node to actually take effect needs a fresh Run.
func TestResumeSkipsSideEffectNodeEvenWhenConfigChanged(t *testing.T) {
	runner, store := newConfigStalenessTestRunner(t)
	ctx := context.Background()

	wf, err := store.CreateWorkflow(ctx, "Config Staleness SideEffect Test", fmt.Sprintf("user-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })

	n2Old := models.WorkflowNode{ID: "n2", Type: models.NodeTypeAction, Template: "webhook", URL: "https://old.example.com/hook"}
	if !nodeMayHaveRealSideEffect(n2Old.Type, n2Old.Template) {
		t.Fatal("test fixture assumption broken: NodeTypeAction must count as a real-side-effect type")
	}
	graphOld := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			n2Old,
			{ID: "n3", Type: models.NodeTypeEnd},
		},
		Edges: []models.WorkflowEdge{
			{ID: "e1", From: "n1", To: "n2", Kind: models.EdgeKindFlow},
			{ID: "e2", From: "n2", To: "n3", Kind: models.EdgeKindFlow},
		},
	}
	wf, _ = store.UpdateWorkflow(ctx, wf.ID, wf.Name, graphOld)

	run, err := store.CreateRun(ctx, wf.ID, "test", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}

	seedEntry, err := store.InsertRunLog(ctx, models.RunLog{RunID: run.ID, NodeID: "n2", NodeType: models.NodeTypeAction, Status: models.LogStatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	seedOutput := []byte(`{"status":200}`)
	if err := store.UpdateRunLog(ctx, seedEntry.ID, models.LogStatusSuccess, seedOutput, 1, nodeConfigHash(n2Old)); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, run.ID, models.RunStatusFailed); err != nil {
		t.Fatal(err)
	}

	// Edit n2's webhook URL -- if this were re-executed, it would try to
	// hit a URL that doesn't exist in this test (and, in a real deploy,
	// could send a duplicate notification to a DIFFERENT endpoint).
	n2New := models.WorkflowNode{ID: "n2", Type: models.NodeTypeAction, Template: "webhook", URL: "https://new.example.com/hook"}
	graphNew := models.WorkflowGraph{
		Nodes: []models.WorkflowNode{
			{ID: "n1", Type: models.NodeTypeTrigger},
			n2New,
			{ID: "n3", Type: models.NodeTypeEnd},
		},
		Edges: graphOld.Edges,
	}
	wf, err = store.UpdateWorkflow(ctx, wf.ID, wf.Name, graphNew)
	if err != nil {
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
	for _, l := range logs {
		if l.NodeID == "n2" {
			n2Count++
		}
	}
	if n2Count != 1 {
		t.Fatalf("n2 has %d run_logs rows, want exactly 1 (the seed) -- a side-effect node must never re-execute on resume, config change or not", n2Count)
	}
}
