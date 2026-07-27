# Run-Level x402 Reservation & Bulk Wallet-2 Funding

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace per-x402-call inbound settlement (Wallet 1 → Wallet 2) with a single per-agent-run inbound settlement, sized to a genuine, real-quote-based estimate of that run's attached x402 tools — so an agent can freely branch between differently-priced tool402 calls without a mid-loop balance block killing the run. The per-call outbound leg (Wallet 2 → real target) still happens once per real call, unchanged in risk profile — it's still one real GoPlausible-attributable payment per call, protocol-mandated and leaderboard-favorable.

**Why:** Today `newPaymentLedger` reserves credits atomically per individual x402 call inside an agent's tool-calling loop (`internal/engine/runner.go:92-118`, consumed by `tool402.go`'s relay path). If a user's balance covers call 1 but not call 2, call 2 gets blocked mid-loop — the run ends up partial. Reserving once per run, sized honestly to what the run could plausibly need, removes that failure mode without reintroducing overspend risk. The sizing discipline (sum of real, freshly-fetched quotes for the agent's own attached tool402 nodes — never a padded ceiling) is what keeps this legitimately custodial rather than wash-settling between two wallets we control: every dollar pre-funded into Wallet 2 must be backed by a real reservation against a real user's real credit balance.

**Not in scope:** standalone (non-agent-attached) `NodeTypeTool402` nodes — those execute once per topology level, not via dynamic LLM looping, so the mid-loop-block problem doesn't apply; their existing per-call reserve/commit/release, and their existing path through the public `/x402/relay` endpoint, stay completely untouched. The legacy flat-quote dialect is also untouched.

**A design point settled during review, stated up front so it isn't relitigated per-task:** it is not enough to just swap the credit ledger from per-call to per-run while leaving every downstream call routed through the *existing* public `/x402/relay?target=...` endpoint — that endpoint always runs a complete inbound-settle-then-outbound-pay cycle per call (`relaySettleAndForward`). Doing that *in addition to* one bulk pre-fund means Wallet 1 pays twice for the same spend. An earlier draft of this plan tried to fix that with an HTTP header + shared-secret bypass on the public relay endpoint. **That approach is rejected below, not adopted.** A shared secret gating a public, unauthenticated route is a real regression from today's security posture, where every dollar Wallet 2 pays out is capped by a real GoPlausible-verified payment matching a real challenge amount — a leaked token would have no server-side spending ceiling at all. The design in this plan instead keeps the public relay endpoint **completely unmodified**, and gives `engine` its own **direct, in-process** path to the same underlying "sign and pay from Wallet 2" logic — no new HTTP route, no token, no header, nothing newly reachable from outside the process. See Task 3.

## Architecture

```
Today (per call, inside the agent's tool-calling loop):
  agent decides to call tool402 node → ReserveCredits(exact amount) →
    HTTP round-trip to our own public /x402/relay:
      inbound settle (Wallet1→Wallet2, real quote, via facilitator)
      → outbound pay (Wallet2→target, same quote)
  → CommitReservedDebit or ReleaseReservedCredits
  (repeat per call — a call whose amount exceeds what's left of the balance blocks here)

This plan (once per agent-node run, entirely in-process — no new public surface):
  agent node starts
    → probe each attached tool402 node for its real current quote (v2 only; legacy dialect skipped)
    → estimate = sum of real quotes
    → ReserveCredits(estimate)                    [DB, existing method, called once]
    → FundRunReserve(estimate)                    [Wallet1→Wallet2, one real facilitator Verify+Settle, in-process]
    → RecordRunFunding(runID, txID, estimate)      [one audit row]
    → runLedger = in-memory pool seeded with `estimate`
  agent's tool-calling loop runs, 0..N real tool402 calls, each:
    → runLedger.Reserve(amount)                   [in-memory decrement — no DB call, no chain call, no HTTP call]
    → PayTargetFromWallet2(target, quote)          [direct Go call — signs + pays target, unchanged signer]
    → RecordSettlement(target, settled)            [DB audit row, closure into r.store]
    → runLedger.Commit(...) or runLedger.Release(...)
  agent node ends
    → ReleaseReservedCredits(estimate - totalCommitted)  [DB, existing method, called once]

Public /x402/relay endpoint: byte-for-byte unchanged. Still the only way any
external, third-party x402 client reaches this system — still does its own
full inbound-settle-then-outbound-pay per call, exactly as it does today,
for standalone tool402 nodes and for any real external caller.
```

## Global Constraints

- The pre-fund amount is **never** padded above the sum of real, freshly-fetched quotes for the agent's attached tool402 nodes. A configurable safety multiplier may exist for future tuning but must default to `1.0` (no padding).
- **No new public HTTP surface, no shared secret, no token.** Everything this plan adds beyond `FundRunReserve`'s facilitator calls (which talk to GoPlausible's own public API, not ours) is a plain in-process Go function call within the existing backend process. The public `/x402/relay` route's externally-observable behavior is unmodified — verified by the existing `x402relay_test.go` suite passing unchanged (Task 3).
- **Exactly one function can sign a payment from Wallet 2's mnemonic:** `nodes.PayTargetFromWallet2` (Task 3). Both the (unmodified) public relay handler and the new in-process run-level path call this same function — there is no second, independently-maintained copy of the sign-and-pay logic to drift out of sync (e.g. forgetting the `MaxRelayOutboundUSDMicros` cap in one copy but not the other).
- **Wallet 2's encrypted mnemonic gains exactly one additional holder: `engine.Runner`.** It's threaded from the same already-in-memory value `main.go` already computes for `handlers.Deps` today — no new secret, no new place secrets are read from, no new external exposure. This is a widening of which trusted, same-process package holds an existing in-memory value, not a new attack surface.
- `internal/x402` is a leaf package with no `github.com/agentmesh/...` imports of its own — safe for `internal/engine`/`internal/engine/nodes` to import directly (verified: only `internal/api/handlers` imports it today; no cycle risk). `internal/api/handlers` already imports `internal/engine/nodes` directly (`tools.go`, `x402relay.go`) — so moving shared logic *into* `nodes` and having `handlers` call it is an already-established dependency direction, not a new one.
- `db.Store.ReserveCredits`, `CommitReservedDebit`, `ReleaseReservedCredits`, `RecordOutboundSettlement` are reused as-is. `CommitReservedDebit` only ever wrote the audit row, never touched the balance, so calling it N times per run without N matching DB reservations is already correct by design.
- The in-memory run-level pool is process-local per agent-node execution — no new shared/concurrent state across runs.
- `go build ./... && go vet ./... && go test ./...` must stay green after every task (real Postgres `TEST_DATABASE_URL` required for ledger/schema tests).
- Land this as its own branch/PR after `worktree-x402-orchestrator-relay` (PR #39) merges.

**Verified against current facts (2026-07-27):** `payTo` is the leaderboard's attribution key; the challenge rules contain no explicit statement for or against aggregating settlements (silent, not prohibited); mainnet-only counts toward the leaderboard, testnet is validation-only (unchanged); GoPlausible's discovery catalog indexes resources by URL separately from merchant totals which key off `payTo` — `FundRunReserve`'s `resource` field points at a real, static, reachable route on our own domain (Task 2) rather than an opaque identifier, so it's unambiguous either way.

---

### Task 1: Factor the v2-probe/quote-fetch logic into a shared helper

**Files:**
- Modify: `internal/engine/nodes/tool402.go`, `internal/engine/runner.go`, `internal/engine/nodes/provider.go`
- Test: `internal/engine/nodes/tool402_test.go`

**Why:** `ExecuteTool402V2` already probes an endpoint (GET, check for `402` + `accepts[]`) to decide v2-relay vs. legacy-dialect. The run-level estimator (Task 5) needs the same probe's price; the new run-level per-call executor (Task 5) additionally needs `payTo`/`asset` from the same probe. One shared helper, not two diverging copies.

**Also bundles `ExecuteTool402V2`'s params, verified against the real current signature — do this now, not in Task 5:** the current function is already `ExecuteTool402V2(ctx, node, rc, aw, signer, usdcSigner, platformSpendEncMnemonic, expectedAssetID, relayBaseURL, ledger)` — 10 positional params, 3 of them `string`/`uint64`/`string` in a row. Task 5 needs to add 3 more (`RunFundingID`, `Wallet2`, `RecordSettlement`), which would push this to 13 and make an accidental argument-swap easy to introduce and impossible for the compiler to catch. Fold the five relay-specific params (`usdcSigner, platformSpendEncMnemonic, expectedAssetID, relayBaseURL, ledger` — already exactly `X402RelayConfig`'s existing fields, verified against `runner.go`) into that one struct now, while this function's body is already being touched for the probe extraction, so Task 5 only ever *adds fields to an existing struct* rather than *adding more positional params to an already-long call*.

- [ ] **Step 1: Extract `probeTool402Endpoint`, returning the full quote**

```go
// x402Quote is what a real v2 challenge's accepts[0] entry carries that
// callers in this package need — payTo/asset for actually paying it,
// MaxAmountRequired (parsed to USD micros) for gating/estimating.
type x402Quote struct {
	PayTo             string
	Asset             string
	MaxAmountRequired int64 // USD micros
}

// probeTool402Endpoint fetches endpoint's 402 challenge (if any) and reports
// whether it speaks real x402 v2 (accepts[] present) along with its current
// quote. notPaymentRequired=true means the endpoint answered something
// other than 402 (caller treats that as "no payment needed", exactly like
// ExecuteTool402V2 does today).
func probeTool402Endpoint(ctx context.Context, endpoint string) (isV2 bool, notPaymentRequired bool, rawResponse any, quote x402Quote, err error) {
	if err := urlValidator(endpoint); err != nil {
		return false, false, nil, x402Quote{}, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return false, false, nil, x402Quote{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPaymentRequired {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, httpResponseLimit))
		var result any
		if json.Unmarshal(b, &result) == nil {
			return false, true, result, x402Quote{}, nil
		}
		return false, true, string(b), x402Quote{}, nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, httpResponseLimit))
	var v2Challenge struct {
		Accepts []map[string]any `json:"accepts"`
	}
	if json.Unmarshal(body, &v2Challenge) != nil || len(v2Challenge.Accepts) == 0 {
		return false, false, nil, x402Quote{}, nil
	}
	accept := v2Challenge.Accepts[0]
	payTo, _ := accept["payTo"].(string)
	asset, _ := accept["asset"].(string)
	amountStr, _ := accept["maxAmountRequired"].(string)
	amount, _ := strconv.ParseInt(amountStr, 10, 64)
	return true, false, nil, x402Quote{PayTo: payTo, Asset: asset, MaxAmountRequired: amount}, nil
}

// ProbeX402Price is the exported form the run-level estimator (Task 5) uses
// when it only needs "is this v2, and what's the current price" — not the
// full quote.
func ProbeX402Price(ctx context.Context, endpoint string) (isV2 bool, amountUSDMicros int64, err error) {
	isV2, _, _, quote, err := probeTool402Endpoint(ctx, endpoint)
	return isV2, quote.MaxAmountRequired, err
}

// TargetQuote is a real target's parsed x402 v2 quote — payTo/asset for
// actually paying it (PayTargetFromWallet2, Task 3), MaxAmountRequired kept
// as the original string since that's the wire format PayTargetFromWallet2
// and the facilitator both expect. Defined here (not in Task 3) so Task 1
// compiles standalone, in task order, before Task 3 exists.
type TargetQuote struct {
	PayTo             string
	Asset             string
	MaxAmountRequired string
}

// ProbeX402Quote is the exported form the run-level per-call executor
// (Task 5) uses when it needs the full quote (payTo/asset too) to actually
// pay it via PayTargetFromWallet2 (Task 3).
func ProbeX402Quote(ctx context.Context, endpoint string) (isV2 bool, quote TargetQuote, err error) {
	isV2, _, _, q, err := probeTool402Endpoint(ctx, endpoint)
	return isV2, TargetQuote{PayTo: q.PayTo, Asset: q.Asset, MaxAmountRequired: strconv.FormatInt(q.MaxAmountRequired, 10)}, err
}
```

- [ ] **Step 2: Rewrite `ExecuteTool402V2` to call `probeTool402Endpoint`, and take `X402RelayConfig` as one param**

New signature: `func ExecuteTool402V2(ctx context.Context, node models.WorkflowNode, rc RunContexter, aw models.AgentWallet, signer WalletSigner, relayCfg X402RelayConfig) (Tool402PaymentResult, error)`. Inside, replace every use of the old flat `usdcSigner`/`platformSpendEncMnemonic`/`expectedAssetID`/`relayBaseURL`/`ledger` params with `relayCfg.USDCSigner`/`relayCfg.PlatformSpendEncMnemonic`/`relayCfg.ExpectedAssetID`/`relayCfg.RelayBaseURL`/`relayCfg.Ledger` — same values, same behavior, just reached through the struct. Replace the initial GET+parse block with a call to `probeTool402Endpoint`, preserving identical behavior: `notPaymentRequired` → return `Tool402PaymentResult{Response: rawResponse}, nil` immediately (unchanged); `!isV2` → fall through to the existing legacy-dialect branch, now reading `relayCfg.Ledger` (unchanged behavior, just the new field path); `isV2` → call `executeTool402V2Relay(ctx, node, relayCfg.USDCSigner, relayCfg.PlatformSpendEncMnemonic, relayCfg.ExpectedAssetID, relayCfg.RelayBaseURL, relayCfg.Ledger)` (unchanged) — the probed quote is discarded here since `executeTool402V2Relay` re-fetches/re-verifies its own quote via the relay itself, unchanged.

- [ ] **Step 3: Update every existing call site**

- `internal/engine/runner.go`'s standalone `NodeTypeTool402` case: currently builds the 5 flat args inline and calls `nodes.ExecuteTool402V2(ctx, node, rc, aw, r.walletSvc, usdcSigner, r.platformSpendEncMnemonic, r.usdcAssetID, r.relayBaseURL, ledger)` — replace with building one `nodes.X402RelayConfig{USDCSigner: usdcSigner, PlatformSpendEncMnemonic: r.platformSpendEncMnemonic, ExpectedAssetID: r.usdcAssetID, RelayBaseURL: r.relayBaseURL, Ledger: ledger}` and calling `nodes.ExecuteTool402V2(ctx, node, rc, aw, r.walletSvc, relayCfg)`.
- `internal/engine/nodes/provider.go`'s `executeFunctionCall` — it already holds a `relayCfg X402RelayConfig` value (built by its caller, `ExecuteAgent`) and today must be unpacking it into flat args to call `ExecuteTool402V2`; change that call site to pass `relayCfg` straight through instead.
- `internal/engine/nodes/tool402_test.go` — every existing `ExecuteTool402V2(...)` call site (there are several, per the prior branch) needs its trailing 5 flat args replaced with one `X402RelayConfig{...}` literal carrying the same values. Read the current test file first — don't guess the exact existing arg list from this plan's memory of it.

- [ ] **Step 4: Run the existing x402 tests**

Run: `go test ./internal/engine/nodes/... -run TestX402 -v` and `go test ./internal/engine/... -v` (covers `runner.go`'s call site)
Expected: all existing tests still PASS unchanged in *behavior* — the call sites' argument lists change shape (flat → struct), but no test's assertions or fixtures should need to change. If a test's expected values need to change, this refactor introduced a behavior difference — stop and find it.

- [ ] **Step 5: Commit**

```bash
git add internal/engine/nodes/tool402.go internal/engine/nodes/tool402_test.go internal/engine/runner.go internal/engine/nodes/provider.go
git commit -m "tool402: factor endpoint probing into a shared helper; bundle ExecuteTool402V2's params into X402RelayConfig"
```

---

### Task 2: `FundRunReserve` — single lump-sum inbound settlement

**Files:**
- Create: `internal/engine/nodes/runfund.go`
- Modify: `internal/api/router.go`, `internal/api/handlers/x402relay.go` (tiny static route)
- Test: `internal/engine/nodes/runfund_test.go`

**Interfaces:**
- Consumes: `x402.FacilitatorClient` (`internal/x402/facilitator.go`), `USDCGroupSigner`.
- Produces: `RunPreFundConfig`, `FundRunReserve(ctx, cfg, runID, amountUSDMicros) (txID string, err error)`.

- [ ] **Step 1: Add the config type**

```go
package nodes

import (
	"context"
	"fmt"
	"strconv"

	"github.com/agentmesh/backend/internal/x402"
)

// RunPreFundConfig carries what's needed to settle a single lump-sum inbound
// x402 payment (Wallet 1 -> Wallet 2) before an agent node's tool-calling
// loop starts. Distinct from Wallet2PayConfig (Task 3), which drives the
// per-call OUTBOUND leg — this only ever settles the INBOUND leg, once per
// run.
type RunPreFundConfig struct {
	USDCSigner               USDCGroupSigner
	PlatformSpendEncMnemonic string
	Facilitator              *x402.FacilitatorClient
	PlatformWalletAddress    string
	RelayNetwork             string
	RelayFeePayer            string
	ExpectedAssetID          uint64
	PublicBaseURL            string // for Resource below — a real, reachable URL, not an opaque string
}
```

- [ ] **Step 2: Add `FundRunReserve`**

```go
// FundRunReserve settles one real GoPlausible payment for amountUSDMicros
// from the platform's Wallet 1 spend wallet into Wallet 2
// (cfg.PlatformWalletAddress) — same payTo as every per-call relay
// settlement, so leaderboard attribution (keyed on payTo, not resource) is
// unaffected. Resource points at a real, static, reachable route on our own
// domain (Step 3 below) rather than an opaque identifier string.
// amountUSDMicros <= 0 is a no-op (an agent with no attached tool402 nodes,
// or all-legacy-dialect ones, needs no pre-fund at all).
func FundRunReserve(ctx context.Context, cfg RunPreFundConfig, runID string, amountUSDMicros int64) (string, error) {
	if amountUSDMicros <= 0 {
		return "", nil
	}

	reqs := x402.PaymentRequirements{
		Scheme:            "exact",
		Network:           cfg.RelayNetwork,
		MaxAmountRequired: strconv.FormatInt(amountUSDMicros, 10),
		Resource:          cfg.PublicBaseURL + "/x402/relay/run-funding",
		Description:       "AgentMesh workflow run funding — pre-settled pool for this run's downstream x402 tool calls",
		PayTo:             cfg.PlatformWalletAddress,
		MaxTimeoutSeconds: 300,
		Asset:             strconv.FormatUint(cfg.ExpectedAssetID, 10),
		Extra: map[string]any{
			"asset":    strconv.FormatUint(cfg.ExpectedAssetID, 10),
			"feePayer": cfg.RelayFeePayer,
			"tag":      "x402-global-challenge",
			"decimals": 6,
		},
	}

	group, idx, err := cfg.USDCSigner.SignUSDCPaymentGroup(ctx, cfg.PlatformSpendEncMnemonic, cfg.PlatformWalletAddress, cfg.ExpectedAssetID, uint64(amountUSDMicros), cfg.RelayFeePayer)
	if err != nil {
		return "", fmt.Errorf("run pre-fund: sign failed: %w", err)
	}
	payload := x402.PaymentPayload{
		X402Version: 2,
		Scheme:      "exact",
		Network:     cfg.RelayNetwork,
		Payload:     x402.PaymentGroup{PaymentGroup: group, PaymentIndex: idx},
	}

	verifyResult, err := cfg.Facilitator.Verify(ctx, payload, reqs)
	if err != nil {
		return "", fmt.Errorf("run pre-fund: facilitator verify failed: %w", err)
	}
	if !verifyResult.IsValid {
		return "", fmt.Errorf("run pre-fund: payment invalid: %s", verifyResult.Invalid)
	}

	settleResult, err := cfg.Facilitator.Settle(ctx, payload, reqs)
	if err != nil {
		return "", fmt.Errorf("run pre-fund: facilitator settle failed: %w", err)
	}
	if !settleResult.Success {
		return "", fmt.Errorf("run pre-fund: settlement failed: %s", settleResult.Error)
	}
	return settleResult.TxID, nil
}
```

- [ ] **Step 3: Add the static resource route**

`router.go`: `r.Get("/x402/relay/run-funding", d.X402RunFundingInfo)`. Handler in `x402relay.go` returns a static JSON body, no payment logic, no auth needed (purely informational, matches what a real Bazaar-catalog crawler would expect to find at a `resource` URL):

```go
func (d *Deps) X402RunFundingInfo(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, http.StatusOK, map[string]string{
		"description": "AgentMesh workflow run funding pool — internal pre-settlement for downstream x402 tool calls, not directly payable via this route",
	})
}
```

- [ ] **Step 4: Test with a fake facilitator**

`internal/engine/nodes/runfund_test.go`, `httptest.NewServer` standing in for the facilitator (same pattern as `tool402_test.go`'s relay tests): (a) `amountUSDMicros <= 0` returns `("", nil)` without calling the signer or facilitator; (b) successful verify+settle returns the settle result's `TxID`; (c) the fake facilitator sees `PayTo == cfg.PlatformWalletAddress` and `Extra.tag == "x402-global-challenge"`; (d) `verifyResult.IsValid == false` or `settleResult.Success == false` surfaces as an error, no txid.

Run: `go test ./internal/engine/nodes/... -run TestFundRunReserve -v`

- [ ] **Step 5: Commit**

```bash
git add internal/engine/nodes/runfund.go internal/engine/nodes/runfund_test.go internal/api/router.go internal/api/handlers/x402relay.go
git commit -m "nodes: add FundRunReserve for a single per-run inbound settlement"
```

---

### Task 3: Shared Wallet-2 payout function — no new public surface, no token

**Files:**
- Create: `internal/engine/nodes/walletpay.go`
- Modify: `internal/api/handlers/x402relay.go` (`payTargetAndRespond` becomes a thin wrapper)
- Create: `internal/db/migrations/000010_x402_run_fundings.up.sql`, `.down.sql`
- Modify: `internal/db/store.go`, `internal/models/types.go` (add `X402RunFunding`; change `X402RelaySettlement.InboundTxID` from `string` to `*string`)
- Test: `internal/engine/nodes/walletpay_test.go`, `internal/api/handlers/x402relay_test.go` (must pass **unchanged**), `internal/db/*_test.go`

**Why this task exists:** this is the fix for the double-settle bug — closing it by giving `engine` a **direct, in-process** way to sign and pay from Wallet 2, instead of routing every per-call payment back through the public HTTP relay (which would re-run its own inbound settle every time). The extraction also fixes the *duplication* risk of writing a second, independent copy of "sign and pay from Wallet 2" logic that could quietly drift out of sync with the original (e.g. forgetting the `MaxRelayOutboundUSDMicros` cap check in one copy).

- [ ] **Step 1: Extract the core sign-and-pay logic into `nodes.PayTargetFromWallet2`**

```go
package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// Wallet2PayConfig carries what's needed to sign and send a real outbound
// x402 payment from the platform's Wallet 2 settlement wallet to a real
// target. Used by BOTH the public relay handler (x402relay.go's
// payTargetAndRespond, for genuine external x402 clients) and the run-level
// in-process path (Task 5) — there is exactly one function that signs from
// Wallet 2's mnemonic; both callers hold their own copy of the same
// already-in-memory secret (threaded from main.go) and reach the same code.
type Wallet2PayConfig struct {
	USDCSigner                USDCGroupSigner
	PlatformWalletEncMnemonic string
	USDCAssetID               uint64
	RelayFeePayer             string
	RelayNetwork              string
	MaxRelayOutboundUSDMicros int64
}

// TargetQuote is defined in Task 1 (walletpay.go uses it, doesn't own it).

// Wallet2PayError carries the exact HTTP status the original inline
// handler used per failure kind, so payTargetAndRespond's refactored
// wrapper can reproduce identical response codes.
type Wallet2PayError struct {
	StatusCode int
	Msg        string
}

func (e *Wallet2PayError) Error() string { return e.Msg }

// Wallet2PayResult mirrors what payTargetAndRespond needs to reconstruct
// its existing HTTP response exactly. Signed becomes true the instant
// SignUSDCPaymentGroup succeeds — the exact moment the public handler sets
// X-Inbound-Settled today — and stays true regardless of what happens next
// (a target-request failure still counts as "money already committed",
// matching the existing billing philosophy documented in x402relay.go).
type Wallet2PayResult struct {
	Signed       bool
	StatusCode   int
	ResponseBody []byte
	Settled      bool // true only when target's own response was 2xx
}

func PayTargetFromWallet2(ctx context.Context, cfg Wallet2PayConfig, target string, quote TargetQuote) (Wallet2PayResult, error) {
	assetID, _ := strconv.ParseUint(quote.Asset, 10, 64)
	amount, _ := strconv.ParseUint(quote.MaxAmountRequired, 10, 64)

	if assetID != cfg.USDCAssetID {
		return Wallet2PayResult{}, &Wallet2PayError{StatusCode: http.StatusBadGateway, Msg: "target quoted an unexpected asset id"}
	}
	if cfg.MaxRelayOutboundUSDMicros > 0 && amount > uint64(cfg.MaxRelayOutboundUSDMicros) {
		return Wallet2PayResult{}, &Wallet2PayError{StatusCode: http.StatusBadGateway, Msg: "target quoted an amount exceeding the relay's per-call cap"}
	}

	group, idx, err := cfg.USDCSigner.SignUSDCPaymentGroup(ctx, cfg.PlatformWalletEncMnemonic, quote.PayTo, assetID, amount, cfg.RelayFeePayer)
	if err != nil {
		return Wallet2PayResult{}, &Wallet2PayError{StatusCode: http.StatusInternalServerError, Msg: "failed to sign outbound payment: " + err.Error()}
	}

	xPaymentOut, _ := json.Marshal(map[string]any{
		"x402Version": 2, "scheme": "exact", "network": cfg.RelayNetwork,
		"payload": map[string]any{"paymentGroup": group, "paymentIndex": idx},
	})
	payReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	payReq.Header.Set("X-Payment", string(xPaymentOut))
	payResp, err := SafeHTTPClient().Do(payReq)
	if err != nil {
		// Signed==true is deliberate here: a real signed group already
		// exists by this point, matching the pre-existing billing
		// philosophy in x402relay.go — a network failure reaching the
		// target doesn't unwind a payment that's already committed.
		return Wallet2PayResult{Signed: true}, &Wallet2PayError{StatusCode: http.StatusBadGateway, Msg: "paid request to target failed: " + err.Error()}
	}
	defer payResp.Body.Close()
	finalBody, _ := io.ReadAll(io.LimitReader(payResp.Body, 5<<20))

	return Wallet2PayResult{
		Signed:       true,
		StatusCode:   payResp.StatusCode,
		ResponseBody: finalBody,
		Settled:      payResp.StatusCode >= 200 && payResp.StatusCode < 300,
	}, nil
}
```

(Confirm `SafeHTTPClient()` is the exported name of whatever `nodes.SafeHTTPClient()` call `x402relay.go` already uses at this call site today — reuse it, don't introduce a second HTTP client with different SSRF-dialer/timeout behavior.)

- [ ] **Step 2: Make `payTargetAndRespond` a thin wrapper — must be behavior-preserving**

Replace its body with:

```go
func (d *Deps) payTargetAndRespond(w http.ResponseWriter, r *http.Request, target, ledgerID string, quote targetPriceQuote) {
	ctx := r.Context()
	cfg := nodes.Wallet2PayConfig{
		USDCSigner:                d.USDCSigner,
		PlatformWalletEncMnemonic: d.PlatformWalletEncMnemonic,
		USDCAssetID:               d.USDCAssetID,
		RelayFeePayer:             d.RelayFeePayer,
		RelayNetwork:              d.RelayNetwork,
		MaxRelayOutboundUSDMicros: d.MaxRelayOutboundUSDMicros,
	}
	result, err := nodes.PayTargetFromWallet2(ctx, cfg, target, nodes.TargetQuote{
		PayTo: quote.PayTo, Asset: quote.Asset, MaxAmountRequired: quote.MaxAmountRequired,
	})
	if result.Signed {
		w.Header().Set("X-Inbound-Settled", "true")
	}
	if err != nil {
		d.Store.RecordOutboundSettlement(ctx, ledgerID, "", "failed")
		status := http.StatusBadGateway
		var payErr *nodes.Wallet2PayError
		if errors.As(err, &payErr) {
			status = payErr.StatusCode
		}
		respond.Error(w, status, err.Error())
		return
	}
	status := "failed"
	if result.Settled {
		status = "settled"
	}
	d.Store.RecordOutboundSettlement(ctx, ledgerID, "", status)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(result.StatusCode)
	w.Write(result.ResponseBody)
}
```

**This step's acceptance test is the existing test suite, unchanged.** Run `go test ./internal/api/handlers/... -run TestX402Relay -v` before and after this edit and diff the output — every existing test (asset-mismatch rejection, cap rejection, sign-failure, target-failure, success, the `X-Inbound-Settled` timing tests) must pass with zero modifications to the test file itself. If any test needs to change to pass, the refactor introduced a behavior difference — stop and find it before proceeding, don't adjust the test to match.

- [ ] **Step 3: Minimal audit schema — `x402_run_fundings`**

`000010_x402_run_fundings.up.sql`:
```sql
CREATE TABLE x402_run_fundings (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id               TEXT NOT NULL,
    inbound_tx_id        TEXT NOT NULL UNIQUE,
    amount_asset_micros  BIGINT NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE x402_relay_settlements
    ALTER COLUMN inbound_tx_id DROP NOT NULL,
    ADD COLUMN run_funding_id UUID REFERENCES x402_run_fundings(id);

-- A row funded by a run-level bulk settlement has no per-call inbound tx of
-- its own (inbound_tx_id NULL, run_funding_id set); a normal per-call row
-- (via the unmodified public relay) keeps requiring inbound_tx_id. This is
-- an AUDIT record, not an enforcement mechanism — nothing about this
-- constraint gates spending; the actual spending gate is that
-- PayTargetFromWallet2 is only reachable from the public relay handler
-- (unchanged) or from engine's own trusted, in-memory-pool-gated code —
-- never from an externally reachable, unauthenticated path.
ALTER TABLE x402_relay_settlements
    ADD CONSTRAINT x402_relay_settlements_funding_check
    CHECK (inbound_tx_id IS NOT NULL OR run_funding_id IS NOT NULL);
```

`.down.sql`: drop the check constraint, drop `run_funding_id`, restore `inbound_tx_id NOT NULL` (fails if any run-funded rows exist with a null `inbound_tx_id` — correct, matches the existing `000009` down-migration's documented pattern for a production audit ledger).

- [ ] **Step 4: Model type + store methods**

```go
// in internal/models/types.go — mirrors x402_run_fundings' columns exactly.
type X402RunFunding struct {
	ID                string    `json:"id"`
	RunID             string    `json:"runId"`
	InboundTxID       string    `json:"inboundTxId"`
	AmountAssetMicros int64     `json:"amountAssetMicros"`
	CreatedAt         time.Time `json:"createdAt"`
}
```

```go
func (s *Store) RecordRunFunding(ctx context.Context, runID, inboundTxID string, amountAssetMicros int64) (models.X402RunFunding, error) {
	var f models.X402RunFunding
	err := s.pool.QueryRow(ctx, `
		INSERT INTO x402_run_fundings (run_id, inbound_tx_id, amount_asset_micros)
		VALUES ($1, $2, $3)
		RETURNING id, run_id, inbound_tx_id, amount_asset_micros, created_at
	`, runID, inboundTxID, amountAssetMicros).Scan(&f.ID, &f.RunID, &f.InboundTxID, &f.AmountAssetMicros, &f.CreatedAt)
	return f, err
}

// RecordRunFundedSettlement inserts an x402_relay_settlements audit row
// attributed to an existing run-level bulk settlement (Task 2/5) instead of
// a fresh per-call inbound one. Takes amountAssetMicros directly at INSERT
// time — verified against the real, merged RecordOutboundSettlement
// (internal/db/store.go:735, post-PR-#39): it only ever updates
// outbound_tx_id/status, never amount_asset_micros, so there is no later
// call that could backfill a placeholder value. RecordInboundSettlement
// (the existing per-call equivalent) already sets the real amount at INSERT
// time for the same reason — this mirrors that, not a new pattern.
func (s *Store) RecordRunFundedSettlement(ctx context.Context, runFundingID, targetURL string, amountAssetMicros int64) (models.X402RelaySettlement, error) {
	var row models.X402RelaySettlement
	err := s.pool.QueryRow(ctx, `
		INSERT INTO x402_relay_settlements (target_url, run_funding_id, amount_asset_micros)
		VALUES ($1, $2, $3)
		RETURNING id, target_url, inbound_tx_id, outbound_tx_id, amount_asset_micros, status, created_at
	`, targetURL, runFundingID, amountAssetMicros).Scan(&row.ID, &row.TargetURL, &row.InboundTxID, &row.OutboundTxID, &row.AmountAssetMicros, &row.Status, &row.CreatedAt)
	return row, err
}
```

**Verified directly against the merged `x402_relay_settlements` schema and `store.go` (post-PR-#39, not assumed):**
- Real columns: `id, target_url, inbound_tx_id TEXT NOT NULL UNIQUE, outbound_tx_id TEXT, amount_asset_micros BIGINT NOT NULL, status TEXT NOT NULL DEFAULT 'pending_outbound' CHECK (...'pending_outbound','settled','failed'), created_at`. The INSERT/RETURNING column lists above match this exactly.
- `status` has a real DB default (`'pending_outbound'`) and a CHECK constraint limiting it to exactly `pending_outbound`/`settled`/`failed` — `RecordRunFundedSettlement` correctly leaves it unset (defaults to `pending_outbound`), and `RecordOutboundSettlement`'s later call must pass `"settled"` or `"failed"`, never anything else, to satisfy the CHECK.
- **`models.X402RelaySettlement.InboundTxID` is currently typed `string`, not `*string`** (`internal/models/types.go:224-232`) — this migration's `ALTER COLUMN inbound_tx_id DROP NOT NULL` means run-funded rows will have a genuinely NULL `inbound_tx_id`, and scanning a NULL column into a non-pointer Go `string` fails at the `Scan` call. **Add to this task:** change `InboundTxID` to `*string` in `models/types.go`. Blast radius checked directly (`git grep '\.InboundTxID\b'`): exactly two read sites, both in `store.go` (`RecordInboundSettlement`'s own Scan, and `GetX402RelaySettlementByInboundTx`'s Scan) — neither breaks, since scanning a non-null TEXT column into a `*string` works identically to today, just via one extra pointer indirection. No handler or frontend code reads this field. Update Step 4's file list to include `internal/models/types.go`'s existing struct edit (not just the new `X402RunFunding` type).

- [ ] **Step 5: Tests**

`TestPayTargetFromWallet2RejectsUnexpectedAsset`, `...RejectsOverCap`, `...SignFailureReturns500`, `...TargetNetworkFailureStillSigned` (assert `result.Signed == true` even though an error is also returned — reproduce-then-fix: temporarily change `Signed: true` to `Signed: false` on that branch, confirm this test fails, restore, confirm it passes), `...SuccessReturnsTargetResponse`, `...NonSuccessStatusNotSettled`.

`TestX402RunFundingConstraintRejectsNeitherIDPresent` (DB-level): inserting an `x402_relay_settlements` row with both `inbound_tx_id` and `run_funding_id` null violates the new CHECK constraint.

Full existing `x402relay_test.go` suite, unmodified, green (Step 2's acceptance criterion).

- [ ] **Step 6: Build, vet, test; commit**

```bash
TEST_DATABASE_URL=... go build ./... && go vet ./... && go test ./... -count=1
git add internal/engine/nodes/walletpay.go internal/engine/nodes/walletpay_test.go internal/api/handlers/x402relay.go internal/db/migrations/000010_x402_run_fundings.up.sql internal/db/migrations/000010_x402_run_fundings.down.sql internal/db/store.go internal/models/types.go internal/db/*_test.go internal/api/handlers/x402relay_test.go
git commit -m "nodes+db: extract shared Wallet 2 payout function, no new public surface"
```

---

### Task 4: Run-level in-memory ledger

**Files:**
- Modify: `internal/engine/runner.go`
- Test: `internal/engine/ledger_internal_test.go`

**Interfaces:**
- Consumes: `nodes.PaymentLedger` (existing struct, unchanged shape).
- Produces: `newRunLevelLedger(pool int64, wf models.Workflow, run models.Run, store *db.Store) (ledger nodes.PaymentLedger, remaining func() int64)`.

**Why this shape:** `PaymentLedger`'s three funcs are exactly what real per-payment attempts already call — reusing that interface means the loop calling into it doesn't need to know whether it's talking to the per-call DB-backed reservation or this in-memory pool. Reserve becomes an in-memory decrement instead of a DB call; Commit still writes the real audit row (unchanged, still a DB call); Release becomes an in-memory credit-back, since only the *outer* reservation is DB-backed.

- [ ] **Step 1: Add `newRunLevelLedger`**

```go
func newRunLevelLedger(pool int64, wf models.Workflow, run models.Run, store *db.Store) (nodes.PaymentLedger, func() int64) {
	var mu sync.Mutex
	remaining := pool

	ledger := nodes.PaymentLedger{
		Reserve: func(_ context.Context, amountUSDMicros int64) error {
			mu.Lock()
			defer mu.Unlock()
			if amountUSDMicros > remaining {
				return fmt.Errorf("run pre-fund pool exhausted: need %d, %d left of %d reserved for this run: %w",
					amountUSDMicros, remaining, pool, db.ErrInsufficientCredits)
			}
			remaining -= amountUSDMicros
			return nil
		},
		Commit: func(cctx context.Context, nodeID string, amountUSDMicros int64, kind string) {
			bctx, cancel := context.WithTimeout(context.WithoutCancel(cctx), ledgerCompensationTimeout)
			defer cancel()
			if err := store.CommitReservedDebit(bctx, wf.UserID, amountUSDMicros, kind, wf.ID, run.ID, nodeID); err != nil {
				msg := fmt.Sprintf("CRITICAL: commit reserved debit failed (run pre-fund pool already decremented, no ledger row written): user=%s workflow=%s run=%s node=%s kind=%s amount=%d: %v",
					wf.UserID, wf.ID, run.ID, nodeID, kind, amountUSDMicros, err)
				log.Print(msg)
				go alert.Notify(context.Background(), alert.ChannelPayments, msg)
			}
		},
		Release: func(_ context.Context, amountUSDMicros int64) {
			mu.Lock()
			defer mu.Unlock()
			remaining += amountUSDMicros
		},
	}
	return ledger, func() int64 {
		mu.Lock()
		defer mu.Unlock()
		return remaining
	}
}
```

- [ ] **Step 2: Unit test the pool semantics directly**

`TestRunLevelLedgerExhaustsPoolNotDBBalance`: `newRunLevelLedger(500000, ...)`, Reserve 300000 (ok), Reserve 300000 again (fails — only 200000 left — assert `errors.Is(err, db.ErrInsufficientCredits)`), Reserve 200000 (ok, pool now 0), Release 100000, Reserve 100000 (ok again).

Run: `go test ./internal/engine/... -run TestRunLevelLedger -v`

- [ ] **Step 3: Commit**

```bash
git add internal/engine/runner.go internal/engine/ledger_internal_test.go
git commit -m "engine: add in-memory run-level ledger for pooled x402 reservations"
```

---

### Task 5: Wire estimate → reserve → fund → in-process payout into `runner.go`

**Files:**
- Modify: `internal/engine/runner.go`, `internal/engine/nodes/tool402.go`
- Not expected to need changes: `internal/engine/nodes/provider.go` — after Task 1's refactor it already passes the whole `X402RelayConfig` struct through to `ExecuteTool402V2`; adding fields to that struct (`RunFundingID`, `Wallet2`, `RecordSettlement` below) doesn't require touching this call site again. Only edit it if that assumption turns out wrong once you're looking at the real code.
- Test: `internal/engine/debit_test.go`, `internal/engine/x402_orchestrator_integration_test.go`

**Interfaces:**
- Consumes: Task 1's `ProbeX402Price`/`ProbeX402Quote`, Task 2's `FundRunReserve`/`RunPreFundConfig`, Task 3's `PayTargetFromWallet2`/`Wallet2PayConfig`, Task 4's `newRunLevelLedger`.

- [ ] **Step 1: Add `reserveAndFundRun`**

```go
// reserveAndFundRun sizes and reserves a single run-level credit hold for
// agentNode's attached tool402 tools, then settles that exact amount as one
// real inbound x402 payment (Wallet 1 -> Wallet 2) before the agent's
// tool-calling loop starts. Size = sum of REAL, freshly-fetched quotes for
// each attached v2 tool402 node — never padded.
//
// An agent with no attached tool402 nodes, or only legacy-dialect ones,
// gets estimate=0 — a no-op returning the existing per-call
// newPaymentLedger and an empty runFundingID, so ExecuteAgent's tool402
// calls take the completely unmodified per-call public-relay path (Task 5
// Step 3 dispatches on runFundingID == "").
func (r *Runner) reserveAndFundRun(ctx context.Context, wf models.Workflow, run models.Run, attach models.AttachConfig) (nodes.PaymentLedger, string, func(context.Context), error) {
	var estimate int64
	for _, tool := range attach.Tools {
		if tool.Type != models.NodeTypeTool402 {
			continue
		}
		isV2, amount, err := nodes.ProbeX402Price(ctx, tool.Endpoint)
		if err != nil || !isV2 {
			continue // unreachable/legacy-dialect tools stay on their existing billing path
		}
		estimate += amount
	}

	if estimate == 0 {
		return r.newPaymentLedger(wf, run), "", func(context.Context) {}, nil
	}

	if err := r.store.ReserveCredits(ctx, wf.UserID, estimate); err != nil {
		return nodes.PaymentLedger{}, "", nil, err
	}

	usdcSigner, _ := r.walletSvc.(nodes.USDCGroupSigner)
	fundCfg := nodes.RunPreFundConfig{
		USDCSigner:               usdcSigner,
		PlatformSpendEncMnemonic: r.platformSpendEncMnemonic,
		Facilitator:              r.x402.FacilitatorClient,
		PlatformWalletAddress:    r.x402.PlatformWalletAddress,
		RelayNetwork:             r.x402.RelayNetwork,
		RelayFeePayer:            r.x402.RelayFeePayer,
		ExpectedAssetID:          r.x402.USDCAssetID,
		PublicBaseURL:            r.relayBaseURL,
	}
	txID, err := nodes.FundRunReserve(ctx, fundCfg, run.ID, estimate)
	if err != nil {
		bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ledgerCompensationTimeout)
		defer cancel()
		if relErr := r.store.ReleaseReservedCredits(bctx, wf.UserID, estimate); relErr != nil {
			log.Printf("CRITICAL: run pre-fund failed AND release failed (balance stranded): user=%s workflow=%s run=%s amount=%d fundErr=%v releaseErr=%v",
				wf.UserID, wf.ID, run.ID, estimate, err, relErr)
			go alert.Notify(context.Background(), alert.ChannelPayments, fmt.Sprintf("run pre-fund+release both failed: user=%s amount=%d", wf.UserID, estimate))
		}
		return nodes.PaymentLedger{}, "", nil, fmt.Errorf("x402 run funding failed: %w", err)
	}

	funding, err := r.store.RecordRunFunding(ctx, run.ID, txID, estimate)
	if err != nil {
		// Real money already moved on-chain — this is a bookkeeping failure,
		// not a payment failure. Do NOT release the DB reservation (the
		// on-chain settle genuinely happened); alert so an operator can
		// reconcile the missing audit row by hand.
		log.Printf("CRITICAL: run funding settled on-chain (tx %s) but RecordRunFunding failed: %v", txID, err)
		go alert.Notify(context.Background(), alert.ChannelPayments, fmt.Sprintf("run funding tx %s settled but not recorded: %v", txID, err))
	}

	ledger, remaining := newRunLevelLedger(estimate, wf, run, r.store)
	cleanup := func(cctx context.Context) {
		unused := remaining()
		if unused <= 0 {
			return
		}
		bctx, cancel := context.WithTimeout(context.WithoutCancel(cctx), ledgerCompensationTimeout)
		defer cancel()
		if err := r.store.ReleaseReservedCredits(bctx, wf.UserID, unused); err != nil {
			msg := fmt.Sprintf("CRITICAL: run-level release failed (balance permanently stranded): user=%s workflow=%s run=%s amount=%d: %v",
				wf.UserID, wf.ID, run.ID, unused, err)
			log.Print(msg)
			go alert.Notify(context.Background(), alert.ChannelPayments, msg)
		}
	}
	return ledger, funding.ID, cleanup, nil
}
```

- [ ] **Step 2: Call it from the `NodeTypeAgent` case**

```go
	attach := attachMap[node.ID]
	runLedger, runFundingID, cleanupRunFund, err := r.reserveAndFundRun(ctx, wf, run, attach)
	if err != nil {
		return nil, err
	}
	defer cleanupRunFund(ctx)

	usdcSigner, _ := r.walletSvc.(nodes.USDCGroupSigner)
	relayCfg := nodes.X402RelayConfig{
		USDCSigner:               usdcSigner,
		PlatformSpendEncMnemonic: r.platformSpendEncMnemonic,
		ExpectedAssetID:          r.x402.USDCAssetID,
		RelayBaseURL:             r.relayBaseURL,
		Ledger:                   runLedger,
		RunFundingID:             runFundingID, // "" => existing unmodified per-call public-relay path
		Wallet2:                  nodes.Wallet2PayConfig{
			USDCSigner:                usdcSigner,
			PlatformWalletEncMnemonic: r.x402.PlatformWalletEncMnemonic,
			USDCAssetID:               r.x402.USDCAssetID,
			RelayFeePayer:             r.x402.RelayFeePayer,
			RelayNetwork:              r.x402.RelayNetwork,
			MaxRelayOutboundUSDMicros: r.x402.MaxRelayOutboundUSDMicros,
		},
		RecordSettlement: func(cctx context.Context, target string, amountUSDMicros int64, settled bool) error {
			row, err := r.store.RecordRunFundedSettlement(cctx, runFundingID, target, amountUSDMicros)
			if err != nil {
				return err
			}
			status := "failed"
			if settled {
				status = "settled"
			}
			return r.store.RecordOutboundSettlement(cctx, row.ID, "", status)
		},
	}
	result, err := nodes.ExecuteAgent(ctx, node, attach, aw, r.walletSvc, rc, checkBalance, relayCfg)
```

(`r.x402` is the new `Runner` field carrying all of this — Task 6.)

Everything after this line (existing error handling / `debitOrLog` for the agent's own flat fee / `billedFlatFeeNodeIds` loop) is untouched.

- [ ] **Step 3: Add the run-level per-call executor, dispatched on `RunFundingID`**

In `tool402.go`, add `RunFundingID string`, `Wallet2 Wallet2PayConfig`, `RecordSettlement func(ctx context.Context, target string, amountUSDMicros int64, settled bool) error` to `X402RelayConfig` (the amount param exists because `RecordRunFundedSettlement` must insert the real amount at INSERT time — see Task 3's verified-against-the-merged-schema note; there is no later call that backfills it).

**This dispatch must sit inside the existing `isV2` branch, not before it — this is the exact bug to avoid:** `RunFundingID != ""` is a property of *the agent's run* (it's set the moment the agent has at least one v2 tool attached), not of *this specific node*. An agent can have a v2 tool AND a legacy-dialect tool attached at once — the legacy one must still go through the untouched `ExecuteTool402` direct-pay path regardless of whether the run has a funding id. Gating on `RunFundingID != ""` alone, before checking this node's own dialect, would wrongly route a legacy-dialect call into `executeTool402RunLevel`, which only knows how to handle v2 targets. In `ExecuteTool402V2`, where the existing code already branches on `isV2` (Task 1, Step 2), change only the v2 branch:

```go
if isV2 {
	if relayCfg.RunFundingID != "" {
		return executeTool402RunLevel(ctx, node, relayCfg)
	}
	return executeTool402V2Relay(ctx, node, relayCfg.USDCSigner, relayCfg.PlatformSpendEncMnemonic, relayCfg.ExpectedAssetID, relayCfg.RelayBaseURL, relayCfg.Ledger)
}
// !isV2 (legacy dialect): existing branch, completely unmodified, reached
// regardless of RunFundingID.
```

```go
// executeTool402RunLevel pays a real target directly from Wallet 2,
// in-process — no HTTP round trip to our own public relay, no fresh
// inbound settle (that already happened once, in bulk, via
// reserveAndFundRun before this agent's loop started). Reserve/Commit/
// Release still go through cfg.Ledger exactly like the per-call path;
// the only difference is what's behind those calls (Task 4's in-memory
// pool instead of a DB round trip per call).
func executeTool402RunLevel(ctx context.Context, node models.WorkflowNode, cfg X402RelayConfig) (Tool402PaymentResult, error) {
	// Re-fetches the quote even though the dispatch in ExecuteTool402V2 just
	// probed this endpoint to decide isV2 — deliberate, not redundant: this
	// is the same "freshest quote right before signing" pattern
	// executeTool402V2Relay already follows for the per-call path (fetch at
	// dispatch time to route, fetch again right before paying), matching
	// this codebase's existing, documented price-drift-aware design rather
	// than trusting a quote that's already one round trip stale.
	isV2, quote, err := ProbeX402Quote(ctx, node.Endpoint)
	if err != nil {
		return Tool402PaymentResult{}, err
	}
	if !isV2 {
		return Tool402PaymentResult{Response: map[string]any{"error": "endpoint no longer speaks x402 v2"}}, nil
	}
	amount, _ := strconv.ParseInt(quote.MaxAmountRequired, 10, 64)

	if err := cfg.Ledger.Reserve(ctx, amount); err != nil {
		return Tool402PaymentResult{}, err
	}

	result, payErr := PayTargetFromWallet2(ctx, cfg.Wallet2, node.Endpoint, quote)
	recordErr := cfg.RecordSettlement(ctx, node.Endpoint, amount, result.Settled)

	if payErr != nil || recordErr != nil {
		cfg.Ledger.Release(ctx, amount)
		if payErr != nil {
			return Tool402PaymentResult{}, payErr
		}
		return Tool402PaymentResult{}, fmt.Errorf("run-level settlement record failed: %w", recordErr)
	}

	cfg.Ledger.Commit(ctx, node.ID, amount, models.DebitKindX402RelayCost)

	var response any
	if err := json.Unmarshal(result.ResponseBody, &response); err != nil {
		response = string(result.ResponseBody)
	}
	return Tool402PaymentResult{Response: response, SettledUSDMicros: amount, DebitKind: models.DebitKindX402RelayCost}, nil
}
```

- [ ] **Step 4: The failing-then-passing regression test**

`TestAgentBranchingBetweenTwoPricedToolsDoesNotBlockMidRun` in `internal/engine/debit_test.go`: two attached tool402 v2 targets priced differently (e.g. 400000 and 350000 micros), user balance 800000 (covers the sum, 750000) — agent calls both, assert neither is blocked, assert exactly one `x402_run_fundings` row and two `x402_relay_settlements` rows (both `run_funding_id`-linked, `inbound_tx_id` null) exist afterward, assert the unused 50000 is released to the user's balance at the end. Second case, balance 600000 (short of 750000): `ReserveCredits` for the sum fails up front, neither tool is ever called.

`TestRunLevelPathNeverCallsPublicRelayEndpoint`: instrument/assert (e.g. via a test double `Ledger`/HTTP client, or by asserting on call counts to `nodes.SafeHTTPClient()`'s underlying transport in a test harness) that a run-funded call makes **zero** requests to `/x402/relay` — this is the direct regression test for the bug this whole task exists to fix.

Run: `go test ./internal/engine/... -run 'TestAgentBranchingBetweenTwoPricedTools|TestRunLevelPathNeverCallsPublicRelayEndpoint' -v`

- [ ] **Step 5: Full suite; commit**

```bash
go build ./... && go vet ./... && TEST_DATABASE_URL=... go test ./... -count=1
git add internal/engine/runner.go internal/engine/nodes/tool402.go internal/engine/debit_test.go internal/engine/x402_orchestrator_integration_test.go
git commit -m "runner+tool402: reserve, pre-fund, and pay via in-process Wallet-2 payout once per agent run"
```

---

### Task 6: Thread new dependencies into `engine.NewRunner`

**Files:**
- Modify: `internal/engine/runner.go`, `cmd/server/main.go`
- Test: every existing `NewRunner(...)` call site — verified directly against the merged tree, 6 total besides `main.go`: `internal/api/handlers/runs_test.go` ×1, `internal/api/handlers/stop_test.go` ×3, `internal/engine/runner_stop_test.go` ×2 (`newTestRunner`'s `fakeRelaySigner` and `noopSigner` variants, not just one)

**Why:** `main.go` already builds `facilitatorClient`, `platformWalletAddr`, `platformWalletEncMnemonic`, `relayNetwork`, `relayFeePayer`, `maxRelayOutboundUSDMicros` for `handlers.Deps` — `engine.Runner` needs the same values (plus, newly, Wallet 2's mnemonic itself) to drive `FundRunReserve` and `PayTargetFromWallet2`. **No `INTERNAL_RELAY_TOKEN` or any new secret is introduced — this task only widens which existing, already-in-memory values `engine.Runner` also holds.**

**Review note:** `NewRunner` was already at 6 params on the prior branch; this task's naive version would push it to 12, half of them same-typed (`string`/`int64`) in a row — exactly the shape where a future edit silently swaps two arguments and the compiler can't catch it. Bundling the x402-identity values (everything past the pre-existing `relayBaseURL`/`platformSpendEncMnemonic`) into one named-field struct removes that risk; `store`/`broker`/`walletSvc` stay positional since they're structurally distinct types, not interchangeable strings.

- [ ] **Step 1: Extend `Runner` with a config struct, not more positional params**

```go
// X402Config bundles the platform-wallet/facilitator identity engine.Runner
// needs for run-level pre-funding (Task 5) — grouped into one struct rather
// than appended as more same-typed positional NewRunner params, so a future
// caller can't silently swap e.g. RelayNetwork and RelayFeePayer (both
// strings) without the compiler catching it.
type X402Config struct {
	PlatformWalletEncMnemonic string
	USDCAssetID               uint64
	FacilitatorClient         *x402.FacilitatorClient
	PlatformWalletAddress     string
	RelayNetwork              string
	RelayFeePayer             string
	MaxRelayOutboundUSDMicros int64
}

type Runner struct {
	store                    *db.Store
	broker                   *sse.Broker
	walletSvc                nodes.WalletSigner
	registry                 *runRegistry
	relayBaseURL             string
	platformSpendEncMnemonic string
	x402                     X402Config
}

func NewRunner(store *db.Store, broker *sse.Broker, walletSvc nodes.WalletSigner, relayBaseURL, platformSpendEncMnemonic string, x402Cfg X402Config) *Runner {
	return &Runner{
		store: store, broker: broker, walletSvc: walletSvc, registry: newRunRegistry(),
		relayBaseURL: relayBaseURL, platformSpendEncMnemonic: platformSpendEncMnemonic,
		x402: x402Cfg,
	}
}
```

Update `reserveAndFundRun` (Task 5) and the `NodeTypeAgent` case's `Wallet2PayConfig` construction to read from `r.x402.*` instead of the flat `r.platformWalletEncMnemonic`/`r.facilitatorClient`/etc. fields those steps originally referenced (e.g. `r.x402.PlatformWalletEncMnemonic`, `r.x402.Facilitator` → `r.x402.FacilitatorClient`, `r.x402.MaxRelayOutboundUSDMicros`, etc.) — same values, just reached through the one struct field instead of many flat ones. `r.usdcAssetID` (already used elsewhere in `runner.go` today, predating this plan) becomes `r.x402.USDCAssetID` too, for consistency — check every existing reference to `r.usdcAssetID` in `runner.go` and update them alongside this change so there isn't a stray field left over.

Add `"github.com/agentmesh/backend/internal/x402"` to `runner.go`'s imports.

- [ ] **Step 2: Update the call in `main.go`**

**Verified against the real, merged `main.go`:** `platformWalletAddr`, `platformWalletEncMnemonic`, `usdcAssetID`, `relayNetwork`, `relayFeePayer`, `facilitatorClient` all already exist as named local vars (lines 43-73) and are passed into the `handlers.Deps{}` literal by name — those five are pure pass-through, no new computation. `MaxRelayOutboundUSDMicros` is the one exception: today it's computed *inline*, directly inside the `handlers.Deps{}` literal (`MaxRelayOutboundUSDMicros: envInt64Or("MAX_RELAY_OUTBOUND_USD_MICROS", 5_000_000)`, line 112) — there is no reusable local var for it yet. Extract it to one first so both `handlers.Deps` and the new `engine.NewRunner` call read the same value instead of calling `envInt64Or` twice with the same literal default (which would drift if one copy's default is ever edited without the other):

```go
	maxRelayOutboundUSDMicros := envInt64Or("MAX_RELAY_OUTBOUND_USD_MICROS", 5_000_000) // $5.00 default
```

placed once, above both the `runner := engine.NewRunner(...)` call and the `handlers.Deps{...}` literal; update the `Deps{}` literal's `MaxRelayOutboundUSDMicros:` field to reference this new var instead of calling `envInt64Or` inline.

```go
	runner := engine.NewRunner(store, broker, walletSvc, envOr("BASE_URL", "http://localhost:8080"), platformSpendWalletEncMnemonic, engine.X402Config{
		PlatformWalletEncMnemonic: platformWalletEncMnemonic,
		USDCAssetID:               usdcAssetID,
		FacilitatorClient:         facilitatorClient,
		PlatformWalletAddress:     platformWalletAddr,
		RelayNetwork:              relayNetwork,
		RelayFeePayer:             relayFeePayer,
		MaxRelayOutboundUSDMicros: maxRelayOutboundUSDMicros,
	})
```

- [ ] **Step 3: Update all six non-`main.go` test call sites**

Append `, engine.X402Config{}` to each of the 6 sites listed above — a zero-value `X402Config` has a nil `FacilitatorClient` and empty strings throughout; none of these tests exercise an agent node with attached v2 tool402 nodes, so `estimate` stays `0` and `reserveAndFundRun` never dereferences the nil facilitator client or empty mnemonic.

- [ ] **Step 4: Full suite; commit**

```bash
go build ./... && go vet ./... && TEST_DATABASE_URL=... go test ./... -count=1
git add internal/engine/runner.go cmd/server/main.go internal/api/handlers/runs_test.go internal/api/handlers/stop_test.go internal/engine/runner_stop_test.go
git commit -m "runner: thread facilitator client and both platform wallets' identity for run-level pre-funding"
```

---

### Task 7: Close the "clear description" catalog gap + runbook update

**Files:**
- Modify: `internal/api/handlers/x402relay.go`
- Modify: `docs/superpowers/plans/2026-07-24-wallet1-operator-runbook.md`

- [ ] **Step 1: Add `description` to `relayInboundChallenge`'s `accepts[]` entry**

`"description": "AgentMesh x402 relay — settles the inbound leg and forwards payment to " + target,` alongside the existing fields. (This is the existing, unmodified public relay's own challenge — unaffected by anything else in this plan; just closes a pre-existing small catalog gap noticed while researching this feature.)

- [ ] **Step 2: Update the operator runbook**

Add a section: agents with attached x402 v2 tools now settle one inbound payment sized to the sum of those tools' real current quotes, before the agent's loop starts, instead of one inbound settle per call; each subsequent real downstream call pays out of Wallet 2 via a direct in-process function call (never the public relay endpoint), recorded via `x402_run_fundings`/`run_funding_id` for audit; Wallet 2 may carry a temporarily larger balance per in-flight run (the un-spent portion of the estimate, between pre-fund and the run's final release) on top of the existing manually-swept spread.

- [ ] **Step 3: Commit**

```bash
git add internal/api/handlers/x402relay.go docs/superpowers/plans/2026-07-24-wallet1-operator-runbook.md
git commit -m "docs+relay: add description field to inbound challenge, document run-level pre-fund in runbook"
```

---

## How this changes PR #39's three disclosed known limitations

PR #39's operator runbook (`2026-07-24-wallet1-operator-runbook.md`) documented three accepted, not-structurally-fixed limitations. This plan changes the shape of all three for run-level-funded traffic specifically — standalone tool402 nodes and any real external caller still go through the completely unmodified public relay, so all three apply to that traffic exactly as before.

- **Relay-timeout ambiguity (can't tell if inbound settled if the orchestrator's own call to `/x402/relay` hangs past 90s): narrowed, not eliminated.** Run-level-funded calls no longer make that self-HTTP-hop — `FundRunReserve`/`PayTargetFromWallet2` are direct in-process calls, so that exact shape of ambiguity goes away for this traffic. A smaller-stakes version reappears: if `FundRunReserve`'s call to the *external* GoPlausible facilitator times out, `reserveAndFundRun` can't tell whether the bulk settle actually landed before deciding whether to release the DB reservation — lower severity than the original (both wallets involved are ours, so a wrong call here is a bookkeeping/accounting risk, not a lost-funds one), but not something to claim as fully solved.
- **Quote price-drift between two fetches: genuinely improved.** Today's per-call path fetches a target's price twice (public challenge, then authoritative settle) — that two-step gap is what lets a target lowball then upcharge. `executeTool402RunLevel` fetches once and pays against that same quote immediately, closing the window entirely for run-level-funded calls. `MaxRelayOutboundUSDMicros` still applies as a backstop regardless.
- **Hard process kill (SIGKILL/OOM) stranding credits: made worse for run-level-funded traffic, not addressed here.** Today's exposure window is one call's Reserve→Commit/Release cycle. This plan's window is the entire span from `reserveAndFundRun`'s upfront `ReserveCredits(estimate)` through the whole agent tool-calling loop (up to 15 iterations) until `cleanup` runs after `ExecuteAgent` returns — a crash anywhere in that much longer window strands the whole unspent portion of the estimate, not one call's worth. This is a materially bigger blast radius per crash than what PR #39 already accepted. Not fixed by this plan; if it matters operationally, the fix is the same one PR #39's runbook already named and deferred — a graceful-shutdown drain — which remains a process-lifecycle change, out of scope here too.

## Out of scope (explicitly not built here)

- Standalone (non-agent-attached) `NodeTypeTool402` nodes — unchanged, still route through the unmodified public `/x402/relay` endpoint, per-call reserve/commit/release exactly as today.
- The legacy flat-quote dialect — unchanged.
- Any padding/safety multiplier above 1.0x on the per-run estimate.
- Automatic clawback of Wallet 2's accumulated pre-fund surplus — x402 has no chargeback primitive; stays a manual-sweep concern, potentially larger/more frequent now (Task 7 documents this).
- An agent whose loop calls the *same* attached tool402 node more than once, driving real spend above the sum-of-quotes-once estimate — surfaces as a `db.ErrInsufficientCredits`-wrapped error from the run-level ledger's `Reserve` (Task 4) and blocks that specific call. Same failure mode as today, just rarer.
- Any HTTP-reachable bypass, internal token, or shared secret on the public relay endpoint — considered and explicitly rejected during design (see the "design point settled during review" note above).
