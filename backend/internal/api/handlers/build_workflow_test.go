package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agentmesh/backend/internal/api/handlers"
	"github.com/agentmesh/backend/internal/engine/nodes"
)

// buildWorkflowReq issues POST /workflows/{id}/build as userID.
func buildWorkflowReq(d *handlers.Deps, workflowID, userID, message string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]string{"message": message})
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/workflows/"+workflowID+"/build", bytes.NewReader(body)), "id", workflowID)
	d.BuildWorkflow(rec, withUser(req, userID))
	return rec
}

func TestBuildWorkflowAddsNode(t *testing.T) {
	d := testDeps(t)
	d.PlatformGeminiAPIKey = "test-key"
	ctx := context.Background()

	user, err := d.Store.CreateUser(ctx, "wf-build-"+randSuffix(t)+"@example.com", "hash")
	if err != nil {
		t.Fatal(err)
	}
	wf, err := d.Store.CreateWorkflow(ctx, "Build Me", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Store.DeleteWorkflow(context.Background(), wf.ID) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{
				{"content": map[string]any{"parts": []map[string]any{
					{"text": "Added a chat trigger node."},
				}}},
			},
		})
	}))
	defer srv.Close()
	nodes.SetGeminiBaseURL(srv.URL)
	defer nodes.SetGeminiBaseURL("https://generativelanguage.googleapis.com")

	rec := buildWorkflowReq(d, wf.ID, user.ID, "add a chat trigger")
	if rec.Code != http.StatusOK {
		t.Fatalf("build got %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Reply    string `json:"reply"`
		Workflow struct {
			Nodes []map[string]any `json:"nodes"`
		} `json:"workflow"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Reply != "Added a chat trigger node." {
		t.Fatalf("unexpected reply: %q", out.Reply)
	}
}

func TestBuildWorkflowRequiresPlatformKey(t *testing.T) {
	d := testDeps(t)
	ctx := context.Background()

	user, err := d.Store.CreateUser(ctx, "wf-build-nokey-"+randSuffix(t)+"@example.com", "hash")
	if err != nil {
		t.Fatal(err)
	}
	wf, err := d.Store.CreateWorkflow(ctx, "No Key", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Store.DeleteWorkflow(context.Background(), wf.ID) })

	rec := buildWorkflowReq(d, wf.ID, user.ID, "add a trigger")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestBuildWorkflowOtherUserGets404(t *testing.T) {
	d := testDeps(t)
	d.PlatformGeminiAPIKey = "test-key"
	ctx := context.Background()

	owner, err := d.Store.CreateUser(ctx, "wf-build-owner-"+randSuffix(t)+"@example.com", "hash")
	if err != nil {
		t.Fatal(err)
	}
	other, err := d.Store.CreateUser(ctx, "wf-build-other-"+randSuffix(t)+"@example.com", "hash")
	if err != nil {
		t.Fatal(err)
	}
	wf, err := d.Store.CreateWorkflow(ctx, "Not Yours", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Store.DeleteWorkflow(context.Background(), wf.ID) })

	rec := buildWorkflowReq(d, wf.ID, other.ID, "add a trigger")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("other user got %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
}
