package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agentmesh/backend/internal/alert"
	"github.com/agentmesh/backend/internal/db"
	"github.com/agentmesh/backend/internal/engine/nodes"
	"github.com/agentmesh/backend/internal/models"
	"github.com/agentmesh/backend/internal/sse"
)

type Runner struct {
	store                    *db.Store
	broker                   *sse.Broker
	walletSvc                nodes.WalletSigner
	registry                 *runRegistry
	relayBaseURL             string
	platformSpendEncMnemonic string
	usdcAssetID              uint64
}

func NewRunner(store *db.Store, broker *sse.Broker, walletSvc nodes.WalletSigner, relayBaseURL string, platformSpendEncMnemonic string, usdcAssetID uint64) *Runner {
	return &Runner{
		store:                    store,
		broker:                   broker,
		walletSvc:                walletSvc,
		registry:                 newRunRegistry(),
		relayBaseURL:             relayBaseURL,
		platformSpendEncMnemonic: platformSpendEncMnemonic,
		usdcAssetID:              usdcAssetID,
	}
}

// preflightCheck fails a node before it runs if wf.UserID can't cover
// amountUSDMicros. Blocks outright — no soft overage — matching the
// prepaid-only model already used for credit top-ups.
func (r *Runner) preflightCheck(ctx context.Context, wf models.Workflow, amountUSDMicros int64) error {
	balance, err := r.store.GetCreditBalance(ctx, wf.UserID)
	if err != nil {
		return err
	}
	if balance < amountUSDMicros {
		return fmt.Errorf("insufficient credits: balance %d micros, need %d micros", balance, amountUSDMicros)
	}
	return nil
}

// debitOrLog charges amountUSDMicros against wf.UserID for nodeID and just
// logs on failure rather than failing the node — the node already ran
// successfully by the time this is called, so there's nothing left to roll
// back (x402 payments in particular can't be undone once sent on-chain).
func (r *Runner) debitOrLog(ctx context.Context, wf models.Workflow, run models.Run, nodeID string, amountUSDMicros int64, kind string) {
	if err := r.store.DebitCredits(ctx, wf.UserID, amountUSDMicros, kind, wf.ID, run.ID, nodeID); err != nil {
		log.Printf("debit failed: user=%s workflow=%s run=%s node=%s kind=%s amount=%d: %v",
			wf.UserID, wf.ID, run.ID, nodeID, kind, amountUSDMicros, err)
	}
}

// ledgerCompensationTimeout bounds Commit/Release calls once they're
// detached from the triggering request's context (see newPaymentLedger) —
// long enough for a single locked UPDATE, short enough not to hang a
// terminating process indefinitely.
const ledgerCompensationTimeout = 10 * time.Second

// newPaymentLedger builds the reserve/commit/release closures a real
// on-chain tool402 payment (either dialect, standalone or agent-attached)
// uses to atomically decrement the user's balance at the moment a payment
// is committed to, before it's attempted — instead of checking balance and
// only debiting afterward, which would let multiple calls within the same
// node execution (an agent's sequential tool loop, or concurrent standalone
// tool402 nodes in the same topology level) all pass a check against the
// same stale balance and collectively overspend past what the user can
// cover. See nodes.PaymentLedger.
//
// Commit and Release are compensating actions for money that has already
// moved (or a reservation that must be undone) — they run with
// context.WithoutCancel, not the caller's cctx. If they inherited a
// cancelled/deadline-exceeded context (e.g. Runner.Stop firing mid-payment,
// or the outbound HTTP call timing out), the resulting DB call would be a
// no-op that neither writes the debit_ledger row nor restores the reserved
// balance, silently stranding the reservation as a permanent, unledgered
// credit loss. UpdateRunLog already establishes this same
// context.Background()-after-cancellation convention elsewhere in Run.
func (r *Runner) newPaymentLedger(wf models.Workflow, run models.Run) nodes.PaymentLedger {
	return nodes.PaymentLedger{
		Reserve: func(cctx context.Context, amountUSDMicros int64) error {
			return r.store.ReserveCredits(cctx, wf.UserID, amountUSDMicros)
		},
		Commit: func(cctx context.Context, nodeID string, amountUSDMicros int64, kind string) {
			bctx, cancel := context.WithTimeout(context.WithoutCancel(cctx), ledgerCompensationTimeout)
			defer cancel()
			if err := r.store.CommitReservedDebit(bctx, wf.UserID, amountUSDMicros, kind, wf.ID, run.ID, nodeID); err != nil {
				msg := fmt.Sprintf("CRITICAL: commit reserved debit failed (balance already decremented, no ledger row written): user=%s workflow=%s run=%s node=%s kind=%s amount=%d: %v",
					wf.UserID, wf.ID, run.ID, nodeID, kind, amountUSDMicros, err)
				log.Print(msg)
				go alert.Notify(context.Background(), alert.ChannelPayments, msg)
			}
		},
		Release: func(cctx context.Context, amountUSDMicros int64) {
			bctx, cancel := context.WithTimeout(context.WithoutCancel(cctx), ledgerCompensationTimeout)
			defer cancel()
			if err := r.store.ReleaseReservedCredits(bctx, wf.UserID, amountUSDMicros); err != nil {
				msg := fmt.Sprintf("CRITICAL: release reserved credits failed (balance permanently stranded): user=%s workflow=%s run=%s amount=%d: %v",
					wf.UserID, wf.ID, run.ID, amountUSDMicros, err)
				log.Print(msg)
				go alert.Notify(context.Background(), alert.ChannelPayments, msg)
			}
		},
	}
}

// newRunLevelLedger builds an in-memory credit pool for a single run,
// atomically tracking reservations against a fixed budget instead of hitting
// the DB per-call. Reserve decrements the pool; Commit writes the permanent
// audit row (DB-backed, same as newPaymentLedger); Release credits back the
// in-memory balance (unlike newPaymentLedger, which also calls the DB). See
// nodes.PaymentLedger for the full contract.
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

// Start creates a cancellable context for the run, registers it, and launches
// Run in a goroutine. Replaces the previous pattern of calling Run directly.
func (r *Runner) Start(wf models.Workflow, run models.Run) {
	ctx, cancel := context.WithCancel(context.Background())
	r.registry.register(wf.ID, cancel)
	go r.Run(ctx, wf, run)
}

// Stop cancels the active run for the given workflow ID. Returns false if no
// run was registered (i.e. the workflow is not currently running).
func (r *Runner) Stop(workflowID string) bool {
	return r.registry.cancel(workflowID)
}

// finishRun records the run's terminal status and fires a workflow-run audit-log
// notification. Centralized here so every terminal path (success, failed, stopped)
// reports to the same Discord channel with the same message shape.
func (r *Runner) finishRun(wf models.Workflow, run models.Run, status models.RunStatus) {
	r.store.FinishRun(context.Background(), run.ID, status)
	go alert.Notify(context.Background(), alert.ChannelWorkflows, fmt.Sprintf("workflow %q run %s finished: %s", wf.Name, run.ID, status))
}

// Run executes a workflow. Call via Start rather than directly.
func (r *Runner) Run(ctx context.Context, wf models.Workflow, run models.Run) {
	defer r.broker.Close(run.ID)
	defer r.registry.deregister(wf.ID)

	go alert.Notify(context.Background(), alert.ChannelWorkflows, fmt.Sprintf("workflow %q run %s started", wf.Name, run.ID))

	attachMap := BuildAttachMap(wf.Nodes, wf.Edges)
	levels, err := TopologicalSort(wf.Nodes, wf.Edges)
	if err != nil {
		r.finishRun(wf, run, models.RunStatusFailed)
		return
	}

	// Build set of tool/tool402 nodes that are ONLY connected via attach edges to
	// agents. These must NOT be executed as standalone topology steps — the agent
	// LLM drives them through function calling at runtime.
	agentToolIDs := make(map[string]bool)
	for _, e := range wf.Edges {
		if e.Kind == models.EdgeKindAttach && e.ToPort == "tools" {
			agentToolIDs[e.From] = true
		}
	}

	// Pre-load all agent wallets for this workflow so tool402 nodes can resolve
	// their parent agent's wallet without hitting the DB per-node.
	walletByAgent := make(map[string]models.AgentWallet)
	if wallets, err := r.store.ListAgentWallets(ctx, run.WorkflowID); err == nil {
		for _, w := range wallets {
			walletByAgent[w.AgentNodeID] = w
		}
	}

	var inputJSON []byte
	if run.InputContext != nil {
		inputJSON, _ = json.Marshal(run.InputContext)
	}
	rc := NewRunContext(run.ID, inputJSON)

	var failed int32

	for stepIdx, level := range levels {
		// Check for cancellation between levels.
		if ctx.Err() != nil {
			r.finishRun(wf, run, models.RunStatusStopped)
			return
		}

		var wg sync.WaitGroup
		for _, node := range level {
			wg.Add(1)
			go func(n models.WorkflowNode, idx int) {
				defer wg.Done()
				// Skip attached tools — the agent invokes them via function calling.
				if agentToolIDs[n.ID] {
					return
				}
				if atomic.LoadInt32(&failed) != 0 {
					return
				}

				start := time.Now()
				logEntry, _ := r.store.InsertRunLog(ctx, models.RunLog{
					RunID:     run.ID,
					StepIndex: idx,
					NodeID:    n.ID,
					NodeType:  n.Type,
					Status:    models.LogStatusRunning,
				})

				result, execErr := r.executeNode(ctx, n, attachMap, walletByAgent, rc, run, wf)
				dur := int(time.Since(start).Milliseconds())

				if execErr != nil {
					atomic.StoreInt32(&failed, 1)
					outJSON, _ := json.Marshal(execErr.Error())
					r.store.UpdateRunLog(context.Background(), logEntry.ID, models.LogStatusFailed, outJSON, dur)
					r.broker.Publish(run.ID, models.LogEvent{
						StepIndex:  idx,
						NodeID:     n.ID,
						NodeType:   n.Type,
						Status:     models.LogStatusFailed,
						Output:     execErr.Error(),
						DurationMs: dur,
						Ts:         time.Now(),
					})
					return
				}

				rc.Set(n.ID, result)
				outJSON, _ := json.Marshal(result)
				r.store.UpdateRunLog(context.Background(), logEntry.ID, models.LogStatusSuccess, outJSON, dur)
				r.broker.Publish(run.ID, models.LogEvent{
					StepIndex:  idx,
					NodeID:     n.ID,
					NodeType:   n.Type,
					Status:     models.LogStatusSuccess,
					Output:     result,
					DurationMs: dur,
					Ts:         time.Now(),
				})
				// Publish a separate log event per x402 payment made inside the agent loop.
				if m, ok := result.(map[string]any); ok {
					if payments, ok := m["x402Payments"].([]map[string]any); ok {
						for _, p := range payments {
							nodeID, _ := p["nodeId"].(string)
							r.broker.Publish(run.ID, models.LogEvent{
								StepIndex:  idx,
								NodeID:     nodeID,
								NodeType:   models.NodeTypeTool402,
								Status:     models.LogStatusSuccess,
								Output:     p,
								DurationMs: 0,
								Ts:         time.Now(),
							})
						}
					}
				}
			}(node, stepIdx)
		}
		wg.Wait()

		if atomic.LoadInt32(&failed) != 0 {
			r.finishRun(wf, run, models.RunStatusFailed)
			return
		}
	}

	r.finishRun(wf, run, models.RunStatusSuccess)
}

func (r *Runner) executeNode(
	ctx context.Context,
	node models.WorkflowNode,
	attachMap map[string]models.AttachConfig,
	walletByAgent map[string]models.AgentWallet,
	rc *RunContext,
	run models.Run,
	wf models.Workflow,
) (any, error) {
	switch node.Type {
	case models.NodeTypeTrigger:
		return rc.input, nil
	case models.NodeTypeEnd:
		return rc.Message(), nil
	case models.NodeTypeAgent:
		if err := r.preflightCheck(ctx, wf, models.ByokFlatFeeUSDMicros); err != nil {
			return nil, err
		}
		aw := walletByAgent[node.ID]
		checkBalance := func(cctx context.Context, amount int64) error {
			return r.preflightCheck(cctx, wf, amount)
		}
		// r.walletSvc's dynamic type (*wallet.Service) also satisfies
		// USDCGroupSigner (same nil-safe assertion as the NodeTypeTool402
		// case below) — an agent-attached tool402 call routes through the
		// same relay/Wallet 1 path as a standalone one.
		usdcSigner, _ := r.walletSvc.(nodes.USDCGroupSigner)
		relayCfg := nodes.X402RelayConfig{
			USDCSigner:               usdcSigner,
			PlatformSpendEncMnemonic: r.platformSpendEncMnemonic,
			ExpectedAssetID:          r.usdcAssetID,
			RelayBaseURL:             r.relayBaseURL,
			Ledger:                   r.newPaymentLedger(wf, run),
		}
		result, err := nodes.ExecuteAgent(ctx, node, attachMap[node.ID], aw, r.walletSvc, rc, checkBalance, relayCfg)
		if err != nil {
			// A *nodes.ErrBalanceBlocked failure means the agent's own LLM
			// turn already completed and only ran into insufficient balance
			// when it tried an attached call — the agent's own flat fee is
			// still owed. Any other error (e.g. LLM connectivity failure)
			// means the agent turn itself never completed, so nothing is
			// billed, matching the pre-existing behavior for those failures.
			var blocked *nodes.ErrBalanceBlocked
			if errors.As(err, &blocked) {
				r.debitOrLog(ctx, wf, run, node.ID, models.ByokFlatFeeUSDMicros, models.DebitKindByokFlatFee)
			}
			return nil, err
		}
		r.debitOrLog(ctx, wf, run, node.ID, models.ByokFlatFeeUSDMicros, models.DebitKindByokFlatFee)
		if m, ok := result.(map[string]any); ok {
			// x402Payments entries are already reserved+committed via
			// relayCfg.Ledger from inside ExecuteAgent's tool-calling loop, at
			// the moment each payment settled — not batched here. Batching the
			// debit until after the whole agent turn completes would let every
			// iteration of the loop check the same stale balance and
			// collectively overspend past what the user can cover; see
			// newPaymentLedger. This entry is retained in the result only so
			// Run() can still publish a log/SSE event per payment below.
			if nodeIDs, ok := m["billedFlatFeeNodeIds"].([]string); ok {
				for _, nodeID := range nodeIDs {
					r.debitOrLog(ctx, wf, run, nodeID, models.ByokFlatFeeUSDMicros, models.DebitKindByokFlatFee)
				}
			}
		}
		return result, nil
	case models.NodeTypeProvider:
		return rc.Message(), nil
	case models.NodeTypeTool:
		billable := nodes.BillableFlatFee(node.Type, node.Template)
		if billable {
			if err := r.preflightCheck(ctx, wf, models.ByokFlatFeeUSDMicros); err != nil {
				return nil, err
			}
		}
		result, err := nodes.ExecuteTool(ctx, node, rc)
		if err != nil {
			return nil, err
		}
		if billable {
			r.debitOrLog(ctx, wf, run, node.ID, models.ByokFlatFeeUSDMicros, models.DebitKindByokFlatFee)
		}
		return result, nil
	case models.NodeTypeTool402:
		// Find the agent that has this tool attached and use its wallet (only
		// the legacy direct-pay dialect still needs this; the relay dialect
		// pays from the platform's own Wallet 1 spend wallet instead).
		var aw models.AgentWallet
		for agentID, cfg := range attachMap {
			for _, t := range cfg.Tools {
				if t.ID == node.ID {
					aw = walletByAgent[agentID]
				}
			}
		}
		// r.walletSvc's dynamic type (*wallet.Service) also satisfies
		// USDCGroupSigner (Task 3); the assertion is nil-safe if a test double
		// only implements WalletSigner, and ExecuteTool402V2 falls back to a
		// graceful "no wallet configured" result rather than paying via relay.
		usdcSigner, _ := r.walletSvc.(nodes.USDCGroupSigner)
		// Cheap, conservative guard before any network call to node.Endpoint —
		// see the matching comment in provider.go's executeFunctionCall. The
		// real, exact-amount reservation happens inside ExecuteTool402V2 via
		// ledger below.
		if err := r.preflightCheck(ctx, wf, models.X402PlatformFeeUSDMicros); err != nil {
			return nil, err
		}
		relayCfg := nodes.X402RelayConfig{
			USDCSigner:               usdcSigner,
			PlatformSpendEncMnemonic: r.platformSpendEncMnemonic,
			ExpectedAssetID:          r.usdcAssetID,
			RelayBaseURL:             r.relayBaseURL,
			Ledger:                   r.newPaymentLedger(wf, run),
		}
		paymentResult, err := nodes.ExecuteTool402V2(ctx, node, rc, aw, r.walletSvc, relayCfg)
		if err != nil {
			return nil, err
		}
		// Already reserved+committed via ledger inside ExecuteTool402V2, at
		// the moment the payment settled — see newPaymentLedger.
		return paymentResult.Response, nil
	case models.NodeTypeAction:
		billable := nodes.BillableFlatFee(node.Type, node.Template)
		if billable {
			if err := r.preflightCheck(ctx, wf, models.ByokFlatFeeUSDMicros); err != nil {
				return nil, err
			}
		}
		result, err := nodes.ExecuteAction(ctx, node, rc)
		if err != nil {
			if errors.Is(err, nodes.ErrActionSkipped) {
				return result, nil
			}
			return nil, err
		}
		if billable {
			r.debitOrLog(ctx, wf, run, node.ID, models.ByokFlatFeeUSDMicros, models.DebitKindByokFlatFee)
		}
		return result, nil
	default:
		return nil, nil
	}
}
