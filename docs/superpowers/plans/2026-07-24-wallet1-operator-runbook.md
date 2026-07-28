# Wallet 1 / Wallet 2 operator runbook

## One-time setup

1. Provision Wallet 1 (platform spend wallet):
   `go run ./cmd/walletgen -enc-key "$ENCRYPTION_KEY" -network mainnet -skip-opt-in > wallet1.enc`
   (or `-import-mnemonic "..."` if you generated it yourself in Pera/Defly first).
2. Fund the printed address with ALGO (fees + min balance) and USDC (the
   actual spend balance — this is the money that pays relayed x402 calls).
3. Complete the USDC opt-in now that the account is funded:
   `go run ./cmd/walletgen -enc-key "$ENCRYPTION_KEY" -network mainnet -opt-in-only "$(cat wallet1.enc)"`
4. Set `PLATFORM_SPEND_WALLET_ADDRESS` (the printed address) and
   `PLATFORM_SPEND_WALLET_ENC_MNEMONIC` (the contents of `wallet1.enc`) in
   the deploy environment. Delete `wallet1.enc` once copied in; clear
   terminal scrollback.
5. Wallet 2 (`PLATFORM_WALLET_ADDRESS`) is unchanged — it already exists
   and keeps settling the inbound leg and paying downstream targets.
6. The server now verifies at boot that `PLATFORM_SPEND_WALLET_ADDRESS`
   actually matches the address derived from
   `PLATFORM_SPEND_WALLET_ENC_MNEMONIC` (and the same for the
   `PLATFORM_WALLET_*` pair) — a mismatched address/mnemonic pairing fails
   startup immediately with a clear error rather than silently signing from
   an unexpected account.
7. Optionally set `MAX_RELAY_OUTBOUND_USD_MICROS` (defaults to 5000000,
   i.e. $5.00) to cap the maximum single outbound relay payment — see the
   price-drift limitation below for why this exists.

## Ongoing: manual sweep

Wallet 2 accumulates the spread between what Wallet 1 pays in and what
downstream targets actually charge, plus anything from other margin
sources. This is not swept automatically. Periodically (weekly, or when
Wallet 2's balance looks large relative to daily volume):

1. Check Wallet 2's balance.
2. Leave a working minimum in Wallet 2 (enough to cover a few days of
   expected settlement volume).
3. Send the rest back to Wallet 1 (to keep funding relayed calls) or to a
   separate treasury wallet, manually, via a plain Algorand transfer.

## NOWPayments payout caveat

If Wallet 1 is also set as the NOWPayments payout-receiving wallet: confirm
NOWPayments actually supports payouts in USDC on the Algorand network
specifically before relying on it — this was not verified as part of this
plan. If it isn't supported, credits and Wallet 1's on-chain balance are
already decoupled (credits are a database tally), so Wallet 1 can be
funded through any means — it just needs to hold enough balance in
aggregate, not a literal per-purchase transfer.

## Known limitation: slow/unresponsive relay targets

Billing keys off an `X-Inbound-Settled` response header the relay sets once
the caller's inbound payment (Wallet 1 -> Wallet 2) has genuinely settled via
the facilitator and a real signed outbound payment group exists — not off
the relay's final HTTP status, so a target can't dodge being billed by
returning a bad status after accepting payment. But if the orchestrator's
own call to the relay times out (90s) before any response arrives at all,
the orchestrator cannot see that header and defensively treats it as an
unsettled, unbilled attempt, even though the inbound leg (and possibly the
outbound leg too) may have completed on-chain in the background. The
target itself is bounded by its own ~10s budget inside the relay, so the
realistic causes of the orchestrator's 90s budget being exceeded are
unbounded latency inside the relay handler itself (e.g. a slow database
round trip on the settlement write) or an intermediate proxy/edge timeout
sitting between the orchestrator and `BASE_URL` (the orchestrator calls its
own public URL to reach the relay) — confirm whatever sits in front of the
backend (Railway's edge, any reverse proxy) has its own request timeout set
above 90s, or this gap widens silently. This is a known, accepted gap:
closing it fully requires the orchestrator to reconcile against the relay's
own `x402_relay_settlements` table after a transport failure, which is a
larger change than fits with the amount of on-chain money currently at
stake. Watch this table (per-target `status` =
`pending_outbound`/`settled`/`failed`) if relay call timeouts start showing
up in logs.

## Known limitation: quote price-drift between the relay's two target fetches

The relay fetches a target's x402 price quote twice per call cycle,
necessarily as two separate HTTP requests at different times: once in
`relayInboundChallenge` (the public, no-payment challenge preview) and again
in `relaySettleAndForward` (the authoritative fetch used to enforce and
record the actual settlement). If a target answers cheap on the first fetch
and expensive on the second, the relay would enforce/pay the second
(higher) amount from Wallet 2 while the caller's inbound payment — verified
against the first, lower quote by the caller's own client before it ever
signs anything — only covers the first amount. This is externally
reachable by anyone hitting the public, unauthenticated relay route, not
just an orchestrator user, and the only thing preventing a target from
actually exploiting it is the GoPlausible facilitator rejecting a payload
that doesn't cover its own enforced `MaxAmountRequired` in `/verify` — there
is no local defense against this scenario. `MAX_RELAY_OUTBOUND_USD_MICROS`
(default $5.00, see env var) bounds worst-case loss per call to a fixed
ceiling regardless of facilitator behavior, but does not close the gap
structurally — a full fix would require the caller-visible challenge and
the settle-time enforcement to be pinned to the same quote, which the
current two-request design doesn't support.

## Known limitation: a hard process kill during an in-flight payment can strand credits

Reserving a user's credits for a real x402 payment (`db.ReserveCredits`)
decrements their balance immediately, with no durable record of the
reservation itself — only a later Commit writes the permanent
`debit_ledger` audit row, and a later Release restores the balance if the
payment never completes. Both are protected against a normal Go panic
anywhere in the call path (a deferred cleanup releases the reservation
before the panic propagates) and against the triggering request's context
being cancelled or timing out (they run on `context.WithoutCancel` with
their own budget). Neither protects against the process itself being
killed outright (`SIGKILL`, an OOM kill, a host crash) between Reserve and
Commit/Release — there is no way for application code to run a compensating
action after the process no longer exists. This is a narrow window (single
in-flight payment, bounded by `relayHTTPClient`'s 90s timeout) but a real
one; a graceful-shutdown drain (waiting for in-flight runs before exiting
on a normal deploy) would close the common, non-crash case but was judged
out of scope for this feature branch — it's a process-lifecycle change,
not a payment-logic one. Track this in the same place as the timeout gap
above if it becomes an operational concern.

## What changed in the payment flow

- Every tool402 node hitting a real x402 v2 endpoint (`accepts[]` present)
  now routes through the AgentMesh relay and pays from Wallet 1, not from
  the triggering agent's own wallet.
- The amount gated against and debited from the triggering user's credits
  is the real settled amount reported by the target (via the relay's
  `maxAmountRequired`), not a flat platform fee. The flat fee only applies
  to the legacy flat-quote dialect (endpoints with no `accepts[]`), which
  still pays directly from the agent's own wallet, unchanged.
- If the triggering user's credit balance can't cover the real cost, the
  call is blocked before Wallet 1 signs or sends anything — no partial
  payment, no soft overage.

## Run-level pre-funding for agent-attached x402 v2 tools

When an agent node has attached v2 tool402 nodes, those tools now settle a
single inbound payment upfront before the agent's tool-calling loop begins,
sized to the sum of those tools' real current quotes (via
`FundRunReserve`) — never padded above what was actually quoted. This
replaces the previous per-call settlement pattern for this traffic.

Each subsequent real downstream tool402 call within the agent's loop pays out
of Wallet 2 via a direct in-process function call (`PayTargetFromWallet2`),
never through the public `/x402/relay` endpoint. These settlements are
recorded via the `x402_run_fundings` table (keyed by `run_funding_id`) for
audit and operational tracking. The public relay endpoint itself is
byte-for-byte unmodified — it's still the only entry point for standalone
tool402 nodes and any real external x402 client.

**Graceful degradation:** if the platform spend wallet (Wallet 1) isn't
configured — no `PLATFORM_SPEND_WALLET_ENC_MNEMONIC`, or a `walletSvc` that
doesn't implement the signer interface this path needs — an agent with
attached v2 tools falls back to the old per-call path rather than failing or
crashing. No credits are reserved in this case; the fallback is a true no-op
from the credit ledger's perspective.

**A legacy-dialect tool attached alongside a v2 tool on the same agent is
completely unaffected**, even when that agent is otherwise run-funded — it
always bills through its own independent, DB-backed per-call ledger, never
the run-level in-memory pool. (An earlier version of this feature let a
co-attached legacy tool draw against the v2 pool by mistake; this was caught
and fixed before merge — see `X402RelayConfig.LegacyLedger` /
`RunFundedToolIDs` in `internal/engine/nodes/tool402.go`.)

**If the run-funding audit write fails after the money has already moved
on-chain** (`RecordRunFunding` erroring after a successful `FundRunReserve`),
the run fails loudly rather than silently falling back to the per-call path —
falling back in that state would let a second, full inbound settlement
happen for the same run (Wallet 1 paying twice). The DB credit reservation is
deliberately *not* released when this happens, since the money is real and
already sitting in Wallet 2 (our own wallet) — reconcile it by hand against
the `x402_run_fundings` table using the `alert.ChannelPayments` notification
(logged as `CRITICAL: run funding settled on-chain (tx ...) but
RecordRunFunding failed`) as the starting point.

Because Wallet 2 now absorbs the upfront estimate before the full agent loop
runs, it may carry a temporarily larger balance per in-flight run — the
un-spent portion of the estimate remains in Wallet 2 until the run completes
and `cleanup` releases any balance above the actual sum of settled calls. This
widens the existing "manual sweep" operational note: the working minimum you
keep in Wallet 2 should account for multiple concurrent in-flight runs'
unspent estimates, not just daily throughput spread.

Note: An agent with no attached v2 tool402 nodes (or only legacy-dialect
ones) is unaffected and continues to use the completely unmodified per-call
path through the public relay.

### How this changes the three known limitations above, for run-funded traffic

Standalone tool402 nodes and any real external caller still go through the
completely unmodified public relay, so all three limitations above apply to
that traffic exactly as documented. For run-level-funded traffic specifically:

- **Relay-timeout ambiguity: narrowed, not eliminated.** Run-funded calls no
  longer make the orchestrator's own self-HTTP-hop to `/x402/relay` —
  `FundRunReserve`/`PayTargetFromWallet2` are direct in-process calls, so
  that exact shape of ambiguity goes away for this traffic. A smaller-stakes
  version reappears: if `FundRunReserve`'s call to the *external* GoPlausible
  facilitator times out, the run can't tell whether the bulk settle actually
  landed before deciding how to fail — lower severity than the original
  (both wallets involved are ours, so a wrong call here is a
  bookkeeping/accounting risk, not a lost-funds one), but not fully solved.
- **Quote price-drift: genuinely improved.** The run-level per-call executor
  fetches a target's price once and pays against that same quote
  immediately, closing the two-fetch drift window entirely for run-funded
  calls. `MAX_RELAY_OUTBOUND_USD_MICROS` still applies as a backstop.
- **Hard process kill stranding credits: made worse for run-funded traffic,
  not addressed.** The exposure window widens from one call's
  Reserve→Commit/Release cycle to the entire agent tool-calling loop's
  duration (up to 15 iterations) until `cleanup` finally runs after the
  agent node returns. A crash anywhere in that longer window strands the
  whole unspent portion of the run's estimate, not one call's worth. Not
  fixed here; the same deferred fix (a graceful-shutdown drain) applies.
