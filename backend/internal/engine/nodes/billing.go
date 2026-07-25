package nodes

import (
	"context"
	"errors"

	"github.com/agentmesh/backend/internal/models"
)

// BillableFlatFee reports whether executing a node performs a real
// off-platform action that should be charged the flat BYOK convenience
// fee. Every Action node (email + all 22 connectors) is billable — in
// practice every Action node has a real, recognized template, since the
// UI only ever creates nodes from its connector list; the "logged"
// fallback in ExecuteAction's switch is a defensive no-op that doesn't
// occur in real workflows, so it isn't special-cased here. Agent nodes
// are always billable — every agent call is BYOK today (issue #25 will
// add a platform-key toggle, which is where this stops being true
// unconditionally). Tool nodes are billable only for the "http" template
// — "calc" and "datetime" are pure local computation, no different from
// Trigger/End, and stay free. Tool402 is metered separately — relay-path
// payments charge the real settled amount, legacy direct-pay charges a
// flat fee, both gated on whether a payment actually happened at runtime,
// not on static config — so it always returns false here.
func BillableFlatFee(nodeType models.NodeType, template string) bool {
	switch nodeType {
	case models.NodeTypeAgent, models.NodeTypeAction:
		return true
	case models.NodeTypeTool:
		return template == "http"
	default:
		return false
	}
}

// BalanceChecker lets the nodes package ask the caller whether a billable
// attached tool/x402 call can proceed, without giving nodes direct DB
// access. Returns a non-nil error (matching preflightCheck's error text
// convention) when the balance is insufficient.
type BalanceChecker func(ctx context.Context, amountUSDMicros int64) error

// PaymentLedger bundles the reserve/commit/release protocol a real on-chain
// x402 payment (either dialect) uses to close the gap between "balance
// checked" and "balance charged": Reserve atomically decrements the balance
// before the payment is attempted (so concurrent or sequential calls within
// one node execution can't all pass a check against the same stale
// balance); Commit writes the permanent debit_ledger audit row once the
// payment is confirmed settled; Release credits back a reservation that
// never became a real charge. A nil field is treated as a no-op — Reserve
// nil means unconditionally allowed, matching the pre-existing nil-checker
// convention elsewhere in this package. Production callers (runner.go)
// always supply all three; nil fields are for tests that don't exercise a
// billable tool402 payment at all.
type PaymentLedger struct {
	Reserve func(ctx context.Context, amountUSDMicros int64) error
	Commit  func(ctx context.Context, nodeID string, amountUSDMicros int64, kind string)
	Release func(ctx context.Context, amountUSDMicros int64)
}

// ErrActionSkipped is returned by Action node implementations (email + all
// connectors) when required credentials/config are missing, so the node
// short-circuits before making any real network call. runner.go's
// NodeTypeAction case treats this as a successful no-op: the descriptive
// skip message is still returned to the workflow as the node's result, but
// the flat BYOK fee is not charged, since no billable work happened.
var ErrActionSkipped = errors.New("action skipped: missing required configuration")

// ErrBalanceBlocked wraps a BalanceChecker failure so the agent loop can
// hard-stop instead of feeding the failure back to the LLM as a retryable
// tool-level error (which would just spin the loop until
// maxToolIterations, contradicting the "blocks before it runs, no soft
// overage" contract). Callers use errors.As to distinguish this from other
// ExecuteAgent failures (e.g. LLM connectivity errors): a
// *ErrBalanceBlocked failure means the agent's own LLM turn already ran
// (so its flat fee is still owed) and only the subsequent attached call
// was blocked; any other error means the agent turn itself never
// completed, so nothing should be billed.
type ErrBalanceBlocked struct {
	Err error
}

func (e *ErrBalanceBlocked) Error() string { return e.Err.Error() }
func (e *ErrBalanceBlocked) Unwrap() error { return e.Err }
