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
