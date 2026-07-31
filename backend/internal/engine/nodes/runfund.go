package nodes

import (
	"context"
	"fmt"
	"net/url"
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
		Resource:          cfg.PublicBaseURL + "/x402/relay/run-funding?runId=" + url.QueryEscape(runID),
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
		// Bazaar discovery declaration on the struct actually POSTed to
		// /verify — extra.tag alone only attributes an already-discovered
		// route's activity to the challenge, it doesn't register the route.
		// Fixed resource/schema here (no pass-through target like the relay
		// has) since this endpoint always settles the same shape of payment.
		Extensions: map[string]any{
			"bazaar": map[string]any{
				"info": map[string]any{
					"output": map[string]any{"description": "confirms this run's pre-fund pool was reserved"},
				},
			},
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
		// Response never arrived -- settlement's fate is unknown, not
		// "failed". Wrapped so reserveAndFundRun's caller can tell this
		// apart from a definitive rejection and avoid releasing a
		// reservation for money that may have already moved.
		return "", fmt.Errorf("run pre-fund: facilitator settle response lost: %v: %w", err, ErrSettlementIndeterminate)
	}
	if !settleResult.Success {
		// A real, received response says it failed -- money definitively
		// never moved.
		return "", fmt.Errorf("run pre-fund: settlement failed: %s", settleResult.Error)
	}
	return settleResult.TxID, nil
}
