# x402-demo

Two real, GoPlausible-facilitated Algorand x402 endpoints, standalone from
AgentMesh's production backend, entering the [Algorand Foundation Global
x402 Challenge](https://algorand.co/global-x402-challenge) as two separate
leaderboard entries:

- **`merchant`** — a **Standard** entry. A single paid endpoint,
  `POST /resume-screen`, priced in USDC. Request/response schema
  deliberately mirrors [Prism](https://prism-99h2.onrender.com/resume-screen-accurate),
  a live challenge entry we're collaborating with: `task_description` +
  `files[]` in, ranked `candidates[]` out. The 402 challenge and settlement
  wire format mirror Prism's actual bytes too (verified live against both
  Prism and the real facilitator — see `demolib/wire.go`'s doc comment).
- **`orchestrator`** — an **Orchestrator** entry. Itself a paid endpoint,
  `POST /orchestrate-resume-screen`; once paid, it becomes an x402 client
  and pays the merchant on the caller's behalf, then returns the merchant's
  result plus both real settlement tx ids.

Three wallets, matching AgentMesh's own Wallet 1 → Wallet 2 → target model:

| Wallet | Role | Address (testnet, generated 2026-07-30) |
|---|---|---|
| **Client** | Payer — pays the orchestrator | `SZFYWYFKXBE63UKG4XQEB76ZD4RAKL54KHPQ3372N2HINJGIENJN7AMG54` |
| **Orch Wallet 2** | Receives inbound, pays merchant outbound | `6FXGH32NW4Z7THEPZTHRY3NNQ2SWD7HM22BEEKTAEGGZMSVBUFWENH2K4A` |
| **Merchant Wallet 3** | Only ever receives | `BIM3CADOAILX7KGAUHKJ7VBNIZZ7NU5RI2EFTEFPPVVWE7EOWHBYBYGJOU` |

All three are already generated and their (ENCRYPTION_KEY-encrypted, same
format as AgentMesh's production platform wallets) mnemonics are already in
`.env` — see [Funding](#funding) below for what to send where.

## Why Go, not the official TS SDK

Reuses `internal/wallet.Service` (the exact `SignUSDCPaymentGroup` /
`OptInAsset` / `Balance` primitives AgentMesh's production relay already
uses, proven in PR #41) and `backend/cmd/walletgen` for wallet generation —
one language, no new SDK to learn, no dependency on a fast-moving npm
package's internal type chunks.

## Layout

```
cmd/x402demo/
  demolib/        shared: wire types, facilitator client, config, resume
                   scorer, PayAndFetch (the client-side pay-and-retry flow)
  merchant/        the Standard entry
  orchestrator/    the Orchestrator entry
  client/          demo driver — plays the real payer, prints the full trace
  .env             already populated (gitignored)
  .env.example
```

## Running it

All commands run from `backend/`:

```bash
# terminal 1
go run ./cmd/x402demo/merchant

# terminal 2
go run ./cmd/x402demo/orchestrator

# terminal 3 — after funding all three wallets, see below
go run ./cmd/x402demo/client
```

`client` prints the full trace: the JSON resume-screening result, plus both
real settlement tx ids (client→orchestrator and orchestrator→merchant) with
explorer links.

## Funding

Every address needs testnet ALGO (fees + minimum balance + the USDC
opt-in). The **client** additionally needs testnet USDC — it's the only
wallet paying anything in the demo loop (orchestrator's outbound payment to
the merchant is funded by what it just received from the client).

1. **ALGO**, on all three addresses — [testnet dispenser](https://bank.testnet.algorand.network/):
   - `SZFYWYFKXBE63UKG4XQEB76ZD4RAKL54KHPQ3372N2HINJGIENJN7AMG54` (Client)
   - `6FXGH32NW4Z7THEPZTHRY3NNQ2SWD7HM22BEEKTAEGGZMSVBUFWENH2K4A` (Orch Wallet 2)
   - `BIM3CADOAILX7KGAUHKJ7VBNIZZ7NU5RI2EFTEFPPVVWE7EOWHBYBYGJOU` (Merchant Wallet 3)
   - ~2 ALGO each is comfortably enough (opt-in min-balance + fees for many demo runs).

2. **Opt in to testnet USDC (ASA 10458941)** on all three — required before
   any of them can hold or receive it. Using the already-generated,
   already-encrypted mnemonics in `.env`:

   ```bash
   ENC_KEY=b5bd2eef46a1669dddbb24a25ffb7400

   # after funding each address with ALGO above, opt each into USDC:
   go run ./cmd/walletgen -enc-key="$ENC_KEY" -network=testnet \
     -opt-in-only="731E8VEHOI6bGA921SYMc02tlGZskGokkAX5IrMk0gzY+NgoezLdxaN7V8ygFnhOWwMS7+xuCa0e7jsILXgQFnr97kNs/Ez/bVcL86nKSYImZVAg8TuKcoSn0kDtRv2nX3ygXa8OEuAMcXPrG+EUSOUr4Vzg6cUGCIkPhZWOtoOE+xHIEjgmv2SetwnPIWtHaqPKWf1gdlU6kWJFXPOobbENPYPpO5XPXempV4iSziaxNjaSO6bIYrp4"   # Client

   go run ./cmd/walletgen -enc-key="$ENC_KEY" -network=testnet \
     -opt-in-only="fiaTdHUaAEbmM7qZFK56s2rCZw+lOCXoywkykwyZZU1YfpvCbk6/hzV3O6f63LLVEykSPxVW3Dk6YOHvwH/kPAYEjdKlADdb21LrM2L83k7oiFOkODQJd6M/6T6e7EIuOR2CenOpJ6FMhfcavq/XgfOkb/4jJFbG1yHizwWuS6z2ZoNTt2mnzbV0VGQd1ispM54urRdpdSladG91GE1cU6KELjZ2lwvBW7v0O1Rp95E35FDX6puoUQ=="   # Orch Wallet 2

   go run ./cmd/walletgen -enc-key="$ENC_KEY" -network=testnet \
     -opt-in-only="aZyOQAYIdvXOCOQTUu/VmBK15XpzmiLLLWMljcQ1fUXF4E7DAetljoh1nMpzHwlBJpe443k7638jTsPasSsKG2n/6MM4vnvjJoQzmrvMNdsdmyoZH8LpJmAD6SAkvodft0Ng1F3GJ6WeuIQKtNnnv466ZQ3Iu389zD31rYe9ojoTTRrwDo0upNIAM1c7Zp5/5ewUjPIVBgJc7s5Qc3NGxKH9YIypJAT5REz1iocHMyXHr0NVDgQSCxd/DhzM"   # Merchant Wallet 3
   ```

3. **Testnet USDC**, on the **Client address only** —
   [testnet dispenser](https://testnet-dispensers.algorand.org/) (select
   USDC): `SZFYWYFKXBE63UKG4XQEB76ZD4RAKL54KHPQ3372N2HINJGIENJN7AMG54`.
   A few dollars' worth covers many demo runs at $0.05/call.

Once funded, `go run ./cmd/x402demo/client` (with `merchant` and
`orchestrator` running) does a real end-to-end settle.

## Testnet → mainnet cutover

Everything is driven by one env var. When ready:

1. Flip `NETWORK=mainnet` in `.env`.
2. Fund all three addresses with real ALGO + real USDC (mainnet ASA
   `31566704`) — same amounts/roles as above, mainnet dispensers don't
   exist, so this is a real transfer.
3. Re-run the opt-in step above (same command, same encrypted mnemonics —
   `wallet.Service` reads `NETWORK`/`ALGOD_URL` from `.env`, so opting in
   again against mainnet uses mainnet algod automatically once you also
   pass `-network=mainnet -asset-id=31566704` to `walletgen`).
4. Set `PUBLIC_MERCHANT_URL` / `PUBLIC_ORCHESTRATOR_URL` to real, public
   HTTPS URLs (deploy or tunnel both processes) — Bazaar's discovery
   crawler and the leaderboard both need to actually reach these.
5. Confirm both endpoints show up in Bazaar and complete at least one real
   mainnet payment (per the Global x402 Challenge's qualification rule).

No code changes required for the cutover — only `.env`.

## What "totally like the real endpoint" means here

`demolib/wire.go` matches Prism's real wire format field-for-field (`amount`
not `maxAmountRequired`, nested `resource` object, `accepted` field in the
payment payload, `X-Payment-Response` header carrying the real settlement)
— confirmed by decoding Prism's actual live 402 challenge and by manually
probing `facilitator.goplausible.xyz/verify` with both this dialect and
AgentMesh's own production relay dialect on 2026-07-30 (both parse
successfully; this package intentionally matches the reference entry's
bytes rather than reusing AgentMesh's own, slightly different production
dialect).
