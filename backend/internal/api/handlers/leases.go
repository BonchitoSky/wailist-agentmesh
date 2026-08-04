package handlers

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/agentmesh/backend/internal/engine/nodes"
	"github.com/agentmesh/backend/internal/respond"
	"github.com/agentmesh/backend/internal/wallet"
)

// ListLeases returns the current user's active leases only, never another
// user's and never the encrypted secrets a lease row carries — those stay
// server-side (models.TendrilLease already tags them json:"-").
func (d *Deps) ListLeases(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(CtxUserID).(string)
	leases, err := d.Store.ListActiveTendrilLeases(r.Context(), userID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list leases")
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"leases": leases})
}

// ReleaseLease stops the meter on one of the current user's own leases.
func (d *Deps) ReleaseLease(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(CtxUserID).(string)
	lease, err := d.Store.GetTendrilLease(r.Context(), chi.URLParam(r, "id"))
	if err != nil || lease.UserID != userID {
		respond.Error(w, http.StatusNotFound, "lease not found")
		return
	}
	if d.TendrilClient == nil {
		respond.Error(w, http.StatusServiceUnavailable, "tendril is not configured on this server")
		return
	}
	res, err := nodes.ReleaseLease(r.Context(), nodes.TendrilConfig{
		Client: d.TendrilClient, Store: d.Store, EncryptKey: d.EncryptionKey,
	}, lease)
	if err != nil {
		respond.Error(w, http.StatusBadGateway, "release failed: "+err.Error())
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{
		"usedSeconds": res.UsedSeconds,
		"charged":     res.ChargedAtomic,
		// Deliberately NOT res.Balance: that is the shared pool, which is
		// every user's money and must never be shown to one of them.
	})
}

// DownloadLeaseKey serves the per-lease SSH private key. It must return 404,
// not 403, on a lease that exists but belongs to someone else — a 403 would
// confirm the id exists at all, making lease ids enumerable.
func (d *Deps) DownloadLeaseKey(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(CtxUserID).(string)
	lease, err := d.Store.GetTendrilLease(r.Context(), chi.URLParam(r, "id"))
	if err != nil || lease.UserID != userID {
		respond.Error(w, http.StatusNotFound, "lease not found")
		return
	}
	key, err := wallet.Decrypt(lease.SSHPrivateKeyEnc, d.EncryptionKey)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "key unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", "agentmesh-"+lease.LeaseID))
	w.Write([]byte(key))
}

// TendrilMachines lists the current online market, cheapest first — the same
// data the canvas's Rent-node machine picker reads.
func (d *Deps) TendrilMachines(w http.ResponseWriter, r *http.Request) {
	if d.TendrilClient == nil {
		respond.Error(w, http.StatusServiceUnavailable, "tendril is not configured on this server")
		return
	}
	machines, err := d.TendrilClient.OnlineNodes(r.Context())
	if err != nil {
		respond.Error(w, http.StatusBadGateway, "failed to reach the tendril registry")
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"machines": machines})
}

// TendrilCredits returns only the requesting user's own Tendril credit
// balance and ledger. It must never expose the shared Wallet 2 pool balance:
// that is the sum of every user's money, and showing it to one user both
// leaks aggregate business data and invites them to believe they can spend it.
func (d *Deps) TendrilCredits(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(CtxUserID).(string)
	balance, err := d.Store.TendrilCreditBalance(r.Context(), userID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to read tendril credit balance")
		return
	}
	recent, err := d.Store.RecentTendrilCreditLedger(r.Context(), userID, 20)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to read tendril credit history")
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{
		"tendrilCreditUsdMicros": balance,
		"recent":                 recent,
	})
}
