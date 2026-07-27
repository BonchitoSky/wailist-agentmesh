package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/agentmesh/backend/internal/db"
	"github.com/agentmesh/backend/internal/models"
	"github.com/agentmesh/backend/internal/sse"
)

// TestPaymentLedgerCommitAndReleaseSurviveCancelledContext is a regression
// test for a phantom-deduction bug: newPaymentLedger's Commit/Release
// closures used to run on the caller's own cctx. Commit and Release are
// compensating actions for money that has already moved (or a reservation
// that must be undone) -- if the triggering request's context was already
// cancelled or past its deadline (e.g. Runner.Stop firing mid-payment, or
// the outbound HTTP call to the relay timing out) by the time either ran,
// the resulting DB call would be a no-op: it neither writes the
// debit_ledger row (Commit) nor restores the reserved balance (Release),
// silently stranding the reservation as a permanent, unledgered credit
// loss. Both must now run on context.WithoutCancel so they complete
// regardless of the caller's context state.
func TestPaymentLedgerCommitAndReleaseSurviveCancelledContext(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	store, err := db.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	email := fmt.Sprintf("ledger-cancelled-ctx-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(context.Background(), email, "hash")
	if err != nil {
		t.Fatal(err)
	}
	orderID := fmt.Sprintf("fund_%s_%d", user.ID, time.Now().UnixNano())
	if _, err := store.CreateCreditTransaction(context.Background(), user.ID, orderID, 100, 1.0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CompleteCreditTransaction(context.Background(), "razorpay", orderID, "pay_"+orderID); err != nil {
		t.Fatal(err)
	}

	wf, err := store.CreateWorkflow(context.Background(), "Ledger Cancelled Ctx Test", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DeleteWorkflow(context.Background(), wf.ID) })
	wf.UserID = user.ID
	run, err := store.CreateRun(context.Background(), wf.ID, "test", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}

	r := NewRunner(store, sse.NewBroker(), nil, "", "", 10458941, "", "", nil, "", "", 0)
	ledger := r.newPaymentLedger(wf, run)

	if err := ledger.Reserve(context.Background(), 250_000); err != nil {
		t.Fatalf("unexpected error reserving: %v", err)
	}
	balance, err := store.GetCreditBalance(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance != 750_000 {
		t.Fatalf("want balance 750000 after reserving 250000 from 1000000, got %d", balance)
	}

	// Simulate the run's context already being cancelled by the time
	// Release runs -- e.g. Runner.Stop fired, or the outbound HTTP call's
	// context deadline was exceeded, mid-payment.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	ledger.Release(cancelledCtx, 250_000)

	balance, err = store.GetCreditBalance(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance != 1_000_000 {
		t.Fatalf("want balance restored to 1000000 after Release, even with an already-cancelled context, got %d -- reservation was stranded", balance)
	}

	// Same for Commit: reserve again, then commit with a cancelled context,
	// and confirm the debit_ledger row actually gets written.
	if err := ledger.Reserve(context.Background(), 250_000); err != nil {
		t.Fatalf("unexpected error reserving: %v", err)
	}
	cancelledCtx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	ledger.Commit(cancelledCtx2, "x1", 250_000, models.DebitKindX402RelayCost)

	entries, err := store.ListDebitLedger(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want exactly 1 debit_ledger row written despite the cancelled context, got %d", len(entries))
	}
	if entries[0].AmountUSDMicros != 250_000 || entries[0].Kind != models.DebitKindX402RelayCost {
		t.Fatalf("want a 250000 x402_relay_cost entry, got %+v", entries[0])
	}
}

// TestRunLevelLedgerExhaustsPoolNotDBBalance verifies that an in-memory
// run-level ledger correctly enforces a fixed credit pool independent of the
// DB balance, and that Release restores the in-memory pool (but not the DB,
// since the DB balance was never touched).
func TestRunLevelLedgerExhaustsPoolNotDBBalance(t *testing.T) {
	wf := models.Workflow{
		ID:     "wf-1",
		UserID: "user-1",
	}
	run := models.Run{
		ID: "run-1",
	}

	// Create a run-level ledger with a fixed pool of 500000 micros.
	ledger, getRemaining := newRunLevelLedger(500000, wf, run, nil)

	ctx := context.Background()

	// Reserve 300000 — should succeed, leaving 200000.
	err := ledger.Reserve(ctx, 300000)
	if err != nil {
		t.Fatalf("first reserve failed: %v", err)
	}
	if remaining := getRemaining(); remaining != 200000 {
		t.Errorf("after first reserve: want 200000 remaining, got %d", remaining)
	}

	// Reserve another 300000 — should fail (only 200000 left).
	// Must assert that the error wraps db.ErrInsufficientCredits.
	err = ledger.Reserve(ctx, 300000)
	if err == nil {
		t.Fatal("second reserve should have failed but didn't")
	}
	if !errors.Is(err, db.ErrInsufficientCredits) {
		t.Errorf("want error wrapping ErrInsufficientCredits, got: %v", err)
	}
	// Pool should not have changed after the failed reserve.
	if remaining := getRemaining(); remaining != 200000 {
		t.Errorf("after failed reserve: want 200000 remaining, got %d", remaining)
	}

	// Reserve 200000 — should succeed, pool now empty.
	err = ledger.Reserve(ctx, 200000)
	if err != nil {
		t.Fatalf("third reserve failed: %v", err)
	}
	if remaining := getRemaining(); remaining != 0 {
		t.Errorf("after third reserve: want 0 remaining, got %d", remaining)
	}

	// Release 100000 back to the pool.
	ledger.Release(ctx, 100000)
	if remaining := getRemaining(); remaining != 100000 {
		t.Errorf("after release: want 100000 remaining, got %d", remaining)
	}

	// Reserve 100000 — should succeed now (exactly fills the released space).
	err = ledger.Reserve(ctx, 100000)
	if err != nil {
		t.Fatalf("reserve after release failed: %v", err)
	}
	if remaining := getRemaining(); remaining != 0 {
		t.Errorf("after reserve after release: want 0 remaining, got %d", remaining)
	}
}
