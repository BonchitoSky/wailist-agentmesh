# Opt-in display currency

Branch: `feature/settings-page` · Migration: `000021`

Every amount on the site is USD-denominated and hardcoded at its render site.
This adds a currency preference to the settings page that changes what the user
reads — and nothing else.

---

## 0. The governing invariant — USD is a no-op

**Nothing in the frontend changes unless the user picks a currency in Settings.**

`display_currency` defaults to `'USD'`. While it is `'USD'`, every screen renders
**exactly** as it does today: same strings, same `$12.50`, no relabelling, and
**no extra network request** — the client does not call `/fx/rates` at all.

This is the acceptance criterion the rest of the design bends around. In code:
`formatMoney(x, "USD")` returns the byte-identical string that
`` `$${n.toFixed(2)}` `` produces today, locked by regression tests. A default
user gains no new failure mode, no new request, and no new pixels.

## 1. What may change, and what may not

| Layer            | Where                                                                                       | Changeable?                                                                                                             |
| ---------------- | ------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| Settlement       | USDC on Algorand via x402                                                                   | **No.** x402 quotes and pays in USDC.                                                                                   |
| Ledger of record | `credit_ledger`, `debit_ledger`, `tendril_credit_ledger`, `users.credit_balance_usd_micros` | **No.** Append-only financial records; re-denominating breaks the audit trail and reconciliation against on-chain USDC. |
| Presentation     | ~16 render sites                                                                            | **Yes.** This is the whole feature.                                                                                     |

Cashfree charges **INR only** (`cashfree.go:72` hardcodes `"order_currency":
"INR"`), and it is the only enabled provider. The charge currency is fixed
regardless of display preference.

## 2. Corrections to the first draft

Recorded rather than deleted; three would have shipped something misleading.

| #   | Mistake                             | Correction                                                                                                                                                                                                                                                   |
| --- | ----------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 0   | Changed the UI for everyone         | §0 — opt-in; USD is byte-identical to today                                                                                                                                                                                                                  |
| 1   | "All currency is USD"               | **The purchase path is already INR** — `billing/page.tsx:484`, `PurchaseHistory.tsx:95`, `CartItemRow.tsx:68`, `OrderSummary.tsx:48,107`, `PaymentInfoPanel.tsx:125`, all of `Receipt.tsx`. Already dual-currency: pay INR, receive USD-denominated credits. |
| 2   | Convert canvas node prices          | **x402 prices are USDC.** `Inspector.tsx:1186-1189` renders `{node.price}` beside `{node.asset ?? "USDC"} / {node.unit}`. Converting misstates what the endpoint charges. Excluded.                                                                          |
| 3   | Convert purchase history + receipts | **localStorage fixtures** — `lib/credits/store.ts:17`. Excluded.                                                                                                                                                                                             |
| 4   | Charts are "a formatter swap"       | `AreaChart.tsx` takes `algoUsd`, derives `spendUsd` from ALGO series, hardcodes `" USD"` at line 225. Needs plumbing; own commit.                                                                                                                            |
| 5   | 1h rate cache                       | `open.er-api.com` publishes **once daily**. 12h TTL, serve-stale-on-error.                                                                                                                                                                                   |
| 6   | "45 call sites"                     | ~16 convert (§4).                                                                                                                                                                                                                                            |

Verified live against the API: 162 currencies, all ten shortlist codes present,
INR = 95.25/USD.

## 3. Credits are not currency

`terms/page.tsx` §4.1 — credits are "**not** currency, legal tender, or a
financial instrument of any kind."
`refund-policy/page.tsx` §4 — credits "hold **zero monetary value** and cannot be
converted, withdrawn, or exchanged for cash, fiat currency, or any other monetary
form **under any circumstances**."

§0 defuses most of this: the default experience is untouched. For a user who
deliberately opts in:

```
USD (default, unchanged)      non-USD (opt-in)
$12.50                        12.50 credits
                              ≈ €10.82 · not redeemable for cash
```

Credits become the primary unit — which is what they are — and the fiat figure is
an explicitly non-redeemable estimate, shown only because it was asked for. Real
prices and charges (platform tier fees, Tendril machine rates, metered spend)
convert normally; the policy language does not cover those.

> Not a code task: whoever owns `terms/page.tsx` and `refund-policy/page.tsx`
> should read the "not redeemable for cash" wording before this ships.

## 4. Scope — the sites that convert (non-USD only)

| Area                           | Sites                                                                                                                                                                       |
| ------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Credit balance                 | `billing/page.tsx:41` (`fmtUSD`), `CanvasPage.tsx:579`, `settings/sections/Billing.tsx:85`, `WorkflowsPage.tsx:222`, `LowBalanceBanner.tsx:29`, `CheckoutModal.tsx:171,185` |
| Metered spend                  | `usage/AreaChart.tsx:225`, usage totals in `UsagePage.tsx`                                                                                                                  |
| Platform tier fee              | `Inspector.tsx:693`                                                                                                                                                         |
| Tendril credit + machine rates | `TendrilConsolePage.tsx:353,564,574,611,784,788`; `Inspector.tsx:2572,2621,2666`                                                                                            |
| Settings limit hints           | `settings/sections/Execution.tsx:56,90`; threshold in `settings/sections/Billing.tsx`                                                                                       |

**Excluded:** `Receipt.tsx` (tax document, INR), every `₹` checkout site,
`Inspector.tsx` price/asset fields (USDC), `PurchaseHistory.tsx` (mock data).

## 5. Design

- **Storage stays USD micros.** No ledger migration; conversion at render only.
- **Two rate paths, kept apart.** `payments.FetchINRToUSDRate` stays **uncached
  and fail-closed** — it locks a rate into a `credit_ledger` row, where a stale
  value is a real financial error. `FetchRateTable` is display-only: 12h TTL,
  serve-stale-on-error.
- **USD short-circuits before any rate lookup**, so an FX outage cannot affect a
  default user.
- **On FX failure in non-USD mode, fall back to USD and say so.** A number from an
  unknown rate is worse than an unconverted one.
- **`Intl.NumberFormat(locale, { style: "currency", currency })`** for non-USD;
  the USD branch reproduces today's output exactly. JPY has no minor units.
- **Shortlist:** USD, INR, EUR, GBP, JPY, AUD, CAD, SGD, AED, CHF.

## 6. Schema

```sql
-- 000021_user_display_currency.up.sql
ALTER TABLE user_settings
    ADD COLUMN IF NOT EXISTS display_currency TEXT NOT NULL DEFAULT 'USD';

ALTER TABLE user_settings ADD CONSTRAINT user_settings_display_currency_valid
    CHECK (display_currency IN
        ('USD','INR','EUR','GBP','JPY','AUD','CAD','SGD','AED','CHF'));
```

`DEFAULT 'USD'` is what makes §0 true for every existing account with no backfill.
Mirrors how `000020` constrains `default_key_mode`.

## 7. Endpoints

| Method  | Path        | Notes                                                                                                                      |
| ------- | ----------- | -------------------------------------------------------------------------------------------------------------------------- |
| `GET`   | `/fx/rates` | `{ base, rates, fetchedAt }`, shortlist only, authed, 12h server cache. Called by the client **only when currency ≠ USD**. |
| `PATCH` | `/settings` | Gains `displayCurrency`, validated in `parseSettingsPatch` reusing the `defaultKeyMode` branch shape                       |

## 8. Commits

| #   | Commit                                                          |
| --- | --------------------------------------------------------------- |
| 1   | `docs: plan the opt-in display currency`                        |
| 2   | `Add display_currency to user settings`                         |
| 3   | `Fetch and cache a multi-currency rate table`                   |
| 4   | `Add GET /fx/rates`                                             |
| 5   | `Add the currency formatting layer` (incl. the USD-no-op tests) |
| 6   | `Add a currency selector to settings`                           |
| 7   | `Show balances in the selected currency`                        |
| 8   | `Convert platform fees, Tendril rates and limit hints`          |
| 9   | `Render the usage chart in the selected currency`               |

Commits 7–9 each keep their USD branch byte-identical; that is the review
checklist for all three.

Reuse: the bounds/timeout/`SetFetchRateForTest` seam in `payments/fx.go`;
`parseSettingsPatch`'s validation shape; the `useSyncExternalStore` module-store
pattern in `lib/credits/store.ts`; `Intl.NumberFormat` as in
`PurchaseHistory.tsx:16`.

**Trap:** currency arrives async from `/settings`. For a non-USD user, rendering
USD first then swapping repeats the null-user bug fixed earlier on this branch —
mirror the last-known currency to `localStorage`, as `lib/credits/store.ts`
already does for its own state. Moot for USD users.

## 9. Not in this change

`lib/credits/fx.ts:4` hardcodes `USD_PER_INR = 1/83`; the live rate is **95.25**,
so the checkout's "≈ $X credits" preview is ~15% optimistic. Nobody is
mischarged — the server recomputes at order time (`payments.go:82`) — but the
pre-payment number is wrong. Fixing it would change the checkout figure for
**every** user, which §0 forbids here. Flagged as its own change, not bundled.

## 10. Verification

**Automated** — `go test ./...`; `npm run typecheck && lint && test`; migration up
**and** down on a scratch DB. New tests: the USD no-op across the value range;
rate-table parsing and bounds rejection; `formatMoney` for INR/JPY (zero-decimal)
and a missing-rate fallback; `PATCH /settings` rejecting an unlisted code.

**Live in the browser**

1. **Before touching the setting:** every page identical to before, and no
   `/fx/rates` request (confirm via network inspection).
2. EUR → tier fees, Tendril rates, usage totals and chart render in €.
3. Balance in EUR reads `12.50 credits ≈ €10.82 · not redeemable for cash`;
   switching back to USD restores `$12.50` exactly.
4. JPY → `¥1,234`, never `¥1,234.00`.
5. Receipt still INR with the GST split; checkout still charges `₹`.
6. Canvas node prices still read `0.002 USDC` in every currency.
7. Spend ceiling in EUR shows an `≈ $X.XX enforced` hint matching `GET /settings`.
8. FX host blocked in EUR → falls back to USD with a notice, no `NaN`; in USD,
   blocking it changes nothing.
9. Reload in EUR → no USD→EUR flash.
10. 375px and desktop; no horizontal overflow.

## 11. Risks

| Risk                                             | Mitigation                                                                        |
| ------------------------------------------------ | --------------------------------------------------------------------------------- |
| The feature leaks into the default experience    | §0 as a tested invariant; USD branch reviewed byte-identical; verification step 1 |
| Policy wording on the fiat estimate              | §3 — flagged for a human owner; only shown to opted-in users                      |
| A cached rate reaches the billing path           | `FetchINRToUSDRate` stays uncached and fail-closed                                |
| A converted figure mistaken for what was charged | Receipts INR, checkout INR, USDC unconverted                                      |
| Zero/three-decimal currencies                    | `Intl.NumberFormat` + explicit JPY test                                           |
| Drift on USD-enforced limit inputs               | Persistent `≈ $X.XX enforced` hint                                                |
