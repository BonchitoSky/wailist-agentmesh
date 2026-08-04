package nodes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agentmesh/backend/internal/models"
	"github.com/agentmesh/backend/internal/tendril"
	"github.com/agentmesh/backend/internal/wallet"
)

const testEncKey = "0123456789abcdef0123456789abcdef"

// Renting is a flat 1¢ gate fee; hours are bought by holding credit. So "2
// hours on a $6/hr box" costs the user $12.00 of their own Tendril credit,
// plus the 1¢ gate fee for the rent call itself.
func TestRequiredCreditAtomic(t *testing.T) {
	cases := []struct {
		name  string
		rate  int64
		hours float64
		want  int64
	}{
		{"two hours at six dollars", 6_000_000, 2, 12_010_000},
		{"one hour at six dollars", 6_000_000, 1, 6_010_000},
		{"half hour at one fifty", 1_500_000, 0.5, 760_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RequiredCreditAtomic(tc.rate, tc.hours); got != tc.want {
				t.Errorf("RequiredCreditAtomic(%d, %v) = %d, want %d", tc.rate, tc.hours, got, tc.want)
			}
		})
	}
}

// Hours come off a canvas text field, so every rejection here is a rejection
// of real money being spent on a nonsense duration.
func TestParseHours(t *testing.T) {
	ok := map[string]float64{"1": 1, "2": 2, "0.5": 0.5, " 3 ": 3, "": 1}
	for in, want := range ok {
		got, err := parseHours(in)
		if err != nil {
			t.Errorf("parseHours(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseHours(%q) = %v, want %v", in, got, want)
		}
	}
	for _, bad := range []string{"0", "-1", "abc", "1e9", "25"} {
		if _, err := parseHours(bad); err == nil {
			t.Errorf("parseHours(%q) should have errored", bad)
		}
	}
}

// Release is the only place compute is actually billed, so it must persist
// what Tendril reported rather than what AgentMesh predicted.
func TestReleaseLeasePersistsTendrilsOwnCharge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/x402/leases/lease_9k2m" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer plain-token" {
			t.Errorf("auth = %q, want the decrypted lease token", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"usedSeconds":1800,"chargedAtomic":3000000,"balance":9000000}`))
	}))
	defer srv.Close()

	enc, err := wallet.Encrypt("plain-token", testEncKey)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	store := &fakeTendrilStore{}
	res, err := ReleaseLease(context.Background(), TendrilConfig{
		Client: tendril.NewClient(srv.URL), Store: store, EncryptKey: testEncKey,
	}, models.TendrilLease{ID: "row1", LeaseID: "lease_9k2m", LeaseTokenEnc: enc})
	if err != nil {
		t.Fatalf("ReleaseLease: %v", err)
	}
	if res.ChargedAtomic != 3_000_000 || res.UsedSeconds != 1800 {
		t.Errorf("result = %+v", res)
	}
	if store.releasedID != "row1" || store.releasedCharged != 3_000_000 || store.releasedSeconds != 1800 {
		t.Errorf("store got id=%q seconds=%d charged=%d",
			store.releasedID, store.releasedSeconds, store.releasedCharged)
	}
}

// Releasing twice must be harmless — the reaper and a user clicking Release can
// race, and a double DELETE against Tendril would surface as a run failure.
func TestReleaseLeaseIsIdempotentOnAlreadyReleased(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"lease not found"}`))
	}))
	defer srv.Close()

	enc, _ := wallet.Encrypt("plain-token", testEncKey)
	store := &fakeTendrilStore{}
	if _, err := ReleaseLease(context.Background(), TendrilConfig{
		Client: tendril.NewClient(srv.URL), Store: store, EncryptKey: testEncKey,
	}, models.TendrilLease{ID: "row1", LeaseID: "gone", LeaseTokenEnc: enc}); err != nil {
		t.Fatalf("ReleaseLease on a missing lease should not error: %v", err)
	}
	if store.releasedID != "row1" {
		t.Error("a lease Tendril no longer knows about must still be marked released locally")
	}
}

// Releasing early must hand the unused reservation back as TENDRIL credit —
// the hours stay the user's. Refunding to AgentMesh credit instead would let a
// user cycle rent/release to convert Tendril credit into general platform
// credit the pool cannot honour.
func TestReleaseRefundsUnusedReservationAsTendrilCredit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Reserved 2h at $6 (+1c gate) = 12.01; actually used 30 min = $3.00.
		w.Write([]byte(`{"usedSeconds":1800,"chargedAtomic":3000000,"balance":0}`))
	}))
	defer srv.Close()

	enc, _ := wallet.Encrypt("plain-token", testEncKey)
	store := &fakeTendrilStore{}
	if _, err := ReleaseLease(context.Background(), TendrilConfig{
		Client: tendril.NewClient(srv.URL), Store: store, EncryptKey: testEncKey,
	}, models.TendrilLease{
		ID: "row1", UserID: "user1", LeaseID: "lease_9k2m", LeaseTokenEnc: enc,
		ReservedUSDMicros: 12_010_000,
	}); err != nil {
		t.Fatalf("ReleaseLease: %v", err)
	}
	if store.refunded != 9_010_000 {
		t.Errorf("refunded = %d, want 9010000", store.refunded)
	}
}

type fakeTendrilStore struct {
	tendrilCredit   int64
	agentMeshCredit int64
	refunded        int64
	releasedID      string
	releasedSeconds int64
	releasedCharged int64
	inserted        models.TendrilLease
	byID            map[string]models.TendrilLease
}

func (f *fakeTendrilStore) InsertTendrilLease(_ context.Context, l models.TendrilLease) (models.TendrilLease, error) {
	l.ID = "row1"
	f.inserted = l
	return l, nil
}
func (f *fakeTendrilStore) GetTendrilLease(_ context.Context, id string) (models.TendrilLease, error) {
	return f.byID[id], nil
}
func (f *fakeTendrilStore) MarkTendrilLeaseReleased(_ context.Context, id string, used, charged int64) error {
	f.releasedID, f.releasedSeconds, f.releasedCharged = id, used, charged
	return nil
}
func (f *fakeTendrilStore) LatestActiveLeaseForRun(_ context.Context, _ string) (models.TendrilLease, error) {
	return models.TendrilLease{}, nil
}
func (f *fakeTendrilStore) TendrilCreditBalance(_ context.Context, _ string) (int64, error) {
	return f.tendrilCredit, nil
}
func (f *fakeTendrilStore) CreditBalance(_ context.Context, _ string) (int64, error) {
	return f.agentMeshCredit, nil
}
func (f *fakeTendrilStore) ConvertCreditsToTendril(_ context.Context, _ string, amount int64, _ string) (int64, error) {
	f.agentMeshCredit -= amount
	f.tendrilCredit += amount
	return f.tendrilCredit, nil
}
func (f *fakeTendrilStore) ChargeTendrilCredit(_ context.Context, _, leaseID, kind string, amount int64) error {
	if kind == "refund" {
		f.tendrilCredit += amount
		f.refunded += amount
		return nil
	}
	f.tendrilCredit -= amount
	return nil
}
