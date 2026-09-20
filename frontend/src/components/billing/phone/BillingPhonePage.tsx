"use client";
import { IconArrow } from "@/components/ui";
import { PurchaseHistory } from "@/components/billing/PurchaseHistory";
import { creditsForTopup } from "@/lib/credits/fx";

// The Credits screen on a phone.
//
// It exists because a run that stops for want of credit is the one thing this
// app has to be able to fix from outside, and the desktop page answers that in
// a two-column layout of panels inside panels. Here the balance leads, the way
// to add more is directly under it, and everything else is a flat section
// under a hairline -- the same shape as the Workflows list.
//
// Paying still happens on the website in the Android app: the content security
// policy blocks the payment provider's script in a WebView. A phone browser
// has no such limit and gets the real checkout, which is why both branches
// live here.

const fmtUSD = (n: number) => `$${n.toFixed(2)}`;

export interface BillingPhoneProps {
  balanceUSD: number;
  balanceKnown: boolean;
  isLow: boolean;
  returnState: { tone: "pending" | "error"; message: string } | null;
  /** The Android app pays on the website; a phone browser checks out here. */
  native: boolean;
  onTopUpOnWeb: () => void;
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
  return (
    <main className="bilp-page">
      <h1 className="bilp-title">Credits</h1>

      <section className="bilp-balance" data-state={state}>
        <span className="bilp-balance__label">Credit balance</span>
        <span className="bilp-balance__row">
          <span className="bilp-balance__amount">
            {p.balanceKnown ? fmtUSD(p.balanceUSD) : "—"}
          </span>
          <span className="bilp-balance__state">
            <span className="bilp-balance__dot" aria-hidden />
            {state === "unknown"
              ? "Checking…"
              : state === "low"
                ? "Low balance"
                : "Active"}
          </span>
        </span>
      </section>

      {p.returnState && (
        <p className="bilp-note" data-tone={p.returnState.tone} role="status">
          {p.returnState.message}
        </p>
      )}

      <section className="bilp-section">
        <h2 className="bilp-heading">Top up</h2>
        {p.native ? (
          <NativeTopUp onOpen={p.onTopUpOnWeb} />
        ) : (
          <WebCheckout {...p} />
        )}
      </section>

      <section className="bilp-section">
        <h2 className="bilp-heading">Coupon</h2>
        <div className="bilp-coupon">
          <input
            className="bilp-input"
            placeholder="Coupon code"
            aria-label="Coupon code"
            autoCapitalize="characters"
            autoCorrect="off"
            value={p.couponCode}
            onChange={(e) => p.onCouponChange(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && p.onApplyCoupon()}
          />
          <button
            type="button"
            className="bilp-btn"
            onClick={p.onApplyCoupon}
            disabled={!p.couponCode.trim() || p.couponState === "loading"}
          >
            {p.couponState === "loading" ? "Applying…" : "Apply"}
          </button>
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

// The Android app. One button, and a sentence saying where it goes and when
// the balance here catches up -- both questions someone standing outside with
// a stalled run will ask.
function NativeTopUp({ onOpen }: { onOpen: () => void }) {
  return (
    <>
      <button type="button" className="bilp-cta" onClick={onOpen}>
        Add credits on the website
        <IconArrow size={13} />
      </button>
      <p className="bilp-hint">
        Opens agent-mesh.app in a browser tab. Pay by card, UPI or crypto,
        signing in with this account if asked. Your balance here updates when
        you close the tab; crypto shows once it confirms.
      </p>
    </>
  );
}

function WebCheckout(p: BillingPhoneProps) {
  const credits = creditsForTopup(p.canCheckout ? p.effectiveINR : 0);
  return (
    <>
      <div className="bilp-presets">
        {p.presets.map((inr) => {
          const selected = !p.customINR && p.amountINR === inr;
          return (
            <button
              key={inr}
              type="button"
              className="bilp-preset"
              aria-pressed={selected}
              onClick={() => p.onPreset(inr)}
            >
              <span className="bilp-preset__amount">
                ₹{inr.toLocaleString("en-IN")}
              </span>
              <span className="bilp-preset__usd">
                ≈ {fmtUSD(creditsForTopup(inr))}
              </span>
            </button>
          );
        })}
      </div>

      <input
        className="bilp-input"
        inputMode="decimal"
        placeholder="Custom amount in ₹"
        aria-label="Custom amount in rupees"
        value={p.customINR}
        onChange={(e) => p.onCustomChange(e.target.value)}
      />

      {p.overMax ? (
        <p className="bilp-note" data-tone="error" role="alert">
          The most you can add at once is ₹{p.maxINR.toLocaleString("en-IN")}.
        </p>
      ) : (
        <p className="bilp-hint">
          {p.canCheckout
            ? `Adds ${fmtUSD(credits)} of credit.`
            : "Top-ups of ₹1000 or more earn 5% bonus credits."}
        </p>
      )}

      <button
        type="button"
        className="bilp-cta"
        onClick={p.onCheckout}
        disabled={!p.canCheckout}
      >
        Continue to checkout
        <IconArrow size={13} />
      </button>
    </>
  );
}
