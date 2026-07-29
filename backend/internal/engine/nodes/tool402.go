package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/agentmesh/backend/internal/alert"
	"github.com/agentmesh/backend/internal/models"
)

// relayHTTPClient is used only for the two calls to our own /x402/relay
// endpoint (the quote fetch and the paid retry). It shares toolHTTPClient's
// SSRF-safe dialer (dialFn, tool.go) but needs a longer timeout: the relay
// handler's own worst-case latency for a single request can exceed
// toolHTTPClient's 10s budget by itself — up to ~10s re-fetching the
// target's quote, up to 20s each for the facilitator's Verify and Settle
// calls (internal/x402/facilitator.go), plus the outbound payment request
// to target (another ~10s budget). A caller-side timeout shorter than the
// callee's own worst case means "the orchestrator gave up waiting" and
// "the inbound leg genuinely never settled" become indistinguishable from
// a transport error, which is unsafe to resolve either way: assuming
// settlement risks billing for a payment that never happened, and assuming
// no settlement risks never billing for one that did (the underlying
// vector the X-Inbound-Settled header exists to close). Sized with
// headroom above the relay's own worst case rather than exactly matching
// it, so ordinary slow-but-real responses aren't cut off right at the
// boundary.
var relayHTTPClient = &http.Client{
	Timeout: 90 * time.Second,
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialFn(ctx, network, addr)
		},
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if err := validateURL(req.URL.String()); err != nil {
			return err
		}
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	},
}

// WalletSigner signs and submits an Algorand payment transaction.
// Satisfied by *wallet.Service.
type WalletSigner interface {
	SignAndSendPayment(ctx context.Context, encMnemonic, toAddress string, microAlgo uint64) (string, error)
}

func QuoteX402(ctx context.Context, rawURL string) (map[string]any, error) {
	if err := urlValidator(rawURL); err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Always attempt to parse payment info from header+body regardless of status.
	// Proxies (Cloudflare tunnels) may rewrite 402 → 200/503 or strip headers.
	quote := parsePaymentHeader(resp)
	if _, hasPrice := quote["price"]; hasPrice {
		return quote, nil
	}
	return map[string]any{"price": "0", "unit": "", "network": "", "recipient": ""}, nil
}

func ExecuteTool402(ctx context.Context, node models.WorkflowNode, rc RunContexter, wallet models.AgentWallet, signer WalletSigner) (any, error) {
	if err := urlValidator(node.Endpoint); err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, node.Endpoint, nil)
	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusPaymentRequired {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, httpResponseLimit))
		var result any
		if json.Unmarshal(b, &result) == nil {
			return result, nil
		}
		return string(b), nil
	}

	quote := parsePaymentHeader(resp) // reads body internally
	resp.Body.Close()

	if wallet.EncryptedMnemonic == "" || signer == nil {
		return map[string]any{"error": "payment required but no agent wallet configured", "quote": quote}, nil
	}

	priceStr, _ := quote["price"].(string)
	recipient, _ := quote["recipient"].(string)
	if recipient == "" {
		return nil, fmt.Errorf("x402: no recipient address in payment header")
	}
	priceFloat, err := strconv.ParseFloat(priceStr, 64)
	if err != nil || priceFloat <= 0 {
		return nil, fmt.Errorf("x402: invalid price %q", priceStr)
	}
	microAlgo := uint64(priceFloat * 1e6)

	txID, err := signer.SignAndSendPayment(ctx, wallet.EncryptedMnemonic, recipient, microAlgo)
	if err != nil {
		return nil, fmt.Errorf("x402 payment failed: %w", err)
	}

	algoAmount := fmt.Sprintf("%.6f", float64(microAlgo)/1e6)
	explorerURL := "https://lora.algokit.io/testnet/transaction/" + txID

	// Retry the original request with the payment proof header.
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, node.Endpoint, nil)
	req2.Header.Set("X-Payment-Txid", txID)
	resp2, err := toolHTTPClient.Do(req2)
	if err != nil {
		return map[string]any{"status": "payment_sent", "txId": txID, "amount": algoAmount, "explorerURL": explorerURL, "error": "retry request failed: " + err.Error()}, nil
	}
	defer resp2.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp2.Body, httpResponseLimit))
	var retryResult any
	if json.Unmarshal(b, &retryResult) == nil {
		return map[string]any{"status": "payment_sent", "txId": txID, "amount": algoAmount, "explorerURL": explorerURL, "response": retryResult}, nil
	}
	return map[string]any{"status": "payment_sent", "txId": txID, "amount": algoAmount, "explorerURL": explorerURL, "response": string(b)}, nil
}

func parsePaymentHeader(resp *http.Response) map[string]any {
	// Try header first (direct connections). Cloudflare and other proxies may
	// strip non-standard response headers, so fall back to the response body.
	header := resp.Header.Get("X-Payment-Required")
	if header == "" {
		header = resp.Header.Get("WWW-Authenticate")
	}
	var result map[string]any
	if header != "" {
		if err := json.Unmarshal([]byte(header), &result); err == nil {
			return result
		}
	}
	// Body fallback: our server returns {"error":"Payment required","payment":{...}}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, httpResponseLimit))
	var envelope struct {
		Payment map[string]any `json:"payment"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Payment != nil {
		return envelope.Payment
	}
	// Last resort: try parsing body directly as the payment object
	if err := json.Unmarshal(body, &result); err == nil {
		return result
	}
	return map[string]any{"raw": header}
}

// USDCGroupSigner signs a gasless USDC atomic-payment group for the relay's
// X-Payment header. Satisfied by *wallet.Service (SignUSDCPaymentGroup).
type USDCGroupSigner interface {
	SignUSDCPaymentGroup(ctx context.Context, encMnemonic, payTo string, assetID, amountMicros uint64, feePayerAddr string) ([]string, int, error)
}

// X402RelayConfig bundles what an agent-attached tool402 call needs to route
// through the AgentMesh relay (Wallet 1) exactly the way a standalone
// tool402 node already does via ExecuteTool402V2. Threaded from Runner
// through ExecuteAgent -> callOpenAICompat/callGemini -> executeFunctionCall.
type X402RelayConfig struct {
	USDCSigner               USDCGroupSigner
	PlatformSpendEncMnemonic string
	ExpectedAssetID          uint64
	RelayBaseURL             string
	// Ledger reserves/commits/releases credits for real on-chain tool402
	// payments made through this config. For a run-funded agent this is
	// swapped to the in-memory run-level pool (Task 5) and is ONLY ever
	// used for v2-dialect dispatch (executeTool402V2Relay when
	// RunFundingID == "", executeTool402RunLevel when it isn't) — the
	// legacy-dialect branch below must never read this field directly; see
	// LegacyLedger.
	Ledger RunLedger
	// LegacyLedger is the original per-call, DB-backed ledger (always
	// r.newPaymentLedger(wf, run), never the run-level in-memory pool) —
	// what the legacy flat-quote dialect's direct-pay branch reserves/
	// commits/releases its flat fee against, regardless of whether Ledger
	// above has been swapped to the run-level pool for this same agent's v2
	// tools. Legacy-dialect billing must be identical whether or not the
	// agent also happens to have a run-funded v2 tool attached — reading
	// Ledger here instead would decrement a pool sized only for v2 quotes,
	// spuriously blocking legacy calls or committing them against credits
	// that were already converted into a real on-chain settlement to
	// Wallet 2 for an unrelated call.
	LegacyLedger CallLedger

	// RunFundingID is set (non-empty) the moment the agent's run has already
	// settled a single lump-sum inbound x402 payment covering its attached
	// v2 tool402 nodes (Task 5's reserveAndFundRun) — a property of the RUN,
	// not of any one node's dialect. "" means no run-level pre-fund happened
	// (no attached v2 tools, or the estimate came back 0), so v2 calls keep
	// taking the existing per-call public-relay path unchanged.
	RunFundingID string
	// RunFundedToolIDs is the set of attached tool402 node IDs that
	// reserveAndFundRun confirmed as real v2 targets and folded into
	// RunFundingID's up-front reservation — empty/nil when RunFundingID is
	// "". A legacy-dialect tool attached to the same run-funded agent is
	// never in this set (reserveAndFundRun's estimator skips it), so
	// provider.go's pre-flight floor guard uses this, not a blanket
	// RunFundingID != "" check, to decide whether a given attached tool402
	// node's own DB balance still needs checking before its first outbound
	// HTTP call.
	RunFundedToolIDs map[string]bool
	// Wallet2 carries what's needed to pay a real target directly from
	// Wallet 2, in-process, once RunFundingID is set. See Wallet2PayConfig.
	Wallet2 Wallet2PayConfig
	// RecordSettlement records one run-funded per-call settlement audit row
	// (x402_relay_settlements, run_funding_id-linked). amountUSDMicros must
	// be the real settled amount — RecordRunFundedSettlement takes it at
	// INSERT time since there is no later call that backfills it.
	RecordSettlement func(ctx context.Context, target string, amountUSDMicros int64, settled bool) error
}

// toolIsRunFunded reports whether toolID's real cost is already covered by
// this run's up-front lump-sum settlement -- true only when the run has a
// funding id AND reserveAndFundRun's estimator specifically confirmed this
// tool as a real v2 target it folded into that estimate. A run-funded
// agent with a tool whose probe failed during estimation (or a
// legacy-dialect tool attached alongside a funded v2 one) must NOT be
// treated as run-funded for that specific tool -- both call sites that used
// to hand-write this condition differently now share this one definition.
func (cfg X402RelayConfig) toolIsRunFunded(toolID string) bool {
	return cfg.RunFundingID != "" && cfg.RunFundedToolIDs[toolID]
}

// Tool402PaymentResult is what ExecuteTool402V2 returns. SettledUSDMicros
// and DebitKind describe a charge that has ALREADY been committed via the
// caller-supplied PaymentLedger by the time this returns — callers report
// these for logging/audit purposes and must not debit again. Both are zero
// when no payment was sent (e.g. the endpoint didn't require one, no wallet
// was configured, or a reservation was taken but released because the
// payment never actually settled).
type Tool402PaymentResult struct {
	Response         any
	SettledUSDMicros int64
	DebitKind        string
}

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
	amount, ok := ParseMaxAmountRequiredAsMicros(accept["maxAmountRequired"])
	// This is a real v2 challenge (accepts[] present) -- a missing or
	// unparseable maxAmountRequired here is a malformed challenge, not a
	// genuinely free tool. Silently returning MaxAmountRequired: 0 in that
	// case would be indistinguishable from a real zero-cost quote, and
	// Task 5 sizes both a credit reservation and a real on-chain payment
	// off this value -- a silent 0 there is a money-correctness bug. Report
	// it as an error instead; isV2 still reflects reality (it IS a v2
	// challenge) even though the quote itself is zero-valued.
	if !ok || amount <= 0 {
		return true, false, nil, x402Quote{}, fmt.Errorf("x402: invalid or missing maxAmountRequired %v in v2 challenge", accept["maxAmountRequired"])
	}
	return true, false, nil, x402Quote{PayTo: payTo, Asset: asset, MaxAmountRequired: amount}, nil
}

// ParseMaxAmountRequiredAsMicros parses a real x402 v2 challenge's
// accepts[].maxAmountRequired field, accepting either its usual JSON-string
// encoding or a JSON-number encoding — some real targets encode this field
// as a number rather than a string, and a string-only type assertion would
// otherwise reject an entirely valid quote. Returns false if neither shape
// parses to a value, or the value isn't a whole number of micros.
func ParseMaxAmountRequiredAsMicros(v any) (int64, bool) {
	switch t := v.(type) {
	case string:
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil || n < 0 {
			return 0, false
		}
		return n, true
	case float64:
		if t != math.Trunc(t) || t < 0 {
			return 0, false
		}
		return int64(t), true
	default:
		return 0, false
	}
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

// ExecuteTool402V2 is the entry point runner.go calls for tool402 nodes. It
// inspects the target's 402 quote shape: a real x402 v2 challenge (accepts[])
// is routed through the AgentMesh relay so both payment legs are real,
// GoPlausible-settled, and attributable to us as an orchestrator entry, paid
// from the platform's own Wallet 1 spend wallet and gated/charged against
// the triggering user's credits for the real settled amount. The legacy
// flat-quote dialect (no accepts[]) bypasses the relay entirely and keeps
// today's direct-pay-from-the-agent's-own-wallet behavior, gated/charged at
// the fixed platform fee — it was never GoPlausible-compliant and isn't
// becoming so.
func ExecuteTool402V2(ctx context.Context, node models.WorkflowNode, rc RunContexter, aw models.AgentWallet, signer WalletSigner, relayCfg X402RelayConfig) (Tool402PaymentResult, error) {
	isV2, notPaymentRequired, rawResponse, quote, err := probeTool402Endpoint(ctx, node.Endpoint)
	if err != nil {
		return Tool402PaymentResult{}, err
	}
	if notPaymentRequired {
		return Tool402PaymentResult{Response: rawResponse}, nil
	}
	if isV2 {
		// toolIsRunFunded (not a blanket RunFundingID != "" check) decides
		// this: a run can be funded while a SPECIFIC tool's probe failed
		// during estimation and was never folded into that estimate, and
		// such a tool must still take the per-call path below rather than
		// draw against a pool sized for other tools. Nested inside isV2 so
		// a legacy-dialect call attached to the same agent as a v2 one
		// still falls through, unmodified, to the direct-pay branch below
		// regardless of the run's funding state.
		if relayCfg.toolIsRunFunded(node.ID) {
			targetQuote := TargetQuote{
				PayTo:             quote.PayTo,
				Asset:             quote.Asset,
				MaxAmountRequired: strconv.FormatInt(quote.MaxAmountRequired, 10),
			}
			return executeTool402RunLevel(ctx, node, relayCfg, targetQuote, quote.MaxAmountRequired)
		}
		return executeTool402V2Relay(ctx, node, relayCfg.USDCSigner, relayCfg.PlatformSpendEncMnemonic, relayCfg.ExpectedAssetID, relayCfg.RelayBaseURL, PaymentLedger(relayCfg.Ledger))
	}

	// Legacy flat-quote dialect: unchanged direct-pay path, flat-fee billing,
	// paid from the agent's own wallet (not Wallet 1). If no wallet is
	// configured, ExecuteTool402 degrades gracefully without attempting a
	// payment at all — check that first so a reservation is never taken for
	// a call that can't possibly pay.
	if aw.EncryptedMnemonic == "" || signer == nil {
		result, err := ExecuteTool402(ctx, node, rc, aw, signer)
		if err != nil {
			return Tool402PaymentResult{}, err
		}
		return Tool402PaymentResult{Response: result}, nil
	}
	if reserve := relayCfg.LegacyLedger.Reserve; reserve != nil {
		if err := reserve(ctx, models.X402PlatformFeeUSDMicros); err != nil {
			return Tool402PaymentResult{}, &ErrBalanceBlocked{Err: err}
		}
	}
	// settled tracks whether the reservation above has already been
	// resolved (via Commit or Release) by the normal control flow below. If
	// a panic unwinds through ExecuteTool402 before that happens (any
	// runtime panic, not just an explicit error return), the balance would
	// otherwise stay permanently decremented with no debit_ledger row and
	// no way to reconcile it -- releasing here on the way out (before
	// re-panicking, so the original panic/stack trace still propagates and
	// this is not mistaken for a handled error) closes that window.
	settled := false
	defer func() {
		if !settled {
			if release := relayCfg.LegacyLedger.Release; release != nil {
				release(ctx, models.X402PlatformFeeUSDMicros)
			}
		}
	}()
	result, err := ExecuteTool402(ctx, node, rc, aw, signer)
	if err != nil {
		settled = true
		if release := relayCfg.LegacyLedger.Release; release != nil {
			release(ctx, models.X402PlatformFeeUSDMicros)
		}
		return Tool402PaymentResult{}, err
	}
	out := Tool402PaymentResult{Response: result}
	if m, ok := result.(map[string]any); ok {
		if _, hasTx := m["txId"]; hasTx {
			out.SettledUSDMicros = models.X402PlatformFeeUSDMicros
			out.DebitKind = models.DebitKindX402PlatformFee
			settled = true
			if commit := relayCfg.LegacyLedger.Commit; commit != nil {
				commit(ctx, node.ID, models.X402PlatformFeeUSDMicros, models.DebitKindX402PlatformFee)
			}
			return out, nil
		}
	}
	// No payment was actually sent (e.g. the retried request came back
	// without a txId) -- release the reservation, nothing to charge for.
	settled = true
	if release := relayCfg.LegacyLedger.Release; release != nil {
		release(ctx, models.X402PlatformFeeUSDMicros)
	}
	return out, nil
}

func executeTool402V2Relay(ctx context.Context, node models.WorkflowNode, usdcSigner USDCGroupSigner, platformSpendEncMnemonic string, expectedAssetID uint64, relayBaseURL string, ledger PaymentLedger) (Tool402PaymentResult, error) {
	if platformSpendEncMnemonic == "" || usdcSigner == nil {
		return Tool402PaymentResult{Response: map[string]any{"error": "payment required but no platform spend wallet configured"}}, nil
	}

	relayURL := relayBaseURL + "/x402/relay?target=" + url.QueryEscape(node.Endpoint)

	quoteReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, relayURL, nil)
	quoteResp, err := relayHTTPClient.Do(quoteReq)
	if err != nil {
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay quote failed: %w", err)
	}
	quoteBody, _ := io.ReadAll(io.LimitReader(quoteResp.Body, httpResponseLimit))
	quoteResp.Body.Close()

	var relayChallenge struct {
		Accepts []map[string]any `json:"accepts"`
	}
	if json.Unmarshal(quoteBody, &relayChallenge) != nil || len(relayChallenge.Accepts) == 0 {
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay: invalid challenge response")
	}
	accept := relayChallenge.Accepts[0]
	payTo, _ := accept["payTo"].(string)
	assetStr, _ := accept["asset"].(string)
	amountStr, _ := accept["maxAmountRequired"].(string)
	var feePayer string
	if extra, ok := accept["extra"].(map[string]any); ok {
		feePayer, _ = extra["feePayer"].(string)
	}
	assetID, err := strconv.ParseUint(assetStr, 10, 64)
	if err != nil {
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay: invalid asset id %q", assetStr)
	}
	if assetID != expectedAssetID {
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay: unexpected asset id %d, want %d", assetID, expectedAssetID)
	}
	amount, err := strconv.ParseUint(amountStr, 10, 64)
	if err != nil || amount == 0 || amount > math.MaxInt64 {
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay: invalid settlement amount %q", amountStr)
	}

	// USDC's 6 decimals match credit_balance_usd_micros' scale exactly —
	// the relay's asset base-unit amount converts to USD micros 1:1.
	//
	// Reserve (atomically decrement) the exact amount now, before signing —
	// not just check it — so a second call racing this one (another
	// iteration of the same agent's tool loop, or a concurrent standalone
	// tool402 node) can't also pass a check against the same stale balance
	// and cause the platform to pay out more than the user can cover in
	// aggregate.
	if reserve := ledger.Reserve; reserve != nil {
		if err := reserve(ctx, int64(amount)); err != nil {
			return Tool402PaymentResult{}, &ErrBalanceBlocked{Err: err}
		}
	}
	// settled tracks whether the reservation above has already been
	// resolved (via Commit or Release) by the normal control flow below —
	// see the identical pattern and rationale in ExecuteTool402V2's legacy
	// branch above. Covers a panic unwinding through the signing call, the
	// relay HTTP round trip, or response parsing before Commit/Release runs.
	settled := false
	defer func() {
		if !settled {
			if release := ledger.Release; release != nil {
				release(ctx, int64(amount))
			}
		}
	}()
	releaseReservation := func() {
		settled = true
		if release := ledger.Release; release != nil {
			release(ctx, int64(amount))
		}
	}

	group, idx, err := usdcSigner.SignUSDCPaymentGroup(ctx, platformSpendEncMnemonic, payTo, assetID, amount, feePayer)
	if err != nil {
		releaseReservation()
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay payment signing failed: %w", err)
	}
	xPayment, _ := json.Marshal(map[string]any{
		"x402Version": 2, "scheme": "exact",
		"payload": map[string]any{"paymentGroup": group, "paymentIndex": idx},
	})

	payReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, relayURL, nil)
	payReq.Header.Set("X-Payment", string(xPayment))
	payResp, err := relayHTTPClient.Do(payReq)
	if err != nil {
		releaseReservation()
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay payment request failed: %w", err)
	}
	defer payResp.Body.Close()
	finalBody, _ := io.ReadAll(io.LimitReader(payResp.Body, httpResponseLimit))

	out := Tool402PaymentResult{}
	var result any
	if json.Unmarshal(finalBody, &result) == nil {
		out.Response = result
	} else {
		out.Response = string(finalBody)
	}
	// Bill based on X-Inbound-Settled, not the relay's overall HTTP status.
	// The relay only sets this header once both (a) the inbound leg (Wallet
	// 1 -> Wallet 2) has irreversibly settled via the facilitator, and (b) a
	// real signed outbound payment group now exists as a submittable claim
	// (see x402relay.go's payTargetAndRespond) — so a signing failure on the
	// platform's side never bills the caller, but once a group is signed the
	// outbound leg to the caller-controlled target can still fail afterward
	// (the target errors, or rejects) with no refund path, and that must
	// still bill: gating on the final composite status instead would let a
	// malicious target accept payment and then deliberately return a
	// non-2xx response to avoid ever being billed, while still being paid.
	if payResp.Header.Get("X-Inbound-Settled") == "true" {
		out.SettledUSDMicros = int64(amount)
		out.DebitKind = models.DebitKindX402RelayCost
		settled = true
		if commit := ledger.Commit; commit != nil {
			commit(ctx, node.ID, int64(amount), models.DebitKindX402RelayCost)
		}
	} else {
		releaseReservation()
	}
	return out, nil
}

// executeTool402RunLevel pays a real target directly from Wallet 2,
// in-process — no HTTP round trip to our own public relay, no fresh
// inbound settle (that already happened once, in bulk, via
// reserveAndFundRun before this agent's loop started). Reserve/Commit/
// Release still go through cfg.Ledger exactly like the per-call path;
// the only difference is what's behind those calls (an in-memory pool
// instead of a DB round trip per call).
func executeTool402RunLevel(ctx context.Context, node models.WorkflowNode, cfg X402RelayConfig, quote TargetQuote, amount int64) (Tool402PaymentResult, error) {
	// quote/amount come from ExecuteTool402V2's dispatch probe, taken
	// synchronously immediately before this call -- no time gap in which
	// price could legitimately drift, unlike the once-per-run estimate in
	// reserveAndFundRun (fetched potentially minutes before any specific
	// call fires) or executeTool402V2Relay's own separate "freshest quote
	// right before signing" refetch, which stays untouched since a real
	// time gap exists there (the agent's tool-calling loop runs between
	// that dispatch and its own pay attempt).

	// Nil-safe like every other ledger call site in this file, even though
	// this path is only ever reached with a fully-populated cfg.Ledger
	// today (only from ExecuteTool402V2 once toolIsRunFunded is true) -- so
	// a future caller that forgets to wire it up fails loudly instead of
	// panicking on a nil func call.
	if reserve := cfg.Ledger.Reserve; reserve != nil {
		if err := reserve(ctx, amount); err != nil {
			return Tool402PaymentResult{}, &ErrBalanceBlocked{Err: err}
		}
	}

	result, payErr := PayTargetFromWallet2(ctx, cfg.Wallet2, node.Endpoint, quote)

	// Branch on result.Signed, not on payErr != nil -- Signed becomes true
	// the instant a real payment group is signed and submitted (see
	// Wallet2PayResult's doc comment in walletpay.go), meaning real money
	// has already left Wallet 2, and stays true regardless of what happens
	// afterward (the target unreachable at the network level, a non-2xx
	// target response, or a failure recording the audit row below).
	// Releasing the reservation for any of those would understate real
	// spend: a later call in the same agent turn could then Reserve
	// phantom headroom this pool doesn't actually have, and cleanup's
	// end-of-run release-unused-pool-to-DB would over-refund the user
	// relative to what was genuinely spent.
	if payErr != nil && !result.Signed {
		// Money never moved (asset mismatch, over-cap, or a failure signing
		// the payment group, all checked/attempted before any real payment
		// was sent) -- release the reservation, nothing was ever spent.
		if release := cfg.Ledger.Release; release != nil {
			release(ctx, amount)
		}
		return Tool402PaymentResult{}, payErr
	}

	// result.Signed is true past this point: the reservation must be
	// Committed, never Released, no matter what happens next.
	if cfg.RecordSettlement != nil {
		if recordErr := cfg.RecordSettlement(ctx, node.Endpoint, amount, result.Settled); recordErr != nil {
			// A bookkeeping failure, not a payment failure -- real money
			// already left Wallet 2, so this is not reversible. Alert so an
			// operator can reconcile the missing audit row by hand, matching
			// the identical pattern reserveAndFundRun already uses when
			// RecordRunFunding fails after a successful FundRunReserve.
			msg := fmt.Sprintf("CRITICAL: run-level x402 payment settled (node %s, target %s, amount %d) but RecordSettlement failed: %v",
				node.ID, node.Endpoint, amount, recordErr)
			log.Print(msg)
			go alert.Notify(context.Background(), alert.ChannelPayments, msg)
		}
	} else {
		// No RecordSettlement configured at all -- a wiring gap, not a
		// payment failure. Money still moved and the ledger Commit below
		// still runs; this only means the audit row is never written.
		msg := fmt.Sprintf("CRITICAL: run-level x402 payment settled (node %s, target %s, amount %d) with no RecordSettlement configured -- audit row never written",
			node.ID, node.Endpoint, amount)
		log.Print(msg)
		go alert.Notify(context.Background(), alert.ChannelPayments, msg)
	}

	if commit := cfg.Ledger.Commit; commit != nil {
		commit(ctx, node.ID, amount, models.DebitKindX402RelayCost)
	}

	if payErr != nil {
		// Target unreachable at the network level -- no response body to
		// return, nothing else to give the caller but the error. The ledger
		// above still reflects the real spend via Commit, not Release.
		return Tool402PaymentResult{}, payErr
	}

	var response any
	if err := json.Unmarshal(result.ResponseBody, &response); err != nil {
		response = string(result.ResponseBody)
	}
	return Tool402PaymentResult{Response: response, SettledUSDMicros: amount, DebitKind: models.DebitKindX402RelayCost}, nil
}
