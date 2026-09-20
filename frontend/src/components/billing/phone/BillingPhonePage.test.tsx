import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

vi.mock("@/components/billing/PurchaseHistory", () => ({
  PurchaseHistory: () => <div data-testid="history" />,
}));

import { BillingPhonePage, type BillingPhoneProps } from "./BillingPhonePage";

const noop = () => {};

function renderPage(over: Partial<BillingPhoneProps> = {}) {
  const props: BillingPhoneProps = {
    balanceUSD: 12.5,
    balanceKnown: true,
    isLow: false,
    returnState: null,
    presets: [1000, 5000],
    amountINR: 5000,
    onPreset: vi.fn(),
    customINR: "",
    onCustomChange: noop,
    effectiveINR: 5000,
    overMax: false,
    maxINR: 100000,
    canCheckout: true,
    onCheckout: vi.fn(),
    couponCode: "",
    onCouponChange: noop,
    couponState: "idle",
    couponMessage: "",
    onApplyCoupon: vi.fn(),
    onBuyAgain: noop,
    howItWorks: ["Credits are spent as your agents call paid tools."],
    ...over,
  };
  const utils = render(<BillingPhonePage {...props} />);
  return { ...utils, props };
}

afterEach(cleanup);

describe("BillingPhonePage", () => {
  it("leads with the balance and says the account is active", () => {
    const { container } = renderPage();
    expect(screen.getByText("$12.50")).toBeTruthy();
    expect(screen.getByText("Credit balance")).toBeTruthy();
    expect(screen.getByText("Active")).toBeTruthy();
    expect(
      container.querySelector(".bilp-head")?.getAttribute("data-state"),
    ).toBe("ok");
  });

  it("warns when the balance is low, which is why colour is here at all", () => {
    const { container } = renderPage({ isLow: true, balanceUSD: 1.2 });
    expect(screen.getByText(/Low — a run may stop/)).toBeTruthy();
    expect(
      container.querySelector(".bilp-head")?.getAttribute("data-state"),
    ).toBe("low");
  });

  it("shows a dash, not $0.00, until the balance is known", () => {
    const { container } = renderPage({ balanceKnown: false, balanceUSD: 0 });
    expect(screen.getByText("—")).toBeTruthy();
    expect(screen.getByText("Checking…")).toBeTruthy();
    expect(container.textContent).not.toContain("$0.00");
  });

  // The whole point of this screen: the payment happens here. There is no
  // longer a branch that sends the app to the website.
  it("pays in place, and never offers to leave for the website", () => {
    const onCheckout = vi.fn();
    const { container } = renderPage({ onCheckout });
    fireEvent.click(screen.getByRole("button", { name: /^Pay / }));
    expect(onCheckout).toHaveBeenCalledTimes(1);
    expect(container.textContent).not.toMatch(/website|browser tab/i);
  });

  it("says what the Pay button will charge", () => {
    renderPage({ effectiveINR: 5000 });
    expect(screen.getByRole("button", { name: "Pay ₹5,000" })).toBeTruthy();
  });

  it("marks the chosen amount and reports the choice", () => {
    const onPreset = vi.fn();
    renderPage({ onPreset });
    expect(
      screen
        .getByRole("button", { name: "₹5,000" })
        .getAttribute("aria-pressed"),
    ).toBe("true");
    fireEvent.click(screen.getByRole("button", { name: "₹1,000" }));
    expect(onPreset).toHaveBeenCalledWith(1000);
  });

  // Boxes are what made this screen look heavy. None of the amounts,
  // fields or section wrappers may carry a border of its own.
  it("draws no boxes around the amounts or the fields", () => {
    const { container } = renderPage();
    expect(container.querySelector(".bilp-balance")).toBeNull();
    expect(container.querySelector(".bilp-preset")).toBeNull();
    // Exactly one filled control: the Pay button.
    expect(container.querySelectorAll(".bilp-pay")).toHaveLength(1);
  });

  it("refuses an amount over the maximum, and says the maximum", () => {
    renderPage({ overMax: true, canCheckout: false, maxINR: 100000 });
    expect(screen.getByRole("alert").textContent).toContain("₹1,00,000");
    expect(screen.getByRole("button", { name: "Pay" })).toHaveProperty(
      "disabled",
      true,
    );
  });

  it("applies a coupon and reports what came back", () => {
    const onApplyCoupon = vi.fn();
    renderPage({
      couponCode: "WELCOME",
      onApplyCoupon,
      couponState: "success",
      couponMessage: "Coupon applied — $5.00 added to your balance.",
    });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    expect(onApplyCoupon).toHaveBeenCalledTimes(1);
    expect(screen.getByText(/\$5\.00 added/)).toBeTruthy();
  });

  it("cannot apply an empty coupon", () => {
    renderPage({ couponCode: "   " });
    expect(screen.getByRole("button", { name: "Apply" })).toHaveProperty(
      "disabled",
      true,
    );
  });

  it("reports the outcome of a redirect checkout", () => {
    renderPage({
      returnState: { tone: "pending", message: "Payment submitted." },
    });
    expect(screen.getByRole("status").textContent).toContain(
      "Payment submitted.",
    );
  });

  it("shows purchase history and how credits work", () => {
    renderPage();
    expect(screen.getByTestId("history")).toBeTruthy();
    // PurchaseHistory has its own heading; this screen must not add a second.
    expect(screen.queryByText("Recent purchases")).toBeNull();
    expect(screen.getByText(/agents call paid tools/)).toBeTruthy();
  });
});
