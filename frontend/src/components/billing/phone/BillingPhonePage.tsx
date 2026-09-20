"use client";
import { PurchaseHistory } from "@/components/billing/PurchaseHistory";
import { creditsForTopup } from "@/lib/credits/fx";

// The Credits screen on a phone.
//
// It exists because a run that stops for want of credit is the one thing this
// app has to be able to fix from outside, and the desktop page answers that in
// a two-column layout of panels inside panels.
//
// Boxes are the thing this screen deliberately does not have. The balance is a
// bare figure on the page rather than a card; the amounts are text in a row
// rather than four bordered tiles; the two fields are underlined rather than
// boxed. One filled control, the Pay button, because that is the single thing
// you came here to do. Sections are told apart by a hairline and a small
// heading, which is how the Workflows list reads.

const fmtUSD = (n: number) => `$${n.toFixed(2)}`;
const fmtINR = (n: number) => `₹${n.toLocaleString("en-IN")}`;

export interface BillingPhoneProps {
  balanceUSD: number;
  balanceKnown: boolean;
  isLow: boolean;
  returnState: { tone: "pending" | "error"; message: string } | null;
  presets: readonly number[];
  amountINR: number;
  onPreset: (inr: number) => void;
  customINR: string;
  onCustomChange: (value: string) => void;
  effectiveINR: number;
  overMax: boolean;
  maxINR: number;
  canCheckout: boolean;
  onCheckout: () => void;
  couponCode: string;
  onCouponChange: (value: string) => void;
  couponState: "idle" | "loading" | "success" | "error";
  couponMessage: string;
  onApplyCoupon: () => void;
  onBuyAgain: (amountINR: number) => void;
  howItWorks: readonly string[];
}

export function BillingPhonePage(p: BillingPhoneProps) {
  const state = !p.balanceKnown ? "unknown" : p.isLow ? "low" : "ok";
  const credits = creditsForTopup(p.canCheckout ? p.effectiveINR : 0);
  return (
    <main className="bilp-page">
      {/* The balance is the page's headline, not a card on it. */}
      <header className="bilp-head" data-state={state}>
        <span className="bilp-head__label">Credit balance</span>
        <span className="bilp-head__amount">
          {p.balanceKnown ? fmtUSD(p.balanceUSD) : "—"}
        </span>
        <span className="bilp-head__state">
          {state === "unknown"
            ? "Checking…"
            : state === "low"
              ? "Low — a run may stop"
              : "Active"}
        </span>
      </header>

      {p.returnState && (
        <p className="bilp-note" data-tone={p.returnState.tone} role="status">
          {p.returnState.message}
        </p>
      )}

      <section className="bilp-section">
        <h2 className="bilp-heading">Add credit</h2>

        {/* Amounts as text in a row. A chosen one is marked by its colour and
            a rule under it, which needs no box to read as chosen. */}
        <div className="bilp-amounts" role="group" aria-label="Amount">
          {p.presets.map((inr) => (
            <button
              key={inr}
              type="button"
              className="bilp-amount"
              aria-pressed={!p.customINR && p.amountINR === inr}
              onClick={() => p.onPreset(inr)}
            >
              {fmtINR(inr)}
            </button>
          ))}
        </div>

        <label className="bilp-field">
          <span className="bilp-field__label">Or another amount</span>
          <span className="bilp-field__row">
            <span className="bilp-field__prefix" aria-hidden>
              ₹
            </span>
            <input
              className="bilp-field__input"
              inputMode="decimal"
              aria-label="Amount in rupees"
              placeholder={String(p.amountINR)}
              value={p.customINR}
              onChange={(e) => p.onCustomChange(e.target.value)}
            />
          </span>
        </label>

        {p.overMax ? (
          <p className="bilp-note" data-tone="error" role="alert">
            The most you can add at once is {fmtINR(p.maxINR)}.
          </p>
        ) : (
          <p className="bilp-note">
            {p.canCheckout
              ? `Adds ${fmtUSD(credits)} of credit.`
              : `Top-ups of ${fmtINR(1000)} or more earn 5% bonus credits.`}
          </p>
        )}

        <button
          type="button"
          className="bilp-pay"
          onClick={p.onCheckout}
          disabled={!p.canCheckout}
        >
          {p.canCheckout ? `Pay ${fmtINR(p.effectiveINR)}` : "Pay"}
        </button>
        <p className="bilp-note">
          Card, UPI or netbanking, without leaving the app.
        </p>
      </section>

      <section className="bilp-section">
        <h2 className="bilp-heading">Coupon</h2>
        <div className="bilp-field bilp-field--inline">
          <span className="bilp-field__row">
            <input
              className="bilp-field__input"
              placeholder="Code"
              aria-label="Coupon code"
              autoCapitalize="characters"
              autoCorrect="off"
              value={p.couponCode}
              onChange={(e) => p.onCouponChange(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && p.onApplyCoupon()}
            />
            <button
              type="button"
              className="bilp-text-btn"
              onClick={p.onApplyCoupon}
              disabled={!p.couponCode.trim() || p.couponState === "loading"}
            >
              {p.couponState === "loading" ? "Applying…" : "Apply"}
            </button>
          </span>
        </div>
        {p.couponMessage && (
          <p
            className="bilp-note"
            data-tone={p.couponState === "error" ? "error" : "ok"}
            role="status"
          >
            {p.couponMessage}
          </p>
        )}
      </section>

      {/* PurchaseHistory brings its own "Billing history" heading, so this
          section does not add a second one above it. */}
      <section className="bilp-section">
        <PurchaseHistory onBuyAgain={p.onBuyAgain} />
      </section>

      <section className="bilp-section">
        <h2 className="bilp-heading">How credits work</h2>
        <ul className="bilp-facts">
          {p.howItWorks.map((line) => (
            <li key={line}>{line}</li>
          ))}
        </ul>
      </section>
    </main>
  );
}
