package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/agentmesh/backend/internal/models"
	"github.com/agentmesh/backend/internal/sshkeys"
	"github.com/agentmesh/backend/internal/tendril"
	"github.com/agentmesh/backend/internal/wallet"
)

// tendrilRentGateFeeAtomic is Tendril's flat charge to open a lease, confirmed
// live 2026-08-04 in the /x402/rent challenge (amount "10000", 6 decimals).
// Renting does NOT buy time — time meters from the paying address's credit
// balance, which is why RequiredCreditAtomic adds hours on top.
const tendrilRentGateFeeAtomic int64 = 10_000

// maxTendrilHours caps a single rent. At $6/hr a fat-fingered "100" would
// commit $600 of real mainnet USDC in one click.
const maxTendrilHours = 24.0

// RequiredCreditAtomic is how much of THIS USER's Tendril credit a rent
// reserves. Not the pool's balance — the pool is a shared custodial float that
// holds every user's topups at once, so it can never be the thing a rent is
// authorized against.
func RequiredCreditAtomic(rateUSDMicrosPerHour int64, hours float64) int64 {
	return int64(float64(rateUSDMicrosPerHour)*hours+0.5) + tendrilRentGateFeeAtomic
}

func parseHours(raw string) (float64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 1, nil
	}
	h, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("tendril: hours %q is not a number", raw)
	}
	if h <= 0 {
		return 0, fmt.Errorf("tendril: hours must be positive, got %v", h)
	}
	if h > maxTendrilHours {
		return 0, fmt.Errorf("tendril: hours must be at most %v, got %v", maxTendrilHours, h)
	}
	return h, nil
}

func parseTopupUSD(raw string) (float64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("tendril: set a topup amount in USD")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("tendril: topup amount %q is not a number", raw)
	}
	if v <= 0 {
		return 0, fmt.Errorf("tendril: topup amount must be positive, got %v", v)
	}
	return v, nil
}

// TendrilStore is the slice of *db.Store this package needs, as an interface
// so tests can drive the executor without a database.
type TendrilStore interface {
	InsertTendrilLease(ctx context.Context, l models.TendrilLease) (models.TendrilLease, error)
	GetTendrilLease(ctx context.Context, id string) (models.TendrilLease, error)
	MarkTendrilLeaseReleased(ctx context.Context, id string, usedSeconds, chargedUSDMicros int64) error
	LatestActiveLeaseForRun(ctx context.Context, runID string) (models.TendrilLease, error)
	// Credit sub-ledger (Task 6) — the authority on what THIS user may spend.
	TendrilCreditBalance(ctx context.Context, userID string) (int64, error)
	CreditBalance(ctx context.Context, userID string) (int64, error)
	ConvertCreditsToTendril(ctx context.Context, userID string, amountUSDMicros int64, txID string) (int64, error)
	ChargeTendrilCredit(ctx context.Context, userID, leaseID, kind string, amountUSDMicros int64) error
}

type TendrilConfig struct {
	Client     *tendril.Client
	Session    *tendril.Session
	Store      TendrilStore
	EncryptKey string
	Relay      X402RelayConfig
	UserID     string
	WorkflowID string
	RunID      string
}

func ExecuteTendril(ctx context.Context, node models.WorkflowNode, rc RunContexter, cfg TendrilConfig) (any, error) {
	switch node.TendrilAction {
	case "", "rent":
		return executeTendrilRent(ctx, node, cfg)
	case "topup":
		return executeTendrilTopup(ctx, node, cfg)
	case "run":
		return executeTendrilRun(ctx, node, rc, cfg)
	case "release":
		return executeTendrilRelease(ctx, node, cfg)
	default:
		return nil, fmt.Errorf("tendril: unknown action %q", node.TendrilAction)
	}
}

// executeTendrilTopup buys Tendril credit for THIS user. It settles a real
// USDC payment into the shared Wallet 2 pool and, in the same breath, moves
// the same value from the user's AgentMesh credits to their Tendril credits.
//
// The pool is universal — every user's topups accumulate in it — but what a
// user may spend is only ever their own converted balance. That is why the
// conversion is not optional bookkeeping: it IS the spending authority.
func executeTendrilTopup(ctx context.Context, node models.WorkflowNode, cfg TendrilConfig) (any, error) {
	amountUSD, err := parseTopupUSD(node.TendrilAmount)
	if err != nil {
		return nil, err
	}
	atomic := int64(amountUSD*1e6 + 0.5)

	// Tendril's own bounds, read live rather than hardcoded.
	platform, err := cfg.Client.Platform(ctx)
	if err != nil {
		return nil, fmt.Errorf("tendril: platform: %w", err)
	}
	if platform.MinTopUpAtomic > 0 && atomic < platform.MinTopUpAtomic {
		return nil, fmt.Errorf("tendril: minimum topup is %s", formatUSDCAmount(platform.MinTopUpAtomic))
	}
	if platform.MaxTopUpAtomic > 0 && atomic > platform.MaxTopUpAtomic {
		return nil, fmt.Errorf("tendril: maximum topup is %s", formatUSDCAmount(platform.MaxTopUpAtomic))
	}

	// Refuse before paying if the user cannot afford it. Settling first and
	// discovering the shortfall afterwards would put real USDC in the pool
	// with no user entitled to spend it.
	balance, err := cfg.Store.TendrilCreditBalance(ctx, cfg.UserID)
	if err != nil {
		return nil, err
	}
	agentMeshBalance, err := cfg.Store.CreditBalance(ctx, cfg.UserID)
	if err != nil {
		return nil, err
	}
	if agentMeshBalance < atomic {
		return nil, fmt.Errorf("tendril: topup of %s needs %s in AgentMesh credits, you have %s",
			formatUSDCAmount(atomic), formatUSDCAmount(atomic), formatUSDCAmount(agentMeshBalance))
	}

	receipt, err := payTendril(ctx, cfg, fmt.Sprintf("/topup?amount=%d", atomic), nil, "")
	if err != nil {
		return nil, err
	}

	txID := ""
	if m, ok := receipt.(map[string]any); ok {
		txID, _ = m["txId"].(string)
	}
	newBalance, err := cfg.Store.ConvertCreditsToTendril(ctx, cfg.UserID, atomic, txID)
	if err != nil {
		// The USDC really moved into the pool. Surface that loudly: the pool
		// is now larger than the sum of user entitlements, which is the one
		// direction of drift that is safe but must still be reconciled.
		return nil, fmt.Errorf("tendril: topup settled on-chain (tx %s) but crediting your balance failed — contact support with that tx id: %w", txID, err)
	}

	out := map[string]any{
		"toppedUp":             formatUSDCAmount(atomic),
		"tendrilCreditBalance": formatUSDCAmount(newBalance),
		"previousBalance":      formatUSDCAmount(balance),
		"note":                 "Tendril credit is separate from your AgentMesh credits and can only be spent on Tendril machine time.",
	}
	if m, ok := receipt.(map[string]any); ok {
		for _, k := range []string{"txId", "explorerURL", "outboundTxId", "outboundExplorerURL"} {
			if v, ok := m[k]; ok {
				out[k] = v
			}
		}
	}
	return out, nil
}

func executeTendrilRent(ctx context.Context, node models.WorkflowNode, cfg TendrilConfig) (any, error) {
	hours, err := parseHours(node.TendrilHours)
	if err != nil {
		return nil, err
	}

	machines, err := cfg.Client.OnlineNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("tendril: market: %w", err)
	}
	if len(machines) == 0 {
		return nil, fmt.Errorf("tendril: no machines are online right now")
	}
	machine := machines[0] // cheapest, per OnlineNodes' ordering
	if node.TendrilNodeID != "" {
		found := false
		for _, m := range machines {
			if m.ID == node.TendrilNodeID {
				machine, found = m, true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("tendril: machine %q is not online", node.TendrilNodeID)
		}
	}

	// Reserve the hours against THIS user's Tendril credit. The shared Wallet 2
	// pool is deliberately not consulted: it holds every user's topups at once,
	// so checking it would let one user rent on hours somebody else bought.
	// Their own balance is the only authority.
	need := RequiredCreditAtomic(machine.RateUSDMicrosPerHour(), hours)
	userCredit, err := cfg.Store.TendrilCreditBalance(ctx, cfg.UserID)
	if err != nil {
		return nil, err
	}
	if userCredit < need {
		return nil, fmt.Errorf(
			"tendril: %v hour(s) on %s costs %s but your Tendril credit is %s — add a Topup node, or raise its amount, before this Rent node",
			hours, machine.ID, formatUSDCAmount(need), formatUSDCAmount(userCredit))
	}
	if err := cfg.Store.ChargeTendrilCredit(ctx, cfg.UserID, "", "charge", need); err != nil {
		return nil, fmt.Errorf("tendril: reserve credit: %w", err)
	}
	// From here on the user has paid; any failure below must hand the
	// reservation back rather than silently keeping it.
	reserved := true
	defer func() {
		if reserved {
			if err := cfg.Store.ChargeTendrilCredit(context.Background(), cfg.UserID, "", "refund", need); err != nil {
				log.Printf("tendril: FAILED to refund %d micros to user %s after a failed rent: %v", need, cfg.UserID, err)
			}
		}
	}()

	// A sanity check on the custodial float, not an authorization check. If the
	// pool cannot cover what users have collectively bought, the invariant has
	// been violated upstream and renting would silently fail at Tendril's end.
	if poolBalance, perr := cfg.Session.Balance(ctx); perr == nil && poolBalance < need {
		return nil, fmt.Errorf("tendril: the platform pool is short (%s available, %s needed) — this is a platform-side problem, not yours; no credit was spent",
			formatUSDCAmount(poolBalance), formatUSDCAmount(need))
	}

	sshPub, sshPriv, err := sshkeys.Generate()
	if err != nil {
		return nil, fmt.Errorf("tendril: ssh keygen: %w", err)
	}
	body, _ := json.Marshal(map[string]string{"sshPubKey": sshPub})
	raw, err := payTendril(ctx, cfg, "/x402/rent?nodeId="+machine.ID, body, "")
	if err != nil {
		return nil, err
	}

	lease, err := decodeRentResponse(raw)
	if err != nil {
		return nil, err
	}

	tokenEnc, err := wallet.Encrypt(lease.LeaseToken, cfg.EncryptKey)
	if err != nil {
		return nil, fmt.Errorf("tendril: encrypt lease token: %w", err)
	}
	keyEnc, err := wallet.Encrypt(sshPriv, cfg.EncryptKey)
	if err != nil {
		return nil, fmt.Errorf("tendril: encrypt ssh key: %w", err)
	}
	passwordEnc := ""
	if lease.SSH.Password != "" {
		if passwordEnc, err = wallet.Encrypt(lease.SSH.Password, cfg.EncryptKey); err != nil {
			return nil, fmt.Errorf("tendril: encrypt ssh password: %w", err)
		}
	}

	fundedUntil, err := time.Parse(time.RFC3339, lease.FundedUntil)
	if err != nil {
		fundedUntil = time.Now().Add(time.Duration(hours * float64(time.Hour)))
	}

	saved, err := cfg.Store.InsertTendrilLease(ctx, models.TendrilLease{
		UserID: cfg.UserID, WorkflowID: cfg.WorkflowID, RunID: cfg.RunID, NodeID: node.ID,
		LeaseID: lease.LeaseID, LeaseTokenEnc: tokenEnc,
		TendrilNodeID: machine.ID, TendrilNodeLabel: machine.Label,
		SSHHost: lease.SSH.Host, SSHPort: lease.SSH.Port, SSHUsername: lease.SSH.Username,
		SSHCommand: lease.SSH.Command, SSHPublicKey: sshPub,
		SSHPrivateKeyEnc: keyEnc, SSHPasswordEnc: passwordEnc,
		RateUSDMicrosPerHour: machine.RateUSDMicrosPerHour(),
		HoursPurchased:       hours,
		ReservedUSDMicros:    need,
		FundedUntil:          fundedUntil,
	})
	if err != nil {
		return nil, fmt.Errorf("tendril: persist lease: %w", err)
	}
	// The lease exists and is metering — the reservation is now legitimately
	// spent, so stop the deferred refund from clawing it back.
	reserved = false

	remaining, _ := cfg.Store.TendrilCreditBalance(ctx, cfg.UserID)

	// The lease token never leaves the server. Everything here is safe to show
	// in the console and to cache in localStorage with the run transcript.
	out := map[string]any{
		"agentMeshLeaseId": saved.ID,
		"leaseId":          lease.LeaseID,
		"machine":          map[string]any{"id": machine.ID, "label": machine.Label, "cpuCores": machine.CPUCores, "ramMb": machine.RAMMb, "pricePerHourUsd": machine.PricePerHourUSD},
		"hours":            hours,
		"ssh":              map[string]any{"host": lease.SSH.Host, "port": lease.SSH.Port, "username": lease.SSH.Username, "command": lease.SSH.Command},
		"fundedUntil":      fundedUntil.Format(time.RFC3339),
		"reservedUsd":      formatUSDCAmount(need),
		// What the user has left to spend on Tendril — the number the canvas
		// shows them, and the only balance that governs what they may rent.
		"tendrilCreditBalance": formatUSDCAmount(remaining),
	}
	if m, ok := raw.(map[string]any); ok {
		for _, k := range []string{"txId", "explorerURL", "outboundTxId", "outboundExplorerURL"} {
			if v, ok := m[k]; ok {
				out[k] = v
			}
		}
	}
	return out, nil
}

type rentResponse struct {
	LeaseID     string `json:"leaseId"`
	LeaseToken  string `json:"leaseToken"`
	FundedUntil string `json:"fundedUntil"`
	SSH         struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Username string `json:"username"`
		Command  string `json:"command"`
		Password string `json:"password"`
	} `json:"ssh"`
}

func decodeRentResponse(raw any) (rentResponse, error) {
	var lease rentResponse
	blob, err := json.Marshal(raw)
	if err != nil {
		return lease, fmt.Errorf("tendril: rent response: %w", err)
	}
	if err := json.Unmarshal(blob, &lease); err != nil {
		return lease, fmt.Errorf("tendril: rent response: %w", err)
	}
	if lease.LeaseID == "" || lease.LeaseToken == "" {
		// Money has already moved at this point, so say so loudly rather than
		// returning a lease-shaped zero value.
		return lease, fmt.Errorf("tendril: rent settled but returned no lease: %s", truncateJSON(blob))
	}
	return lease, nil
}

func truncateJSON(b []byte) string {
	if len(b) > 400 {
		return string(b[:400])
	}
	return string(b)
}

// payTendril runs one paid Tendril call through the EXISTING relay path by
// synthesizing a tool402 node for it. Nothing about payment is reimplemented
// here: ExecuteTool402V2 probes the 402, picks the group-vs-single signer off
// extra.feePayer, settles through Wallet 1 -> Wallet 2 -> Tendril, and bills
// the user's credit balance through cfg.Relay's ledger exactly as a normal
// x402 tool call does.
//
// bearer, when set, is the Tendril lease token the TARGET needs (for /x402/run
// against a machine the user already holds). It is not auth for our own relay
// — see the X-Relay-Auth passthrough added in Task 10.
func payTendril(ctx context.Context, cfg TendrilConfig, path string, body []byte, bearer string) (any, error) {
	node := models.WorkflowNode{
		ID:       "tendril:" + path,
		Type:     models.NodeTypeTool402,
		Endpoint: strings.TrimRight(cfg.Client.BaseURL(), "/") + path,
		Method:   http.MethodPost,
	}
	if len(body) > 0 {
		node.BodyMode = models.BodyModeJSON
		node.BodyTemplate = string(body)
	}
	if bearer != "" {
		node.TendrilLeaseToken = bearer
	}
	res, err := ExecuteTool402V2(ctx, node, emptyRunContext{}, models.AgentWallet{}, nil, cfg.Relay)
	if err != nil {
		return nil, fmt.Errorf("tendril %s: %w", path, err)
	}
	return res.Response, nil
}

// emptyRunContext satisfies RunContexter for the synthesized nodes above,
// whose bodies are fully specified by BodyTemplate and must never pick up the
// run's free-text trigger message.
type emptyRunContext struct{}

func (emptyRunContext) Message() string             { return "" }
func (emptyRunContext) UserInput() string           { return "" }
func (emptyRunContext) ToolOutputs() map[string]any { return nil }
func (emptyRunContext) Set(string, any)             {}
func (emptyRunContext) Get(string) (any, bool)      { return nil, false }

// ReleaseLease stops the meter and records what Tendril actually charged.
// Shared by the release node, the REST endpoint, and the reaper so all three
// bill identically.
func ReleaseLease(ctx context.Context, cfg TendrilConfig, lease models.TendrilLease) (tendril.ReleaseResult, error) {
	token, err := wallet.Decrypt(lease.LeaseTokenEnc, cfg.EncryptKey)
	if err != nil {
		return tendril.ReleaseResult{}, fmt.Errorf("tendril: decrypt lease token: %w", err)
	}
	res, err := cfg.Client.Release(ctx, lease.LeaseID, token)
	if err != nil {
		// Tendril's own watchdog reaps abandoned leases, so a lease it no
		// longer knows about is already stopped and already billed. Treating
		// that as a failure would leave our row 'active' forever and have the
		// reaper retry it on every tick.
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "not found") {
			if merr := cfg.Store.MarkTendrilLeaseReleased(ctx, lease.ID, 0, 0); merr != nil {
				return tendril.ReleaseResult{}, merr
			}
			return tendril.ReleaseResult{}, nil
		}
		return tendril.ReleaseResult{}, fmt.Errorf("tendril: release: %w", err)
	}
	if err := cfg.Store.MarkTendrilLeaseReleased(ctx, lease.ID, res.UsedSeconds, res.ChargedAtomic); err != nil {
		return res, err
	}

	// Return the unused part of the reservation as Tendril credit — not as
	// AgentMesh credit. The user bought hours; releasing early means they
	// still hold those hours, just not on this machine. Refunding to AgentMesh
	// credits instead would let a user cycle rent/release to convert Tendril
	// credit back into general platform credit, which the pool cannot honour
	// (the USDC is already sitting at Tendril).
	if unused := lease.ReservedUSDMicros - res.ChargedAtomic; unused > 0 {
		if err := cfg.Store.ChargeTendrilCredit(ctx, lease.UserID, lease.ID, "refund", unused); err != nil {
			// The lease is already closed and billed; a failed refund is a
			// reconciliation problem, not a reason to report the release as
			// failed and have the reaper retry a DELETE that already ran.
			log.Printf("tendril: lease %s released but refunding %d micros to user %s failed: %v",
				lease.LeaseID, unused, lease.UserID, err)
		}
	}
	return res, nil
}

func executeTendrilRelease(ctx context.Context, node models.WorkflowNode, cfg TendrilConfig) (any, error) {
	lease, err := resolveLease(ctx, node, cfg)
	if err != nil {
		return nil, err
	}
	res, err := ReleaseLease(ctx, cfg, lease)
	if err != nil {
		return nil, err
	}
	remaining, _ := cfg.Store.TendrilCreditBalance(ctx, lease.UserID)
	return map[string]any{
		"agentMeshLeaseId": lease.ID,
		"leaseId":          lease.LeaseID,
		"usedSeconds":      res.UsedSeconds,
		"charged":          formatUSDCAmount(res.ChargedAtomic),
		"refunded":         formatUSDCAmount(max64(0, lease.ReservedUSDMicros-res.ChargedAtomic)),
		// Deliberately NOT res.Balance: that is the shared pool, which is
		// every user's money and must never be shown to one of them.
		"tendrilCreditBalance": formatUSDCAmount(remaining),
	}, nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// resolveLease finds which lease a run/release node acts on. A node may name
// one explicitly via TendrilNodeID; otherwise it takes the lease this run
// opened, which is what the trigger -> rent -> run -> release workflow wants.
func resolveLease(ctx context.Context, node models.WorkflowNode, cfg TendrilConfig) (models.TendrilLease, error) {
	if node.TendrilNodeID != "" {
		return cfg.Store.GetTendrilLease(ctx, node.TendrilNodeID)
	}
	lease, err := cfg.Store.LatestActiveLeaseForRun(ctx, cfg.RunID)
	if err != nil {
		return models.TendrilLease{}, fmt.Errorf("tendril: no lease to act on — put a Rent node before this one: %w", err)
	}
	return lease, nil
}

func executeTendrilRun(ctx context.Context, node models.WorkflowNode, rc RunContexter, cfg TendrilConfig) (any, error) {
	payload := strings.TrimSpace(rc.Message())
	for _, p := range node.CustomParams {
		if p.Name == "payload" && strings.TrimSpace(p.Value) != "" {
			payload = p.Value
		}
	}
	if payload == "" {
		return nil, fmt.Errorf("tendril: run needs a payload — set one on the node or pass it as the run's input")
	}

	// A lease is optional: with one, the job runs inside the machine the user
	// is already paying for; without one, Tendril picks an idle machine and
	// bills the seconds. Both are the same paid endpoint.
	body, _ := json.Marshal(map[string]string{"payload": payload})
	var leaseToken string
	if lease, err := resolveLease(ctx, node, cfg); err == nil && lease.LeaseTokenEnc != "" {
		if tok, derr := wallet.Decrypt(lease.LeaseTokenEnc, cfg.EncryptKey); derr == nil {
			leaseToken = tok
		}
	}
	return payTendril(ctx, cfg, "/x402/run", body, leaseToken)
}
