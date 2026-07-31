package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"

	"github.com/agentmesh/backend/internal/db"
	"github.com/agentmesh/backend/internal/engine/nodes"
	"github.com/agentmesh/backend/internal/respond"
	"github.com/agentmesh/backend/internal/x402"
)

// X402Relay is the orchestrator's own paid endpoint. It has no fixed price:
// the price it charges the caller is whatever the target endpoint (given via
// ?target=) actually charges. This is what makes the relay generic across
// every x402 endpoint in the GoPlausible marketplace, not just a fixed set.
//
// Flow: no X-Payment header -> fetch target's real 402, mirror it back as our
// own v2/USDC/tagged challenge (payTo = platform wallet). X-Payment present ->
// verify+settle the inbound payment via the facilitator (credited to us),
// then pay the target from the platform wallet (credited to them), then
// relay the target's paid response back to the caller.
func (d *Deps) X402Relay(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("target")
	if target == "" {
		respond.Error(w, http.StatusBadRequest, "target query param required")
		return
	}
	// target is caller-supplied and this route is public/unauthenticated —
	// without this check, callers could make the relay fetch or pay
	// arbitrary internal/private addresses (SSRF). Same guard applied to
	// every tool402 node's target before Task 6 wires it through here.
	if err := nodes.ValidateURL(target); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid target: "+err.Error())
		return
	}

	// X-Relay-Method/X-Relay-Body tell the relay what to send to TARGET (not
	// how the caller reaches the relay itself -- that's always this same GET
	// with ?target=, unchanged). Real x402 endpoints are not guaranteed to
	// be GET-compatible (a POST-only resource can 404 a bare GET before it
	// ever considers payment state), so the relay needs to know. Body goes
	// in a header, base64-encoded, rather than the query string: request
	// bodies can exceed URLs' practical length limits, while headers hold
	// far more (net/http's default MaxHeaderBytes is 1MB) — same pattern
	// X-Payment and Payment-Required already use in this file.
	targetMethod := r.Header.Get("X-Relay-Method")
	if targetMethod == "" {
		targetMethod = http.MethodGet
	}
	switch targetMethod {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		respond.Error(w, http.StatusBadRequest, "unsupported X-Relay-Method: "+targetMethod)
		return
	}
	var targetBody []byte
	if b64 := r.Header.Get("X-Relay-Body"); b64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			respond.Error(w, http.StatusBadRequest, "invalid X-Relay-Body: "+err.Error())
			return
		}
		targetBody = decoded
	}

	xPayment := r.Header.Get("X-Payment")
	if xPayment == "" {
		d.relayInboundChallenge(w, r, target, targetMethod, targetBody)
		return
	}
	d.relaySettleAndForward(w, r, target, xPayment, targetMethod, targetBody)
}

// X402RunFundingInfo is the static, informational resource FundRunReserve's
// PaymentRequirements.Resource points at — a real, reachable route on our own
// domain rather than an opaque identifier string. No payment logic, no auth:
// purely informational, matching what a real Bazaar-catalog crawler would
// expect to find at a `resource` URL.
func (d *Deps) X402RunFundingInfo(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, http.StatusOK, map[string]string{
		"description": "AgentMesh workflow run funding pool — internal pre-settlement for downstream x402 tool calls, not directly payable via this route",
	})
}

// targetPriceQuote is the subset of a target's x402 402 response the relay
// cares about.
type targetPriceQuote struct {
	PayTo             string
	Asset             string
	MaxAmountRequired string
}

// fetchTargetPriceQuote issues an unauthenticated GET to the caller-supplied
// target (via the SSRF-safe shared client, which also enforces a 10s dial+
// request timeout — see nodes.toolHTTPClient) and parses its x402 402
// challenge.
//
// This is called independently from two places: relayInboundChallenge (to
// mirror the price to the caller on the no-payment leg) and
// relaySettleAndForward (to learn the authoritative price to enforce and
// record before settling the inbound payment). Those two calls are separate,
// unrelated HTTP requests (no-payment challenge vs. with-payment settle) with
// no shared state between them, so the price genuinely has to be re-fetched
// across that boundary rather than trusted from the earlier call.
//
// Deliberately NOT called a second time from payTargetAndRespond: target is
// caller-supplied and this route is public/unauthenticated, so re-fetching a
// second, independent quote at pay-time would let a malicious target answer
// cheaply the first time and expensively (and/or to a different payTo) the
// second time, draining the platform wallet for more than was ever collected
// from the caller. relaySettleAndForward fetches the quote exactly once per
// relay cycle and passes that same value into payTargetAndRespond.
func fetchTargetPriceQuote(ctx context.Context, target, method string, body []byte) (targetPriceQuote, error) {
	var bodyReader io.Reader
	if method != http.MethodGet && len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bodyReader)
	if err != nil {
		return targetPriceQuote{}, err
	}
	if bodyReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := nodes.SafeHTTPClient().Do(req)
	if err != nil {
		return targetPriceQuote{}, err
	}
	defer resp.Body.Close()

	var parsed struct {
		Accepts []map[string]any `json:"accepts"`
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	json.Unmarshal(respBody, &parsed)
	if len(parsed.Accepts) == 0 {
		// Body carried no accepts[] — some real targets (Prism's live
		// endpoint confirmed 2026-07-31) put the full challenge in the
		// Payment-Required header instead and leave the body empty/minimal.
		parsed.Accepts = nodes.ChallengeAcceptsFromHeader(resp.Header)
	}
	if len(parsed.Accepts) == 0 {
		return targetPriceQuote{}, fmt.Errorf("target did not return a valid x402 challenge")
	}
	accept := parsed.Accepts[0]
	payTo, _ := accept["payTo"].(string)
	asset, _ := accept["asset"].(string)
	// Accept both the usual JSON-string encoding and a JSON-number encoding
	// of maxAmountRequired — some real targets encode this field as a
	// number rather than a string, which a string-only type assertion would
	// otherwise reject as an empty/invalid quote (see
	// nodes.ParseMaxAmountRequiredAsMicros). Also accept `amount` as a
	// fallback field name when `maxAmountRequired` is absent/unparseable —
	// the CURRENT real-world x402 dialect (Prism's live endpoint, the
	// official @x402/core v2.20 SDK, confirmed live 2026-07-31) uses `amount`
	// instead; `maxAmountRequired` is only checked first because it's this
	// codebase's own historical convention, not because it's more correct.
	// A parse failure on both still yields an empty MaxAmountRequired, which
	// the callers below already turn into an error via strconv.ParseInt.
	var amount string
	micros, ok := nodes.ParseMaxAmountRequiredAsMicros(accept["maxAmountRequired"])
	if !ok {
		micros, ok = nodes.ParseMaxAmountRequiredAsMicros(accept["amount"])
	}
	if ok {
		amount = strconv.FormatInt(micros, 10)
	}
	return targetPriceQuote{PayTo: payTo, Asset: asset, MaxAmountRequired: amount}, nil
}

// relayInboundChallenge fetches the target's real 402 price and mirrors it
// back as our own v2 challenge, tagged for the challenge and paid to our
// platform wallet instead of the target's.
func (d *Deps) relayInboundChallenge(w http.ResponseWriter, r *http.Request, target, targetMethod string, targetBody []byte) {
	quote, err := fetchTargetPriceQuote(r.Context(), target, targetMethod, targetBody)
	if err != nil {
		respond.Error(w, http.StatusBadGateway, "target fetch failed: "+err.Error())
		return
	}
	// fetchTargetPriceQuote returns MaxAmountRequired == "" when neither the
	// maxAmountRequired nor amount field parsed -- relaySettleAndForward
	// catches this later via strconv.ParseInt, but this path (the public,
	// unauthenticated challenge preview -- what a Bazaar crawler actually
	// hits) had no equivalent check, so it would happily emit a 402 with an
	// empty price instead of a clear error.
	if quote.MaxAmountRequired == "" {
		respond.Error(w, http.StatusBadGateway, "target returned an unparseable or missing price quote")
		return
	}

	challenge := map[string]any{
		"x402Version": 2,
		"accepts": []map[string]any{{
			"scheme":            "exact",
			"network":           d.RelayNetwork,
			"maxAmountRequired": quote.MaxAmountRequired,
			"resource":          target,
			"description":       "AgentMesh x402 relay — settles the inbound leg and forwards payment to " + target,
			"payTo":             d.PlatformWalletAddress,
			"maxTimeoutSeconds": 300,
			"asset":             strconv.FormatUint(d.USDCAssetID, 10),
			"extra": map[string]any{
				"asset":    strconv.FormatUint(d.USDCAssetID, 10),
				"feePayer": d.RelayFeePayer,
				"tag":      "x402-global-challenge",
				"decimals": 6,
			},
		}},
		// Bazaar discovery metadata: describes the relay's pass-through
		// shape (any downstream target URL in, that target's own response
		// out) since this endpoint has no fixed schema of its own — see
		// GoPlausible's bazaar-integration reference server.
		"extensions": map[string]any{
			"bazaar": map[string]any{
				"info": map[string]any{
					"input":  map[string]any{"target": target},
					"output": map[string]any{"description": "the target endpoint's own paid response, forwarded unmodified"},
				},
				"schema": map[string]any{
					"properties": map[string]any{
						"target": map[string]any{"type": "string", "description": "downstream x402 endpoint URL this relay settles payment for and forwards to"},
					},
				},
			},
		},
	}

	challengeJSON, err := json.Marshal(challenge)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to encode challenge")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	// Bazaar's discovery crawler reads the challenge off this header (base64
	// JSON), not just the body — see GoPlausible's bazaar-integration
	// reference server verification step. Cheap to set both.
	w.Header().Set("Payment-Required", base64.StdEncoding.EncodeToString(challengeJSON))
	w.WriteHeader(http.StatusPaymentRequired)
	w.Write(challengeJSON)
}

// relaySettleAndForward verifies+settles the caller's inbound payment, then
// pays the real target from the platform wallet, then relays the target's
// paid response back. Both settlements are real, GoPlausible-facilitated,
// mainnet payments — this is what earns orchestrator-entry attribution.
func (d *Deps) relaySettleAndForward(w http.ResponseWriter, r *http.Request, target, xPaymentHeader, targetMethod string, targetBody []byte) {
	ctx := r.Context()

	var payload x402.PaymentPayload
	if err := json.Unmarshal([]byte(xPaymentHeader), &payload); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid X-Payment payload")
		return
	}

	// Re-fetch the target's own 402 to learn the authoritative current
	// price. This is what lets us set MaxAmountRequired below (so the
	// facilitator actually enforces the quoted price instead of trusting
	// whatever the caller's payment payload claims) and what lets us record
	// the real settled amount in the ledger instead of a hardcoded 0.
	quote, err := fetchTargetPriceQuote(ctx, target, targetMethod, targetBody)
	if err != nil {
		respond.Error(w, http.StatusBadGateway, "target fetch failed: "+err.Error())
		return
	}
	amountAssetMicros, err := strconv.ParseInt(quote.MaxAmountRequired, 10, 64)
	if err != nil {
		respond.Error(w, http.StatusBadGateway, "target returned invalid maxAmountRequired: "+err.Error())
		return
	}

	// quote.Asset comes straight from the target's own (caller-supplied,
	// unauthenticated) 402 response and is only ever used later, for the
	// OUTBOUND leg to target — the inbound leg below always settles in
	// d.USDCAssetID regardless of what the target claims. Checked here,
	// before ever verifying/settling the inbound payment, so a target that
	// can never be paid (wrong/unsupported asset) is rejected before the
	// caller's inbound payment is collected at all — settling inbound first
	// and only discovering this in payTargetAndRespond would already have
	// set X-Inbound-Settled, billing the caller in full for a call that was
	// never going to reach the target.
	if quoteAssetID, err := strconv.ParseUint(quote.Asset, 10, 64); err != nil || quoteAssetID != d.USDCAssetID {
		respond.Error(w, http.StatusBadGateway, "target quoted an unexpected asset id")
		return
	}

	reqs := x402.PaymentRequirements{
		Scheme:            "exact",
		Network:           d.RelayNetwork,
		PayTo:             d.PlatformWalletAddress,
		Asset:             strconv.FormatUint(d.USDCAssetID, 10),
		MaxAmountRequired: quote.MaxAmountRequired,
	}

	verifyResult, err := d.FacilitatorClient.Verify(ctx, payload, reqs)
	if err != nil {
		respond.Error(w, http.StatusBadGateway, "facilitator verify failed: "+err.Error())
		return
	}
	if !verifyResult.IsValid {
		respond.Error(w, http.StatusPaymentRequired, "payment invalid: "+verifyResult.Invalid)
		return
	}

	settleResult, err := d.FacilitatorClient.Settle(ctx, payload, reqs)
	if err != nil {
		respond.Error(w, http.StatusBadGateway, "facilitator settle failed: "+err.Error())
		return
	}
	if !settleResult.Success {
		respond.Error(w, http.StatusPaymentRequired, "settlement failed: "+settleResult.Error)
		return
	}

	ledgerRow, err := d.Store.RecordInboundSettlement(ctx, target, settleResult.TxID, amountAssetMicros)
	if err == db.ErrDuplicateSettlement {
		respond.Error(w, http.StatusConflict, "payment already processed")
		return
	}
	if err != nil {
		log.Printf("x402 relay: failed to record inbound settlement: %v", err)
		respond.Error(w, http.StatusInternalServerError, "internal error recording settlement")
		return
	}

	d.payTargetAndRespond(w, r, target, ledgerRow.ID, settleResult.TxID, quote, targetMethod, targetBody)
}

// payTargetAndRespond pays the real target from the platform wallet via the
// facilitator, then relays the target's paid response back to the caller.
// No refund path on failure: x402 has no chargeback primitive, and the
// inbound leg's attribution to us already landed regardless of this outcome.
//
// It takes the already-fetched targetPriceQuote from relaySettleAndForward
// (its only caller) rather than re-fetching the caller-supplied target
// itself. This is deliberate: target is arbitrary and attacker-controlled on
// this public, unauthenticated route, so re-fetching it a second time here
// would let a malicious target answer the first (enforcement/recording)
// fetch cheaply and this second (pay-time) fetch with an inflated amount
// and/or a different payTo — causing the platform wallet to sign and
// broadcast a payment for more than was ever collected from the caller, to
// an address the caller chose. One quote, fetched once per relay cycle, used
// for both enforcement/recording and the actual outbound payment closes that
// gap.
//
// Outbound tx id: paying the target here goes over the target's own
// X-Payment header directly, not through our own FacilitatorClient, so there
// is no SettleResult (and therefore no facilitator-issued transaction id) on
// this leg — the target's paid HTTP response carries no standardized txid
// reference either. RecordOutboundSettlement is called with an empty
// outbound tx id below; that is a real gap in observability given the
// current architecture (the relay pays the target directly rather than via
// a second facilitator round-trip from our side), not an oversight, and not
// something to paper over with a fabricated id.
func (d *Deps) payTargetAndRespond(w http.ResponseWriter, r *http.Request, target, ledgerID, inboundTxID string, quote targetPriceQuote, targetMethod string, targetBody []byte) {
	ctx := r.Context()

	cfg := nodes.Wallet2PayConfig{
		USDCSigner:                d.USDCSigner,
		PlatformWalletEncMnemonic: d.PlatformWalletEncMnemonic,
		USDCAssetID:               d.USDCAssetID,
		RelayFeePayer:             d.RelayFeePayer,
		RelayNetwork:              d.RelayNetwork,
		MaxRelayOutboundUSDMicros: d.MaxRelayOutboundUSDMicros,
	}
	result, err := nodes.PayTargetFromWallet2(ctx, cfg, target, targetMethod, targetBody, nodes.TargetQuote{
		PayTo: quote.PayTo, Asset: quote.Asset, MaxAmountRequired: quote.MaxAmountRequired,
	})

	// Signals to the orchestrator's own tool402 caller (tool402.go) that the
	// inbound leg (Wallet 1 -> Wallet 2, via the facilitator in
	// relaySettleAndForward) has irreversibly settled AND a real signed
	// outbound payment group now exists, independent of whatever the
	// target's HTTP response says. result.Signed becomes true the instant
	// PayTargetFromWallet2's SignUSDCPaymentGroup call succeeds -- the exact
	// moment this handler used to set the header inline, before its own
	// paid request to target. A signing failure (bad payTo, algod outage,
	// ...) means the target receives nothing at all -- billing the caller
	// in that case would be a real over-charge, not the "money already
	// moved so it's fair to bill" case this header exists to represent.
	// Once a group is signed, it's a submittable claim regardless of what
	// the target's HTTP response says, which is what makes billing on this
	// flag (not on the target's status code) safe: a target that accepts
	// payment and then deliberately returns non-2xx must not be able to
	// dodge billing while still being paid. Must be set before any
	// WriteHeader call below.
	if result.Signed {
		w.Header().Set("X-Inbound-Settled", "true")
		// Carries the real, already-irreversible inbound facilitator tx id
		// out to the caller (tool402.go's executeTool402V2Relay) purely for
		// display -- e.g. surfacing it in a workflow run's console log. Set
		// alongside X-Inbound-Settled, under the same "money already moved"
		// condition, since that's the only point at which this id is
		// meaningful to show.
		w.Header().Set("X-Settlement-TxId", inboundTxID)
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

	// The target's paid response must actually succeed for the outbound leg
	// to count as settled — a 402/4xx/5xx here means the platform wallet's
	// payment was rejected (or the target errored) despite the inbound leg
	// already being collected. Relay the target's real status/body back to
	// the caller either way (no refund path), but only record "settled"
	// when the target actually accepted the payment.
	//
	// See the empty outbound-tx-id note in the function doc comment above:
	// there is no facilitator-issued outbound transaction id available at
	// this call site with the current design.
	status := "settled"
	if !result.Settled {
		status = "failed"
	}
	d.Store.RecordOutboundSettlement(ctx, ledgerID, "", status)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(result.StatusCode)
	w.Write(result.ResponseBody)
}
