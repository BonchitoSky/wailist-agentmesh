package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/agentmesh/backend/internal/engine/nodes"
	"github.com/agentmesh/backend/internal/models"
	"github.com/agentmesh/backend/internal/respond"
)

// tendrilConsoleWorkflowName backs the Tendril console's direct-action
// endpoints (buy credit / rent / run) with one real, hidden workflow per
// user — there's no canvas UI for it, but the engine's node executors and
// the FK constraints on rows like tendril_leases (workflow_id, run_id) both
// need one to exist. GetOrCreateSystemWorkflow finds-or-creates it lazily on
// first use.
const tendrilConsoleWorkflowName = "Tendril Console (managed — do not edit)"

// TendrilConsoleWorkflow returns (creating on first call) the one hidden
// workflow that backs this user's Tendril console — so "Load Tendril
// workflow" in the workflow list opens the SAME row every time (never a
// fresh duplicate) and the canvas route can recognize it by id and render
// the console UI in place of the graph editor.
func (d *Deps) TendrilConsoleWorkflow(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(CtxUserID).(string)
	wf, err := d.Store.GetOrCreateSystemWorkflow(r.Context(), userID, tendrilConsoleWorkflowName)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to open the tendril console")
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"workflowId": wf.ID})
}

// consoleRunContext satisfies nodes.RunContexter for the console's
// synthesized action nodes, whose input is always request-body fields, never
// a run's free-text trigger message — Message/UserInput only ever answer
// with what the caller explicitly passed in (the "run" action's payload).
type consoleRunContext struct{ message string }

func (c consoleRunContext) Message() string             { return c.message }
func (c consoleRunContext) UserInput() string           { return c.message }
func (c consoleRunContext) ToolOutputs() map[string]any { return nil }
func (c consoleRunContext) Set(string, any)             {}
func (c consoleRunContext) Get(string) (any, bool)      { return nil, false }

// runTendrilAction executes exactly one Tendril node, bypassing the graph
// engine (TopologicalSort/Run) entirely — the console has no trigger, no
// chain, no "run the whole workflow": each button press is one direct call.
// A fresh Run row is created every call so LatestActiveLeaseForRun stays
// meaningful for the rent step that opens a lease; run/release steps that
// don't share that run_id fall back to LatestActiveLeaseForUser (see
// resolveLease in engine/nodes/tendril.go).
func (d *Deps) runTendrilAction(ctx context.Context, userID string, node models.WorkflowNode, message string) (any, error) {
	wf, err := d.Store.GetOrCreateSystemWorkflow(ctx, userID, tendrilConsoleWorkflowName)
	if err != nil {
		return nil, err
	}
	run, err := d.Store.CreateRun(ctx, wf.ID, "tendril-console", []byte("{}"))
	if err != nil {
		return nil, err
	}
	cfg := nodes.TendrilConfig{
		Client:     d.TendrilClient,
		Session:    d.TendrilSession,
		Store:      d.Store,
		EncryptKey: d.EncryptionKey,
		UserID:     userID,
		WorkflowID: wf.ID,
		RunID:      run.ID,
		Relay: nodes.X402RelayConfig{
			USDCSigner:               d.USDCSigner,
			PlatformSpendEncMnemonic: d.PlatformSpendWalletEncMnemonic,
			ExpectedAssetID:          d.USDCAssetID,
			RelayBaseURL:             d.RelayBaseURL,
		},
	}
	result, err := nodes.ExecuteTendril(ctx, node, consoleRunContext{message: message}, cfg)
	status := models.RunStatusSuccess
	if err != nil {
		status = models.RunStatusFailed
	}
	d.Store.FinishRun(ctx, run.ID, status)
	return result, err
}

// TendrilConsoleTopup buys Tendril credit directly — no workflow chain, no
// re-buying on every job you run.
func (d *Deps) TendrilConsoleTopup(w http.ResponseWriter, r *http.Request) {
	if d.TendrilClient == nil {
		respond.Error(w, http.StatusServiceUnavailable, "tendril is not configured on this server")
		return
	}
	userID, _ := r.Context().Value(CtxUserID).(string)
	var body struct {
		AmountUsd string `json:"amountUsd"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	node := models.WorkflowNode{ID: "tendril-console-topup", Type: models.NodeTypeTendril,
		TendrilAction: "topup", TendrilAmount: body.AmountUsd}
	result, err := d.runTendrilAction(r.Context(), userID, node, "")
	if err != nil {
		respond.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	respond.JSON(w, http.StatusOK, result)
}

// TendrilConsoleRent opens a metered lease directly on a machine the caller
// picked from GET /tendril/machines.
func (d *Deps) TendrilConsoleRent(w http.ResponseWriter, r *http.Request) {
	if d.TendrilClient == nil {
		respond.Error(w, http.StatusServiceUnavailable, "tendril is not configured on this server")
		return
	}
	userID, _ := r.Context().Value(CtxUserID).(string)
	var body struct {
		NodeID string `json:"nodeId"`
		Hours  string `json:"hours"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	node := models.WorkflowNode{ID: "tendril-console-rent", Type: models.NodeTypeTendril,
		TendrilAction: "rent", TendrilNodeID: body.NodeID, TendrilHours: body.Hours}
	result, err := d.runTendrilAction(r.Context(), userID, node, "")
	if err != nil {
		respond.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	respond.JSON(w, http.StatusOK, result)
}

// TendrilConsoleRun executes a job on whichever machine this user currently
// has open (resolveLease's LatestActiveLeaseForUser fallback — the console
// has no single "run" that a Rent step shares a run_id with).
func (d *Deps) TendrilConsoleRun(w http.ResponseWriter, r *http.Request) {
	if d.TendrilClient == nil {
		respond.Error(w, http.StatusServiceUnavailable, "tendril is not configured on this server")
		return
	}
	userID, _ := r.Context().Value(CtxUserID).(string)
	var body struct {
		Payload string `json:"payload"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	node := models.WorkflowNode{ID: "tendril-console-run", Type: models.NodeTypeTendril,
		TendrilAction: "run"}
	result, err := d.runTendrilAction(r.Context(), userID, node, body.Payload)
	if err != nil {
		respond.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	respond.JSON(w, http.StatusOK, result)
}
