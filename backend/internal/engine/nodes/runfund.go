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
	// BaseURL is our own backend's real, reachable origin (BASE_URL) --
	// combined with the real /x402/relay/run-funding path below for
	// PaymentRequirements.Resource and PaymentPayload.Resource. Was
	// FrontendURL (a separate marketing domain) until confirmed live
	// 2026-08-01 that every actually-cataloged mainnet resource on the real
	// facilitator uses its OWN API domain+path as resource.url, never a
	// separate marketing site -- our FrontendURL-based declaration was
	// schema-perfect and still never once appeared in
	// /discovery/resources across multiple real settlements.
	BaseURL string
}

// FundRunReserve settles one real GoPlausible payment for amountUSDMicros
// from the platform's Wallet 1 spend wallet into Wallet 2
// (cfg.PlatformWalletAddress) — same payTo as every per-call relay
// settlement, so leaderboard attribution (keyed on payTo, not resource) is
// unaffected. Resource points at cfg.BaseURL's real run-funding route on our
// own domain, rather than an opaque identifier string or a separate
// marketing domain.
// amountUSDMicros <= 0 is a no-op (an agent with no attached tool402 nodes,
// or all-legacy-dialect ones, needs no pre-fund at all).
func FundRunReserve(ctx context.Context, cfg RunPreFundConfig, runID string, amountUSDMicros int64) (string, error) {
	if amountUSDMicros <= 0 {
		return "", nil
	}

	description := "AgentMesh workflow run funding — pre-settled pool for this run's downstream x402 tool calls"
	resourceURL := cfg.BaseURL + "/x402/relay/run-funding"
	reqs := x402.PaymentRequirements{
		Scheme:            "exact",
		Network:           cfg.RelayNetwork,
		MaxAmountRequired: strconv.FormatInt(amountUSDMicros, 10),
		// See RunPreFundConfig.BaseURL's doc comment.
		Resource:          resourceURL,
		Description:       description,
		MimeType:          "application/json",
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
		// Schema-valid shape (info.input.type/method + a schema sibling) --
		// see x402relay.go's bazaarDiscoveryExtension doc comment for why
		// the {info:{output:{...}}} shape this had before this fix failed
		// the facilitator's ajv validation unconditionally (no schema at
		// all, no info.input.type) and so never once cataloged, even though
		// verify/settle both succeeded and real money moved every time.
		Extensions: map[string]any{
			"bazaar": map[string]any{
				// See x402relay.go's bazaarDiscoveryExtension doc comment on
				// routeTemplate -- same reasoning, this route's path never
				// varies either.
				"routeTemplate": "/x402/relay/run-funding",
				"info": map[string]any{
					"input": map[string]any{"type": "http", "method": "GET"},
				},
				"schema": map[string]any{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"type":    "object",
					"properties": map[string]any{
						"input": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"type":   map[string]any{"type": "string", "const": "http"},
								"method": map[string]any{"type": "string", "enum": []string{"GET", "HEAD", "DELETE"}},
							},
							"required":             []string{"type", "method"},
							"additionalProperties": false,
						},
					},
					"required": []string{"input"},
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
		// Set authoritatively regardless of the fact that WE are both payer
		// and resource server here -- same reasoning as x402relay.go's
		// relaySelfSettle/relaySettleAndForward: the facilitator's discovery
		// extraction reads resource/extensions off the PAYLOAD, not off
		// PaymentRequirements above, so without these two fields this
		// settlement (real money, real facilitator round trip) still had
		// nothing for the catalog to key off, exactly like every other
		// settlement path did before its own matching fix today.
		Resource:   map[string]any{"url": resourceURL, "description": description, "mimeType": "application/json", "serviceName": "AgentMesh", "tags": []string{"x402-global-challenge"}},
		Extensions: reqs.Extensions,
		// Required field of the v2 payload schema -- see
		// x402.PaymentPayload.Accepted's doc comment. This path already set
		// a positive maxTimeoutSeconds on reqs (unlike x402relay.go's two
		// settle paths, which sent zero until this fix), so the projection
		// below is schema-valid as-is.
		Accepted: reqs.AcceptedV2(),
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
