package nodes

import (
	"bytes"
	"context"
	"encoding/base64"
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
//
// SignUSDCPaymentSingle is the other half: a plain, self-fee-paying,
// single-transaction "exact" scheme payment for PayTargetFromWallet2's
// direct-to-third-party outbound leg, which no arbitrary target's own
// facilitator can cosign a fee-pooled stub for. Both methods live on the
// same interface since exactly one concrete type (*wallet.Service) signs
// for both legs today.
type USDCGroupSigner interface {
	SignUSDCPaymentGroup(ctx context.Context, encMnemonic, payTo string, assetID, amountMicros uint64, feePayerAddr string) ([]string, int, error)
	SignUSDCPaymentSingle(ctx context.Context, encMnemonic, payTo string, assetID, amountMicros uint64) ([]string, int, error)
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
	// MarkupLedger is the platform-flat-markup counterpart to Ledger, read
	// only by executeTool402RunLevel. Kept as a SEPARATE pool rather than
	// folded into Ledger's own budget: reserveAndFundRun sizes Ledger to
	// `estimate` (the exact amount actually moved on-chain Wallet 1 ->
	// Wallet 2 for this run) and MarkupLedger to `markupTotal` (pure
	// credits-side margin, no on-chain leg). If markup were added into
	// Ledger's own pool instead, a single call's real vendor amount could
	// exceed `estimate` by borrowing unused markup headroom left over from
	// other funded-but-never-called tools, letting Wallet 2 pay out real
	// USDC this run never actually funded on-chain. Unused for the
	// per-call relay path (executeTool402V2Relay), which reserves its own
	// amount+markup total from one DB-backed ledger per call — there's no
	// upfront padded pool to protect against in that path.
	MarkupLedger RunLedger
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
	// FlatFeeLedger reserves/commits/releases credits for an agent-attached
	// billable flat-fee node (BillableFlatFee -- an attached "http" Tool or
	// any Action/connector node), atomically per call, exactly like
	// LegacyLedger does for the legacy x402 dialect. Deliberately NOT a
	// batched-at-turn-end debit: checking balance without reserving and only
	// debiting once the whole agent turn ends would let every iteration of
	// the tool-calling loop check the same stale balance and collectively
	// overspend past what the user can cover (identical hazard to the one
	// newPaymentLedger's doc comment describes for x402 payments -- see
	// runner.go). A nil Reserve/Commit/Release is a no-op, matching the
	// pre-existing nil-checker convention elsewhere in this package.
	FlatFeeLedger CallLedger
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
	Response any
	// SettledUSDMicros is the real vendor/on-chain component only -- for a
	// v2 call this is strictly less than the total actually debited from
	// the user's credits, since PlatformFeeUSDMicros below is committed as
	// a second, separate debit_ledger row on top of it. Kept vendor-cost-
	// only (not the sum) so this field's meaning matches its DebitKind tag
	// and existing callers reading it for on-chain/audit purposes aren't
	// silently handed a blended number.
	SettledUSDMicros int64
	DebitKind        string
	// PlatformFeeUSDMicros is the flat markup committed alongside
	// SettledUSDMicros for a v2 call (models.X402PlatformFeeUSDMicros,
	// DebitKind models.DebitKindX402PlatformFee) -- zero for the legacy
	// dialect, whose SettledUSDMicros already IS the flat markup with no
	// separate vendor-cost component to add it to.
	PlatformFeeUSDMicros int64
}

// ChallengeAcceptsFromHeader extracts a v2 challenge's accepts[] from a
// Payment-Required response header, for targets that put the full
// base64-encoded challenge there and leave the JSON body empty/minimal —
// Prism's live endpoint does exactly this (confirmed 2026-07-31: body is
// `{}`, the real accepts[] is only in this header). This is the same header
// name and encoding this codebase's own relay emits (see
// relayInboundChallenge in handlers/x402relay.go), so it's a real,
// currently-used wire format, not a defensive guess.
func ChallengeAcceptsFromHeader(header http.Header) []map[string]any {
	challenge := ChallengeFromHeader(header)
	if challenge == nil {
		return nil
	}
	acceptsRaw, _ := challenge["accepts"].([]any)
	accepts := make([]map[string]any, 0, len(acceptsRaw))
	for _, a := range acceptsRaw {
		if m, ok := a.(map[string]any); ok {
			accepts = append(accepts, m)
		}
	}
	return accepts
}

// ChallengeFromHeader decodes a full v2 challenge object (not just its
// accepts[]) from a Payment-Required response header, when present -- some
// targets need the whole object echoed back verbatim on the paid retry
// (see TargetQuote.RawChallenge), not just the parsed fields
// ChallengeAcceptsFromHeader extracts.
func ChallengeFromHeader(header http.Header) map[string]any {
	b64 := header.Get("Payment-Required")
	if b64 == "" {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil
	}
	var challenge map[string]any
	if json.Unmarshal(decoded, &challenge) != nil {
		return nil
	}
	return challenge
}

// x402Quote is what a real v2 challenge's accepts[0] entry carries that
// callers in this package need — payTo/asset for actually paying it,
// MaxAmountRequired (parsed to USD micros) for gating/estimating.
type x402Quote struct {
	PayTo             string
	Asset             string
	MaxAmountRequired int64 // USD micros
	// FeePayer, when the target declares one in accepts[0].extra.feePayer,
	// names the shared facilitator address that will cosign a fee-pooled
	// stub txn for this specific payment. A target with no declared
	// feePayer is signaling the opposite -- a plain, self-fee-paying single
	// transaction -- so this must never default to our own relay's feePayer
	// constant; presence/absence of this exact field on the target's OWN
	// quote is what selects the signing scheme (see PayTargetFromWallet2).
	FeePayer string
	// RawAccept and RawChallenge are the exact accepts[0] entry and the
	// exact full challenge object the target returned, kept verbatim (not
	// reconstructed) -- see TargetQuote's matching fields for why.
	RawAccept    map[string]any
	RawChallenge map[string]any
}

// probeTool402Endpoint fetches endpoint's 402 challenge (if any) and reports
// whether it speaks real x402 v2 (accepts[] present) along with its current
// quote. notPaymentRequired=true means the endpoint answered something
// other than 402 (caller treats that as "no payment needed", exactly like
// ExecuteTool402V2 does today).
//
// method/body let this reach targets that gate on HTTP method before ever
// looking at payment state (e.g. a POST-only resource 404s a bare GET
// before it gets a chance to return 402) — real x402 endpoints are not
// guaranteed to be GET-compatible. method empty defaults to GET, matching
// every caller's behavior before this parameter existed. body is only ever
// sent when method is not GET, mirroring nodes.go's callHTTP convention for
// the plain "tool" node type.
func probeTool402Endpoint(ctx context.Context, endpoint, method string, body []byte) (isV2 bool, notPaymentRequired bool, rawResponse any, quote x402Quote, err error) {
	if err := urlValidator(endpoint); err != nil {
		return false, false, nil, x402Quote{}, err
	}
	if method == "" {
		method = http.MethodGet
	}
	var bodyReader io.Reader
	if method != http.MethodGet && len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}
	req, _ := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
	if bodyReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
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

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, httpResponseLimit))
	var rawChallenge map[string]any
	json.Unmarshal(respBody, &rawChallenge)
	var v2Challenge struct {
		Accepts []map[string]any `json:"accepts"`
	}
	json.Unmarshal(respBody, &v2Challenge)
	if len(v2Challenge.Accepts) == 0 {
		// Body carried no accepts[] -- some real targets (Prism's live
		// endpoint, confirmed 2026-07-31) put the full challenge in the
		// Payment-Required header instead, so the raw object has to come
		// from there too, not the (empty/minimal) body.
		v2Challenge.Accepts = ChallengeAcceptsFromHeader(resp.Header)
		if len(v2Challenge.Accepts) > 0 {
			rawChallenge = ChallengeFromHeader(resp.Header)
		}
	}
	if len(v2Challenge.Accepts) == 0 {
		return false, false, nil, x402Quote{}, nil
	}
	accept := v2Challenge.Accepts[0]
	payTo, _ := accept["payTo"].(string)
	asset, _ := accept["asset"].(string)
	var feePayer string
	if extra, ok := accept["extra"].(map[string]any); ok {
		feePayer, _ = extra["feePayer"].(string)
	}
	// `amount` is the field name the CURRENT real-world dialect uses (Prism's
	// live endpoint, the official @x402/core v2.20 SDK, confirmed live
	// 2026-07-31) — `maxAmountRequired` is checked first only because it's
	// this codebase's own historical convention, not because it's more
	// correct. Both were separately confirmed to parse fine against the real
	// facilitator, so accepting either read-side (never emitted ourselves,
	// see relayInboundChallenge in x402relay.go) is pure compatibility, not
	// a protocol judgment call.
	amount, ok := ParseMaxAmountRequiredAsMicros(accept["maxAmountRequired"])
	if !ok {
		amount, ok = ParseMaxAmountRequiredAsMicros(accept["amount"])
	}
	// This is a real v2 challenge (accepts[] present) -- a missing or
	// unparseable amount here is a malformed challenge, not a genuinely free
	// tool. Silently returning MaxAmountRequired: 0 in that case would be
	// indistinguishable from a real zero-cost quote, and Task 5 sizes both a
	// credit reservation and a real on-chain payment off this value -- a
	// silent 0 there is a money-correctness bug. Report it as an error
	// instead; isV2 still reflects reality (it IS a v2 challenge) even
	// though the quote itself is zero-valued.
	if !ok || amount <= 0 {
		return true, false, nil, x402Quote{}, fmt.Errorf("x402: invalid or missing amount/maxAmountRequired in v2 challenge (maxAmountRequired=%v amount=%v)", accept["maxAmountRequired"], accept["amount"])
	}
	return true, false, nil, x402Quote{PayTo: payTo, Asset: asset, MaxAmountRequired: amount, FeePayer: feePayer, RawAccept: accept, RawChallenge: rawChallenge}, nil
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
		// Same 1e15-micros ($1B/call) ceiling as the float64 branch below --
		// without it a target quoting a huge numeric string (e.g. close to
		// MaxInt64) parses "successfully" and downstream callers that add
		// models.X402PlatformFeeUSDMicros to this value (executeTool402RunLevel,
		// reserveAndFundRun's markup sizing) can overflow int64 into a negative
		// amount, which store.ReserveCredits then reads as a credit INCREASE.
		if err != nil || n < 0 || n > 1e15 {
			return 0, false
		}
		return n, true
	case float64:
		// Upper-bounded well below int64's range (1e15 micros = $1B/call,
		// already absurd for a real quote) and safely representable exactly
		// as a float64 (2^53 ≈ 9e15) -- without this, a target quoting e.g.
		// 1e20 as a JSON number converts via int64(t) to an
		// implementation-defined (in practice large-negative) result that
		// still returns ok=true, which every downstream ceiling check here
		// and in reserveAndFundRun assumes can't happen for a "valid" parse.
		if t != math.Trunc(t) || t < 0 || t > 1e15 {
			return 0, false
		}
		return int64(t), true
	default:
		return 0, false
	}
}

// ProbeX402Price is the exported form the run-level estimator (Task 5) uses
// when it only needs "is this v2, and what's the current price" — not the
// full quote. Always probes with an empty body: the 402 gate on a real
// x402 endpoint fires on method/payment-header alone, before any body
// validation, so a pure price probe never needs the real call's payload —
// only its method.
func ProbeX402Price(ctx context.Context, endpoint, method string) (isV2 bool, amountUSDMicros int64, err error) {
	isV2, _, _, quote, err := probeTool402Endpoint(ctx, endpoint, method, nil)
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
	// FeePayer mirrors x402Quote.FeePayer -- see that field's doc comment
	// for why its presence/absence (not a blanket default) selects which
	// signing scheme PayTargetFromWallet2 uses.
	FeePayer string
	// RawAccept is the target's own accepts[0] entry, verbatim, and
	// RawChallenge is its full challenge object, verbatim -- some targets
	// (confirmed live 2026-08-01: canix402-api.compx.io) require their own
	// challenge echoed back inside the paid retry rather than a fresh
	// minimal payload, so PayTargetFromWallet2 needs the exact original
	// objects, not a reconstruction from the parsed fields above (which
	// would drop fields specific to that target's own schema).
	RawAccept    map[string]any
	RawChallenge map[string]any
}

// ProbeX402Quote is the exported form the run-level per-call executor
// (Task 5) uses when it needs the full quote (payTo/asset too) to actually
// pay it via PayTargetFromWallet2 (Task 3). Same empty-body reasoning as
// ProbeX402Price.
func ProbeX402Quote(ctx context.Context, endpoint, method string) (isV2 bool, quote TargetQuote, err error) {
	isV2, _, _, q, err := probeTool402Endpoint(ctx, endpoint, method, nil)
	return isV2, TargetQuote{
		PayTo:             q.PayTo,
		Asset:             q.Asset,
		MaxAmountRequired: strconv.FormatInt(q.MaxAmountRequired, 10),
		FeePayer:          q.FeePayer,
		RawAccept:         q.RawAccept,
		RawChallenge:      q.RawChallenge,
	}, err
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
	method := node.Method
	if method == "" {
		method = http.MethodGet
	}
	isV2, notPaymentRequired, rawResponse, quote, err := probeTool402Endpoint(ctx, node.Endpoint, method, nil)
	if err != nil {
		return Tool402PaymentResult{}, err
	}
	if notPaymentRequired {
		return Tool402PaymentResult{Response: rawResponse}, nil
	}
	if isV2 {
		// The real, final call (as opposed to the probe above) carries the
		// run's actual input as its body when method isn't GET — same
		// convention nodes.go's callHTTP already uses for the plain "tool"
		// node type's http template. GET requests never carry a body,
		// unchanged from before this parameter existed.
		var payBody []byte
		if method != http.MethodGet {
			payBody = []byte(rc.Message())
		}
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
				FeePayer:          quote.FeePayer,
				RawAccept:         quote.RawAccept,
				RawChallenge:      quote.RawChallenge,
			}
			return executeTool402RunLevel(ctx, node, relayCfg, targetQuote, quote.MaxAmountRequired, method, payBody)
		}
		return executeTool402V2Relay(ctx, node, relayCfg.USDCSigner, relayCfg.PlatformSpendEncMnemonic, relayCfg.ExpectedAssetID, relayCfg.RelayBaseURL, PaymentLedger(relayCfg.Ledger), method, payBody)
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

// setRelayTargetHeaders tells our own /x402/relay handler (x402relay.go)
// what HTTP method/body to use against target, out of band from the
// relay's own always-GET calling convention (?target=... never changes
// shape). A body goes in a header, base64-encoded, rather than the query
// string: request bodies (a full resume's text, for example) can easily
// exceed URLs' practical length limits, while headers comfortably hold
// far more (net/http's default MaxHeaderBytes is 1MB). Same base64-in-
// header pattern this file/x402relay.go already use for X-Payment and
// Payment-Required.
func setRelayTargetHeaders(req *http.Request, method string, body []byte) {
	if method != "" && method != http.MethodGet {
		req.Header.Set("X-Relay-Method", method)
	}
	if len(body) > 0 {
		req.Header.Set("X-Relay-Body", base64.StdEncoding.EncodeToString(body))
	}
}

// targetMethod/targetBody describe the call the RELAY should make to
// node.Endpoint (our own /x402/relay is always reached via a plain GET
// itself — these two headers just tell the relay handler what to do with
// target on the relay's own end, same "method/body only matter for the
// downstream target, never for talking to the relay" split PayTargetFromWallet2
// and probeTool402Endpoint already follow).
func executeTool402V2Relay(ctx context.Context, node models.WorkflowNode, usdcSigner USDCGroupSigner, platformSpendEncMnemonic string, expectedAssetID uint64, relayBaseURL string, ledger PaymentLedger, targetMethod string, targetBody []byte) (Tool402PaymentResult, error) {
	if platformSpendEncMnemonic == "" || usdcSigner == nil {
		return Tool402PaymentResult{Response: map[string]any{"error": "payment required but no platform spend wallet configured"}}, nil
	}

	relayURL := relayBaseURL + "/x402/relay?target=" + url.QueryEscape(node.Endpoint)

	quoteReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, relayURL, nil)
	setRelayTargetHeaders(quoteReq, targetMethod, targetBody)
	quoteResp, err := relayHTTPClient.Do(quoteReq)
	if err != nil {
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay quote failed: %w", err)
	}
	quoteBody, _ := io.ReadAll(io.LimitReader(quoteResp.Body, httpResponseLimit))
	quoteResp.Body.Close()

	var relayChallenge struct {
		Accepts []map[string]any `json:"accepts"`
		// Resource/Extensions are captured here so the paid retry below can
		// echo them back verbatim onto its own payment payload -- a real v2
		// client is expected to copy these straight from the challenge it
		// received, and without them here even a correctly-signed payment
		// has nothing for the facilitator's discovery extraction to catalog
		// against (see x402relay.go's resourceInfo/bazaarDiscoveryExtension
		// doc comments). The relay itself now also sets these fields
		// server-side regardless of what this payload carries, so this is
		// belt-and-suspenders rather than load-bearing for OUR OWN relay --
		// but it's what makes this call correct/spec-compliant client
		// behavior in general, not just against our own endpoint.
		Resource   map[string]any `json:"resource"`
		Extensions map[string]any `json:"extensions"`
	}
	if json.Unmarshal(quoteBody, &relayChallenge) != nil || len(relayChallenge.Accepts) == 0 {
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay: invalid challenge response")
	}
	accept := relayChallenge.Accepts[0]
	payTo, _ := accept["payTo"].(string)
	assetStr, _ := accept["asset"].(string)
	// Our own relay emits "amount" (matching GoPlausible's facilitator wire
	// format — see relayInboundChallenge), "maxAmountRequired" kept as a
	// fallback for the historical dialect.
	amountStr, ok := accept["amount"].(string)
	if !ok {
		amountStr, _ = accept["maxAmountRequired"].(string)
	}
	network, _ := accept["network"].(string)
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
	if err != nil || amount == 0 || amount > uint64(models.MaxSingleX402QuoteUSDMicros) {
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay: invalid settlement amount %q", amountStr)
	}

	// USDC's 6 decimals match credit_balance_usd_micros' scale exactly —
	// the relay's asset base-unit amount converts to USD micros 1:1.
	//
	// total is amount (the real vendor cost, what actually leaves Wallet 2)
	// plus the platform's flat markup -- see executeTool402RunLevel's
	// identical total/amount split for the run-funded path; both real x402
	// dispatch paths bill the same way.
	total := int64(amount) + models.X402PlatformFeeUSDMicros
	//
	// Reserve (atomically decrement) the exact amount now, before signing —
	// not just check it — so a second call racing this one (another
	// iteration of the same agent's tool loop, or a concurrent standalone
	// tool402 node) can't also pass a check against the same stale balance
	// and cause the platform to pay out more than the user can cover in
	// aggregate.
	if reserve := ledger.Reserve; reserve != nil {
		if err := reserve(ctx, total); err != nil {
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
				release(ctx, total)
			}
		}
	}()
	releaseReservation := func() {
		settled = true
		if release := ledger.Release; release != nil {
			release(ctx, total)
		}
	}

	group, idx, err := usdcSigner.SignUSDCPaymentGroup(ctx, platformSpendEncMnemonic, payTo, assetID, amount, feePayer)
	if err != nil {
		releaseReservation()
		return Tool402PaymentResult{}, fmt.Errorf("x402 relay payment signing failed: %w", err)
	}
	xPaymentFields := map[string]any{
		"x402Version": 2, "scheme": "exact", "network": network,
		"payload": map[string]any{"paymentGroup": group, "paymentIndex": idx},
	}
	if relayChallenge.Resource != nil {
		xPaymentFields["resource"] = relayChallenge.Resource
	}
	if relayChallenge.Extensions != nil {
		xPaymentFields["extensions"] = relayChallenge.Extensions
	}
	xPayment, _ := json.Marshal(xPaymentFields)

	payReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, relayURL, nil)
	payReq.Header.Set("X-Payment", string(xPayment))
	setRelayTargetHeaders(payReq, targetMethod, targetBody)
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
		out.PlatformFeeUSDMicros = models.X402PlatformFeeUSDMicros
		settled = true
		if commit := ledger.Commit; commit != nil {
			commit(ctx, node.ID, int64(amount), models.DebitKindX402RelayCost)
			commit(ctx, node.ID, models.X402PlatformFeeUSDMicros, models.DebitKindX402PlatformFee)
		}
		// Surfaces the real, already-settled inbound tx id in the node's own
		// output (rather than only the DB audit trail) so a run's console log
		// shows it -- same txId/explorerURL shape LogDrawer already renders
		// specially for the legacy direct-pay dialect (see ExecuteTool402).
		// Only when Response unmarshaled as a JSON object: a non-object
		// response (e.g. the target returned a bare string/array) has no
		// place to attach sibling fields without changing its type.
		if txID := payResp.Header.Get("X-Settlement-TxId"); txID != "" {
			if m, ok := out.Response.(map[string]any); ok {
				m["txId"] = txID
				m["amount"] = strconv.FormatUint(amount, 10)
				m["explorerURL"] = explorerURLForAsset(expectedAssetID, txID)
			}
		}
		// The OUTBOUND leg's own settlement id (Wallet 2 -> target) --
		// separate from txId above, which is only ever the inbound leg
		// (caller -> Wallet 2). Together they show the full real payment
		// chain in a run's console log: caller -> Wallet 2 -> target. Not
		// every target returns one (Settled/StatusCode already say whether
		// the payment worked regardless), so this is additive/best-effort.
		if outboundTxID := payResp.Header.Get("X-Outbound-Settlement-TxId"); outboundTxID != "" {
			if m, ok := out.Response.(map[string]any); ok {
				m["outboundTxId"] = outboundTxID
				m["outboundExplorerURL"] = explorerURLForAsset(expectedAssetID, outboundTxID)
			}
		}
		// Billing (above) and target delivery are separate concerns by
		// design -- but once billed, a non-2xx from target must still
		// surface as a failed node, or a run silently "succeeds" with the
		// caller charged and target's own raw 402/error body relayed back
		// as if it were real data (confirmed live 2026-08-01: a target that
		// rejected the signed outbound payment still produced a "success"
		// step with its un-paid challenge merged in as the response).
		// Deliberately scoped to this branch only -- when the INBOUND leg
		// itself is rejected (below, nothing billed), returning the relay's
		// error body as a graceful non-error result is correct as-is.
		if payResp.StatusCode < 200 || payResp.StatusCode >= 300 {
			const errSnippetLimit = 512
			snippet := finalBody
			if len(snippet) > errSnippetLimit {
				snippet = snippet[:errSnippetLimit]
			}
			return out, fmt.Errorf("x402 relay: target rejected the paid request (status %d): %s", payResp.StatusCode, snippet)
		}
	} else {
		releaseReservation()
	}
	return out, nil
}

// explorerURLForAsset picks the Lora explorer network segment from the
// USDC asset id the relay was configured to expect -- the same
// testnet/mainnet asset id split main.go already uses to choose
// usdcAssetID and relayNetwork together, so the two stay consistent without
// threading a separate network string through this call chain.
func explorerURLForAsset(assetID uint64, txID string) string {
	network := "testnet"
	if assetID == mainnetUSDCAssetID {
		network = "mainnet"
	}
	return "https://lora.algokit.io/" + network + "/transaction/" + txID
}

const mainnetUSDCAssetID = 31566704

// executeTool402RunLevel pays a real target directly from Wallet 2,
// in-process — no HTTP round trip to our own public relay, no fresh
// inbound settle (that already happened once, in bulk, via
// reserveAndFundRun before this agent's loop started). Reserve/Commit/
// Release still go through cfg.Ledger exactly like the per-call path;
// the only difference is what's behind those calls (an in-memory pool
// instead of a DB round trip per call).
func executeTool402RunLevel(ctx context.Context, node models.WorkflowNode, cfg X402RelayConfig, quote TargetQuote, amount int64, method string, body []byte) (Tool402PaymentResult, error) {
	// quote/amount come from ExecuteTool402V2's dispatch probe, taken
	// synchronously immediately before this call -- no time gap in which
	// price could legitimately drift, unlike the once-per-run estimate in
	// reserveAndFundRun (fetched potentially minutes before any specific
	// call fires) or executeTool402V2Relay's own separate "freshest quote
	// right before signing" refetch, which stays untouched since a real
	// time gap exists there (the agent's tool-calling loop runs between
	// that dispatch and its own pay attempt).

	// Every real x402 call -- v2 or legacy, run-funded or per-call -- bills
	// the platform's flat markup on top of the real vendor amount, not just
	// the vendor amount alone. amount and markup are reserved/committed
	// against two SEPARATE pools here, not summed into one: cfg.Ledger is
	// sized to `estimate` by reserveAndFundRun -- the exact amount actually
	// moved on-chain Wallet 1 -> Wallet 2 for this run -- so it correctly
	// gates the real PayTargetFromWallet2 call below at what this run's
	// on-chain leg actually funded. cfg.MarkupLedger is a second pool sized
	// to markupTotal (pure credits bookkeeping, no on-chain leg of its
	// own). Reserving amount+markup from ONE pool sized estimate+markupTotal
	// would let a single call's real amount exceed `estimate` by borrowing
	// unused markup headroom from other funded-but-uncalled tools, causing
	// Wallet 2 to pay out real USDC this run never backed on-chain.
	markup := int64(models.X402PlatformFeeUSDMicros)

	// Nil-safe like every other ledger call site in this file, even though
	// this path is only ever reached with fully-populated cfg.Ledger/
	// cfg.MarkupLedger today (only from ExecuteTool402V2 once
	// toolIsRunFunded is true) -- so a future caller that forgets to wire
	// them up fails loudly instead of panicking on a nil func call.
	if reserve := cfg.Ledger.Reserve; reserve != nil {
		if err := reserve(ctx, amount); err != nil {
			return Tool402PaymentResult{}, &ErrBalanceBlocked{Err: err}
		}
	}
	if reserve := cfg.MarkupLedger.Reserve; reserve != nil {
		if err := reserve(ctx, markup); err != nil {
			if release := cfg.Ledger.Release; release != nil {
				release(ctx, amount)
			}
			return Tool402PaymentResult{}, &ErrBalanceBlocked{Err: err}
		}
	}

	result, payErr := PayTargetFromWallet2(ctx, cfg.Wallet2, node.Endpoint, method, body, quote)

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
		// was sent) -- release both reservations, nothing was ever spent.
		if release := cfg.Ledger.Release; release != nil {
			release(ctx, amount)
		}
		if release := cfg.MarkupLedger.Release; release != nil {
			release(ctx, markup)
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

	// Two separate debit_ledger rows for one call, each committed against
	// the pool it was reserved from above: the real vendor cost (what
	// Wallet 2 actually paid out, cfg.Ledger) and the platform's flat
	// markup on top of it (pure margin, no corresponding on-chain leg,
	// cfg.MarkupLedger). Commit only ever writes the audit row, it never
	// touches either pool's remaining balance.
	if commit := cfg.Ledger.Commit; commit != nil {
		commit(ctx, node.ID, amount, models.DebitKindX402RelayCost)
	}
	if commit := cfg.MarkupLedger.Commit; commit != nil {
		commit(ctx, node.ID, markup, models.DebitKindX402PlatformFee)
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

	// result.Settled (true only for a 2xx from target) is intentionally
	// separate from billing above -- money already left Wallet 2 the
	// instant it was signed, so Commit above is correct regardless. But
	// the node must still report failure when target rejected the paid
	// request, or a run silently "succeeds" while relaying target's own
	// un-paid 402/error body back as if it were real data -- the exact
	// bug this comment is fixing (confirmed live 2026-08-01, same failure
	// mode as executeTool402V2Relay above).
	if !result.Settled {
		const errSnippetLimit = 512
		snippet := result.ResponseBody
		if len(snippet) > errSnippetLimit {
			snippet = snippet[:errSnippetLimit]
		}
		return Tool402PaymentResult{Response: response, SettledUSDMicros: amount, DebitKind: models.DebitKindX402RelayCost, PlatformFeeUSDMicros: markup},
			fmt.Errorf("x402 run-level: target rejected the paid request (status %d): %s", result.StatusCode, snippet)
	}

	// The outbound leg's own settlement id (Wallet 2 -> target), when the
	// target returned one -- see the matching merge in
	// executeTool402V2Relay above for why this is surfaced in the node's
	// output rather than only the DB audit trail.
	if result.OutboundTxID != "" {
		if m, ok := response.(map[string]any); ok {
			m["outboundTxId"] = result.OutboundTxID
			m["outboundExplorerURL"] = explorerURLForAsset(cfg.ExpectedAssetID, result.OutboundTxID)
		}
	}

	return Tool402PaymentResult{Response: response, SettledUSDMicros: amount, DebitKind: models.DebitKindX402RelayCost, PlatformFeeUSDMicros: markup}, nil
}
