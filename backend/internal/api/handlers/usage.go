package handlers

import (
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/agentmesh/backend/internal/respond"
)

// Usage endpoints report real spend out of debit_ledger. Every amount is USD
// micros in the database; responses are plain USD, which is the unit users
// actually top up and are billed in.
//
// The frontend's field names still say "algo" (they predate credits being
// denominated in USD). They are left alone here rather than renamed on both
// sides mid-flight — the values are USD, and the page labels them as such.

func rangeWindow(raw string) (since time.Duration, bucket string) {
	switch raw {
	case "24h":
		return 24 * time.Hour, "hour"
	case "30d":
		return 30 * 24 * time.Hour, "day"
	default: // 7d
		return 7 * 24 * time.Hour, "day"
	}
}

func usd(micros int64) float64 { return float64(micros) / 1e6 }

func (d *Deps) UsageSummary(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(CtxUserID).(string)
	window, _ := rangeWindow(r.URL.Query().Get("range"))
	now := time.Now()
	since := now.Add(-window)

	current, err := d.Store.UsageTotalsSince(r.Context(), userID, since, nil)
	if err != nil {
		log.Printf("usage summary: %v", err)
		respond.Error(w, http.StatusInternalServerError, "internal error")
		return
	}
	// The immediately preceding window of equal length, so the headline
	// percentage compares like with like.
	prevEnd := since
	previous, err := d.Store.UsageTotalsSince(r.Context(), userID, since.Add(-window), &prevEnd)
	if err != nil {
		log.Printf("usage summary (previous window): %v", err)
		respond.Error(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Growth from a zero baseline has no defined percentage — reporting a
	// spike from nothing would be noise, so it reads as 0% change.
	deltaPct := 0.0
	if previous.TotalUSDMicros > 0 {
		deltaPct = (float64(current.TotalUSDMicros-previous.TotalUSDMicros) / float64(previous.TotalUSDMicros)) * 100
	}

	respond.JSON(w, http.StatusOK, map[string]any{
		"totalAlgo":  usd(current.TotalUSDMicros),
		"x402Calls":  current.X402Calls,
		"llmTokens":  0,
		"llmEstAlgo": nil,
		"budget":     nil,
		"deltas":     map[string]any{"totalAlgoPct": deltaPct},
	})
}

func (d *Deps) UsageTimeseries(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(CtxUserID).(string)
	window, bucket := rangeWindow(r.URL.Query().Get("range"))
	// The client sends its own bucket hint; honour it only if it is one of the
	// two we support, so an arbitrary value can never reach date_trunc.
	if b := r.URL.Query().Get("bucket"); b == "hour" || b == "day" {
		bucket = b
	}

	buckets, err := d.Store.UsageTimeseries(r.Context(), userID, time.Now().Add(-window), bucket)
	if err != nil {
		log.Printf("usage timeseries: %v", err)
		respond.Error(w, http.StatusInternalServerError, "internal error")
		return
	}

	layout := "Jan 2"
	if bucket == "hour" {
		layout = "15:04"
	}
	out := make([]map[string]any, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, map[string]any{
			"ts":       b.Bucket.Format(layout),
			"x402Algo": usd(b.X402USDMicros),
			"llmAlgo":  usd(b.LLMUSDMicros),
			"calls":    b.Calls,
		})
	}
	respond.JSON(w, http.StatusOK, out)
}

func (d *Deps) UsageByWorkflow(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(CtxUserID).(string)
	window, _ := rangeWindow(r.URL.Query().Get("range"))

	rows, err := d.Store.UsageByWorkflow(r.Context(), userID, time.Now().Add(-window))
	if err != nil {
		log.Printf("usage by-workflow: %v", err)
		respond.Error(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"workflowId": row.WorkflowID,
			"name":       row.Name,
			"status":     row.Status,
			"algo":       usd(row.USDMicros),
			"calls":      row.Calls,
		})
	}
	respond.JSON(w, http.StatusOK, out)
}

func (d *Deps) UsageByEndpoint(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(CtxUserID).(string)
	window, _ := rangeWindow(r.URL.Query().Get("range"))

	rows, err := d.Store.UsageByEndpoint(r.Context(), userID, time.Now().Add(-window))
	if err != nil {
		log.Printf("usage by-endpoint: %v", err)
		respond.Error(w, http.StatusInternalServerError, "internal error")
		return
	}

	var total int64
	for _, row := range rows {
		total += row.USDMicros
	}

	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		host := row.Endpoint
		if u, err := url.Parse(row.Endpoint); err == nil && u.Host != "" {
			host = u.Host
		}
		kind := "llm"
		if row.IsX402 {
			kind = "x402"
		}
		// A per-call unit price is only meaningful where each call costs the
		// same; token-billed LLM steps have no such figure, so they report
		// null rather than an average masquerading as a price.
		var unitPrice any
		if row.IsX402 && row.Calls > 0 {
			unitPrice = usd(row.USDMicros) / float64(row.Calls)
		}
		pct := 0.0
		if total > 0 {
			pct = float64(row.USDMicros) / float64(total) * 100
		}
		provider := row.Name
		if provider == "" {
			provider = strings.TrimPrefix(host, "www.")
		}
		out = append(out, map[string]any{
			"endpoint":    row.Endpoint,
			"host":        host,
			"provider":    provider,
			"type":        kind,
			"calls":       row.Calls,
			"unitPrice":   unitPrice,
			"unit":        "call",
			"totalAlgo":   usd(row.USDMicros),
			"pctOfSpend":  pct,
			"successRate": nil,
			"lastUsedAt":  row.LastUsedAt.UTC().Format(time.RFC3339),
		})
	}
	respond.JSON(w, http.StatusOK, out)
}

// UsageSettlements currently returns nothing. The on-chain settlement rows
// (x402_relay_settlements) carry no user_id — the relay is a public,
// unauthenticated route, so at insert time there is no user to attribute a
// settlement to. Returning another user's settlements would leak them, and
// fabricating rows would be worse than an empty panel, so this reports
// honestly until that table gains an owner column. Per-run settlement tx ids
// remain visible in the run console, which does know the user.
func (d *Deps) UsageSettlements(w http.ResponseWriter, r *http.Request) {
	if limit := r.URL.Query().Get("limit"); limit != "" {
		if _, err := strconv.Atoi(limit); err != nil {
			respond.Error(w, http.StatusBadRequest, "limit must be a number")
			return
		}
	}
	respond.JSON(w, http.StatusOK, []map[string]any{})
}
