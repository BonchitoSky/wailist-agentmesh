import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

const state = vi.hoisted(() => ({
  push: vi.fn(),
  credits: { balanceUSD: 12.5, balanceKnown: true },
}));

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: state.push }) }));
vi.mock("@/lib/credits/store", () => ({ useCredits: () => state.credits }));

import { WorkflowsPhoneHeader } from "./WorkflowsPhoneHeader";

const renderHeader = (
  over: Partial<Parameters<typeof WorkflowsPhoneHeader>[0]> = {},
) =>
  render(<WorkflowsPhoneHeader total={6} shown={6} spend={8.708} {...over} />);

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  state.credits = { balanceUSD: 12.5, balanceKnown: true };
});

describe("WorkflowsPhoneHeader", () => {
  it("summarises the workspace beside the title", () => {
    const { container } = renderHeader();
    expect(screen.getByRole("heading", { name: "Workflows" })).toBeTruthy();
    expect(container.textContent).toContain("6 total");
    expect(container.textContent).toContain("$8.71 spent");
  });

  it("says how many are showing once a filter narrows the list", () => {
    const { container } = renderHeader({ shown: 1, spend: 2.12 });
    expect(container.textContent).toContain("1 of 6");
    expect(container.textContent).not.toContain("6 total");
    // The label replaces the text for a screen reader, so it narrows too.
    expect(
      screen.getByLabelText(
        "1 of 6 workflows shown, $2.12 spent in the last 30 days",
      ),
    ).toBeTruthy();
  });

  // Carried over from CreditStrip, which this header replaces.
  it("shows the credit balance, labelled so it is not read as spend", () => {
    const { container } = renderHeader();
    expect(container.textContent).toContain("$12.50");
    expect(screen.getByLabelText("Credit balance $12.50")).toBeTruthy();
  });

  it("shows a dash, not $0.00, until the balance is known", () => {
    state.credits = { balanceUSD: 0, balanceKnown: false };
    const { container } = renderHeader();
    expect(container.textContent).toContain("—");
    expect(container.textContent).not.toContain("$0.00");
  });

  // A bare "+" did not say it adds credits, so the button says it in words.
  it("labels the add-credits button in words and opens billing", () => {
    renderHeader();
    const button = screen.getByRole("button", { name: "Add credits" });
    expect(button.textContent).toBe("Add credits");
    expect(button.hasAttribute("aria-label")).toBe(false);
    fireEvent.click(button);
    expect(state.push).toHaveBeenCalledWith("/billing");
  });
});
