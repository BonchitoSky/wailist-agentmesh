import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

const state = vi.hoisted(() => ({
  native: false,
  readOnly: false,
  openExternal: vi.fn<
    (url: string, options?: { onClose?: () => void }) => Promise<void>
  >(async () => {}),
  refreshBalance: vi.fn(async () => {}),
  refreshPurchases: vi.fn(async () => {}),
}));

vi.mock("@/lib/nativeAuth", () => ({
  get IS_NATIVE() {
    return state.native;
  },
}));
// Still mocked, so a test can prove nothing reaches for it any more.
vi.mock("@/lib/openExternal", () => ({
  WEB_BILLING_URL: "https://www.agent-mesh.app/billing",
  openExternal: state.openExternal,
}));
vi.mock("@/hooks/useReadOnly", () => ({
  useReadOnly: () => state.readOnly,
}));
vi.mock("@/lib/credits/store", () => ({
  useCredits: () => ({
    balanceUSD: 12,
    balanceKnown: true,
    lastPurchase: undefined,
    refreshBalance: state.refreshBalance,
    refreshPurchases: state.refreshPurchases,
    // The phone screen only renders payment history once it has loaded,
    // so the mock has to supply one for Buy again to exist there.
    purchases: [
      {
        id: "p1",
        createdAt: "2026-08-03T10:00:00.000Z",
        amountINR: 500,
        creditsUSD: 5.24,
        method: "cashfree",
        status: "completed",
      },
    ],
    purchasesKnown: true,
  }),
}));
// The page now also reads the workflow list (for 30-day spend) and the
// live FX rate (so the quote matches what the ledger will credit).
vi.mock("@/lib/api", () => ({
  credits: {},
  workflows: { list: vi.fn(async () => []) },
  payments: {
    listProviders: vi.fn(async () => ({
      usd_per_inr: 0.010423,
      providers: [{ id: "cashfree", enabled: true, currency: "INR" }],
    })),
  },
}));
vi.mock("@/components/Topbar", () => ({ Topbar: () => null }));
vi.mock("@/components/checkout/CheckoutModal", () => ({
  CheckoutModal: () => <div>checkout dialog</div>,
}));
vi.mock("@/components/billing/PurchaseHistory", () => ({
  PurchaseHistory: ({
    onBuyAgain,
  }: {
    onBuyAgain: (amountINR: number) => void;
  }) => (
    <button type="button" onClick={() => onBuyAgain(1000)}>
      Buy again
    </button>
  ),
}));

import BillingPage from "./page";

afterEach(() => {
  cleanup();
  state.native = false;
  state.readOnly = false;
  state.openExternal.mockClear();
  state.refreshBalance.mockClear();
  state.refreshPurchases.mockClear();
});

// Paying used to leave the app for the website, because the native CSP blocked
// the payment SDK in the WebView. The policy now admits it (lib/csp.ts), so
// every client checks out in place and there is no second path to keep working.
describe("BillingPage in the Android app", () => {
  it("checks out in the app, and never opens the website", () => {
    state.native = true;
    state.readOnly = true;
    render(<BillingPage />);

    expect(
      screen.queryByRole("button", { name: /Add credits on the website/ }),
    ).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: /^Pay / }));

    expect(screen.getByText("checkout dialog")).toBeTruthy();
    expect(state.openExternal).not.toHaveBeenCalled();
  });

  it("sends Buy again to the checkout dialog, not the website", () => {
    state.native = true;
    state.readOnly = true;
    render(<BillingPage />);

    fireEvent.click(screen.getByRole("button", { name: "Buy again" }));

    expect(screen.getByText("checkout dialog")).toBeTruthy();
    expect(state.openExternal).not.toHaveBeenCalled();
  });
});

describe("BillingPage on a phone browser", () => {
  it("gets the same screen and the same checkout as the app", () => {
    state.readOnly = true;
    render(<BillingPage />);

    expect(screen.getByRole("button", { name: /^Pay / })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Buy again" }));
    expect(screen.getByText("checkout dialog")).toBeTruthy();
  });
});

describe("BillingPage on the web", () => {
  it("keeps the amount picker, and Buy again opens checkout", () => {
    render(<BillingPage />);

    expect(screen.getByText("Choose an amount")).toBeTruthy();
    expect(
      screen.queryByRole("button", { name: /Add credits on the website/ }),
    ).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Buy again" }));

    expect(screen.getByText("checkout dialog")).toBeTruthy();
    expect(state.openExternal).not.toHaveBeenCalled();
  });
});
