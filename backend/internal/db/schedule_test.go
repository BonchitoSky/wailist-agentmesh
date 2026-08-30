package db_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/agentmesh/backend/internal/models"
)

// TestClaimDueSchedulesClaimsAndAdvances verifies the core scheduler
// contract: a deployed workflow whose schedule_next_run_at is in the past
// is claimed, and its next_run_at is advanced to whatever the caller's
// nextRun callback returns -- in the SAME call, so a second sweep before
// that new time doesn't re-claim it.
func TestClaimDueSchedulesClaimsAndAdvances(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	email := fmt.Sprintf("schedule-test-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(ctx, email, "hash")
	if err != nil {
		t.Fatal(err)
	}
	wf, err := store.CreateWorkflow(ctx, "Schedule Test WF", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkflowDeployed(ctx, wf.ID, "https://example.com/run", time.Now()); err != nil {
		t.Fatal(err)
	}

	past := time.Now().Add(-time.Minute)
	if err := store.SetWorkflowSchedule(ctx, wf.ID, "0 9 * * *", past); err != nil {
		t.Fatal(err)
	}

	future := time.Now().Add(24 * time.Hour)
	nextRun := func(cronExpr string, after time.Time) (time.Time, error) {
		if cronExpr != "0 9 * * *" {
			t.Errorf("nextRun got cronExpr %q, want the workflow's own expression", cronExpr)
		}
		return future, nil
	}

	due, err := store.ClaimDueSchedules(ctx, time.Now(), nextRun)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].ID != wf.ID {
		t.Fatalf("claimed %d workflows, want exactly [%s]", len(due), wf.ID)
	}

	// A second sweep right away must not re-claim it -- next_run_at was
	// already advanced into the future inside the first call.
	due2, err := store.ClaimDueSchedules(ctx, time.Now(), nextRun)
	if err != nil {
		t.Fatal(err)
	}
	if len(due2) != 0 {
		t.Fatalf("second sweep claimed %d workflows, want 0 (already advanced past now)", len(due2))
	}

	got, err := store.GetWorkflow(ctx, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Postgres timestamptz is microsecond-precision; time.Now() is
	// nanosecond-precision, so an exact Equal would fail on the sub-
	// microsecond remainder lost in the round trip. A millisecond
	// tolerance confirms the right value made it through without being
	// that strict about it.
	if got.ScheduleNextRunAt == nil {
		t.Fatal("schedule_next_run_at is nil, want the advanced time")
	}
	if diff := got.ScheduleNextRunAt.Sub(future); diff > time.Millisecond || diff < -time.Millisecond {
		t.Errorf("schedule_next_run_at = %v, want %v (diff %v)", got.ScheduleNextRunAt, future, diff)
	}
}

// TestClaimDueSchedulesSkipsDraftAndUnscheduledWorkflows verifies the query
// filters correctly: a draft (never deployed) workflow and a deployed
// workflow with no schedule set must never be claimed, even with a due
// schedule_next_run_at set directly for the draft one.
func TestClaimDueSchedulesSkipsDraftAndUnscheduledWorkflows(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	email := fmt.Sprintf("schedule-draft-test-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(ctx, email, "hash")
	if err != nil {
		t.Fatal(err)
	}

	draftWF, err := store.CreateWorkflow(ctx, "Draft WF", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute)
	if err := store.SetWorkflowSchedule(ctx, draftWF.ID, "0 9 * * *", past); err != nil {
		t.Fatal(err)
	}

	unscheduledWF, err := store.CreateWorkflow(ctx, "Unscheduled Deployed WF", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkflowDeployed(ctx, unscheduledWF.ID, "https://example.com/run", time.Now()); err != nil {
		t.Fatal(err)
	}

	nextRun := func(string, time.Time) (time.Time, error) { return time.Now().Add(time.Hour), nil }
	due, err := store.ClaimDueSchedules(ctx, time.Now(), nextRun)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range due {
		if w.ID == draftWF.ID {
			t.Error("a draft (never deployed) workflow must never be claimed")
		}
		if w.ID == unscheduledWF.ID {
			t.Error("a deployed workflow with no schedule must never be claimed")
		}
	}
}

// TestClaimDueSchedulesClearsInvalidExpression verifies a schedule whose
// cron expression no longer parses (nextRun returns an error) is cleared
// rather than left to be re-claimed and fail identically every tick
// forever.
func TestClaimDueSchedulesClearsInvalidExpression(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	email := fmt.Sprintf("schedule-invalid-test-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(ctx, email, "hash")
	if err != nil {
		t.Fatal(err)
	}
	wf, err := store.CreateWorkflow(ctx, "Invalid Schedule WF", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkflowDeployed(ctx, wf.ID, "https://example.com/run", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkflowSchedule(ctx, wf.ID, "not a cron expr", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	failingNextRun := func(string, time.Time) (time.Time, error) {
		return time.Time{}, fmt.Errorf("invalid expression")
	}
	due, err := store.ClaimDueSchedules(ctx, time.Now(), failingNextRun)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("claimed %d workflows for an unparseable schedule, want 0", len(due))
	}

	got, err := store.GetWorkflow(ctx, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ScheduleCron != nil {
		t.Errorf("schedule_cron = %v, want cleared (nil) after an unparseable expression", got.ScheduleCron)
	}
}

// TestListWorkflowsAndUpdateWorkflowReportScheduleFields is a regression
// test for a review finding: ListWorkflows' SELECT and UpdateWorkflow's
// RETURNING clause both omitted schedule_cron/schedule_next_run_at, so
// GET /workflows always reported a null schedule even for a workflow with
// one actively set, and the response immediately after any PUT
// /workflows/{id} did too -- inconsistent with GetWorkflow, which always
// populated them correctly.
func TestListWorkflowsAndUpdateWorkflowReportScheduleFields(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	email := fmt.Sprintf("schedule-list-update-test-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(ctx, email, "hash")
	if err != nil {
		t.Fatal(err)
	}
	wf, err := store.CreateWorkflow(ctx, "Schedule List Update Test", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkflowDeployed(ctx, wf.ID, "https://example.com/run", time.Now()); err != nil {
		t.Fatal(err)
	}
	nextRun := time.Now().Add(24 * time.Hour)
	if err := store.SetWorkflowSchedule(ctx, wf.ID, "0 9 * * *", nextRun); err != nil {
		t.Fatal(err)
	}

	wfs, err := store.ListWorkflows(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, w := range wfs {
		if w.ID != wf.ID {
			continue
		}
		found = true
		if w.ScheduleCron == nil || *w.ScheduleCron != "0 9 * * *" {
			t.Errorf("ListWorkflows: schedule_cron = %v, want \"0 9 * * *\"", w.ScheduleCron)
		}
		if w.ScheduleNextRunAt == nil {
			t.Error("ListWorkflows: schedule_next_run_at is nil, want the set time")
		}
	}
	if !found {
		t.Fatalf("workflow %s not found in ListWorkflows result", wf.ID)
	}

	updated, err := store.UpdateWorkflow(ctx, wf.ID, "Schedule List Update Test (renamed)", models.WorkflowGraph{})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ScheduleCron == nil || *updated.ScheduleCron != "0 9 * * *" {
		t.Errorf("UpdateWorkflow response: schedule_cron = %v, want \"0 9 * * *\" (must survive an unrelated save)", updated.ScheduleCron)
	}
	if updated.ScheduleNextRunAt == nil {
		t.Error("UpdateWorkflow response: schedule_next_run_at is nil, want the set time to survive an unrelated save")
	}
}

// TestFindSystemWorkflowReportsScheduleFields is a regression test for a
// review finding: FindSystemWorkflow's SELECT was left on the pre-existing
// 9-column list when schedule_cron/schedule_next_run_at were added to
// GetWorkflow, ListWorkflows, and UpdateWorkflow's queries -- so a workflow
// found via FindSystemWorkflow/GetOrCreateSystemWorkflow (the Tendril
// console's hidden per-user workflow) always reported a null schedule
// regardless of the row's real DB state.
func TestFindSystemWorkflowReportsScheduleFields(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	email := fmt.Sprintf("schedule-find-system-test-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(ctx, email, "hash")
	if err != nil {
		t.Fatal(err)
	}
	const systemWorkflowName = "Tendril Console"
	wf, err := store.GetOrCreateSystemWorkflow(ctx, user.ID, systemWorkflowName)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkflowDeployed(ctx, wf.ID, "https://example.com/run", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkflowSchedule(ctx, wf.ID, "0 9 * * *", time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	found, ok, err := store.FindSystemWorkflow(ctx, user.ID, systemWorkflowName)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("FindSystemWorkflow: want found=true")
	}
	if found.ScheduleCron == nil || *found.ScheduleCron != "0 9 * * *" {
		t.Errorf("FindSystemWorkflow: schedule_cron = %v, want \"0 9 * * *\"", found.ScheduleCron)
	}
	if found.ScheduleNextRunAt == nil {
		t.Error("FindSystemWorkflow: schedule_next_run_at is nil, want the set time")
	}
}
