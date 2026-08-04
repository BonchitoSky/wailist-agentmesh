package db_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/agentmesh/backend/internal/db"
)

// setupUserWithCredits creates a user funded to exactly micros AgentMesh
// credits, reusing fundUser (debit_test.go) rather than a second top-up path.
func setupUserWithCredits(t *testing.T, micros int64) (store *db.Store, userID string) {
	t.Helper()
	store = testStore(t)
	ctx := context.Background()

	email := fmt.Sprintf("tendril-credit-test-%d@example.com", time.Now().UnixNano())
	user, err := store.CreateUser(ctx, email, "hash")
	if err != nil {
		t.Fatal(err)
	}
	fundUser(t, store, user.ID, micros)
	return store, user.ID
}

// The topup is a transfer, not a grant: AgentMesh credits go down by exactly
// what Tendril credits go up by, in one transaction.
func TestConvertCreditsToTendrilIsAtomicTransfer(t *testing.T) {
	store, userID := setupUserWithCredits(t, 20_000_000) // $20 AgentMesh credits
	ctx := context.Background()

	newBal, err := store.ConvertCreditsToTendril(ctx, userID, 12_000_000, "TXID1")
	if err != nil {
		t.Fatalf("ConvertCreditsToTendril: %v", err)
	}
	if newBal != 12_000_000 {
		t.Errorf("tendril balance = %d, want 12000000", newBal)
	}
	agentMesh, err := store.GetCreditBalance(ctx, userID)
	if err != nil {
		t.Fatalf("GetCreditBalance: %v", err)
	}
	if agentMesh != 8_000_000 {
		t.Errorf("agentmesh balance = %d, want 8000000", agentMesh)
	}
}

// A user cannot buy Tendril credit they cannot afford, and a failed conversion
// must leave BOTH balances untouched.
func TestConvertCreditsToTendrilRejectsOverdraftAtomically(t *testing.T) {
	store, userID := setupUserWithCredits(t, 5_000_000)
	ctx := context.Background()

	if _, err := store.ConvertCreditsToTendril(ctx, userID, 12_000_000, "TXID1"); err == nil {
		t.Fatal("expected an error converting more than the AgentMesh balance")
	}
	agentMesh, _ := store.GetCreditBalance(ctx, userID)
	tendril, _ := store.TendrilCreditBalance(ctx, userID)
	if agentMesh != 5_000_000 || tendril != 0 {
		t.Errorf("balances moved on a failed conversion: agentmesh=%d tendril=%d", agentMesh, tendril)
	}
}

// This is the property that keeps one user off another user's hours: the check
// is against this user's own row, never against the shared pool.
func TestChargeTendrilCreditCannotOverspendOneUsersBalance(t *testing.T) {
	store, userID := setupUserWithCredits(t, 20_000_000)
	ctx := context.Background()
	if _, err := store.ConvertCreditsToTendril(ctx, userID, 6_000_000, "TXID1"); err != nil {
		t.Fatalf("convert: %v", err)
	}

	if err := store.ChargeTendrilCredit(ctx, userID, "lease1", "charge", 6_000_001); err == nil {
		t.Fatal("expected an error charging more than this user's tendril credit")
	}
	if err := store.ChargeTendrilCredit(ctx, userID, "lease1", "charge", 6_000_000); err != nil {
		t.Fatalf("charging the exact balance should succeed: %v", err)
	}
	bal, _ := store.TendrilCreditBalance(ctx, userID)
	if bal != 0 {
		t.Errorf("balance = %d, want 0", bal)
	}
}

// Releasing early returns the unused reservation as Tendril credit — hours a
// user bought stay theirs rather than evaporating into the pool.
func TestRefundReturnsCreditToTheSameUser(t *testing.T) {
	store, userID := setupUserWithCredits(t, 20_000_000)
	ctx := context.Background()
	if _, err := store.ConvertCreditsToTendril(ctx, userID, 12_000_000, "TXID1"); err != nil {
		t.Fatalf("convert: %v", err)
	}
	if err := store.ChargeTendrilCredit(ctx, userID, "lease1", "charge", 12_000_000); err != nil {
		t.Fatalf("charge: %v", err)
	}
	if err := store.ChargeTendrilCredit(ctx, userID, "lease1", "refund", 9_000_000); err != nil {
		t.Fatalf("refund: %v", err)
	}
	bal, _ := store.TendrilCreditBalance(ctx, userID)
	if bal != 9_000_000 {
		t.Errorf("balance after refund = %d, want 9000000", bal)
	}
	// The refund must not touch AgentMesh credits — the user still holds
	// Tendril hours, they did not get their money back.
	agentMesh, _ := store.GetCreditBalance(ctx, userID)
	if agentMesh != 8_000_000 {
		t.Errorf("agentmesh balance = %d, want 8000000 (untouched by a tendril refund)", agentMesh)
	}
}
