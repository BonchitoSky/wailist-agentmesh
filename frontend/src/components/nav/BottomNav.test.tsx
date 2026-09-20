import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

const state = vi.hoisted(() => ({ handheld: true, pathname: "/workflows" }));

vi.mock("@/hooks/useIsHandheld", () => ({
  useIsHandheld: () => state.handheld,
}));
vi.mock("next/navigation", () => ({ usePathname: () => state.pathname }));

import { BottomNav } from "./BottomNav";

afterEach(() => {
  cleanup();
  state.handheld = true;
  state.pathname = "/workflows";
  document.body.removeAttribute("data-bottomnav");
});

describe("BottomNav", () => {
  it("offers Workflows, Activity, Credits and Account on a handheld", () => {
    state.pathname = "/activity";
    render(<BottomNav />);

    const links = screen.getAllByRole("link");
    expect(links.map((l) => l.textContent)).toEqual([
      "Workflows",
      "Activity",
      "Credits",
      "Account",
    ]);
    expect(links.map((l) => l.getAttribute("href"))).toEqual([
      "/workflows",
      "/activity",
      "/billing",
      "/account",
    ]);
    expect(
      screen
        .getByRole("link", { name: "Activity" })
        .getAttribute("aria-current"),
    ).toBe("page");
    expect(document.body.hasAttribute("data-bottomnav")).toBe(true);
  });

  // Reaching /billing from the Workflows "+" used to land on a screen with no
  // bottom bar and no back button. It is a tab root now, so the bar is there.
  it("shows on the Credits tab, so billing is never a dead end", () => {
    state.pathname = "/billing";
    render(<BottomNav />);
    expect(
      screen
        .getByRole("link", { name: "Credits" })
        .getAttribute("aria-current"),
    ).toBe("page");
    expect(document.body.hasAttribute("data-bottomnav")).toBe(true);
  });

  it("no longer offers the Bazaar", () => {
    render(<BottomNav />);
    expect(screen.queryByRole("link", { name: "Bazaar" })).toBeNull();
  });

  it("shows on the Account tab too", () => {
    state.pathname = "/account";
    render(<BottomNav />);
    expect(
      screen
        .getByRole("link", { name: "Account" })
        .getAttribute("aria-current"),
    ).toBe("page");
  });

  it("hides on a pushed screen such as a workflow", () => {
    state.pathname = "/workflows/wf-1";
    render(<BottomNav />);
    expect(screen.queryByRole("navigation")).toBeNull();
    expect(document.body.hasAttribute("data-bottomnav")).toBe(false);
  });

  it("hides on a desktop", () => {
    state.handheld = false;
    render(<BottomNav />);
    expect(screen.queryByRole("navigation")).toBeNull();
  });
});
