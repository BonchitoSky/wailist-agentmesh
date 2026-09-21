import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { buildUsage } from "@/lib/data";

const state = vi.hoisted(() => ({ readOnly: false }));

vi.mock("@/hooks/useReadOnly", () => ({ useReadOnly: () => state.readOnly }));
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }));
vi.mock("@/components/Topbar", () => ({ Topbar: () => null }));
vi.mock("./phone/UsagePhonePage", () => ({
  UsagePhonePage: () => <div>phone usage</div>,
}));
vi.mock("@/lib/api", () => {
  const u = buildUsage("30d");
  return {
    usage: {
      invalidate: () => {},
      summary: async () => u.summary,
      timeseries: async () => u.timeseries,
      byWorkflow: async () => u.byWorkflow,
      byEndpoint: async () => u.byEndpoint,
      settlements: async () => u.settlements,
    },
  };
});

import { UsagePage } from "./UsagePage";

afterEach(() => {
  cleanup();
  state.readOnly = false;
});

// The desktop page is two wide tables that only scroll sideways on a phone,
// so a phone -- and the Android app -- gets its own screen.
describe("UsagePage", () => {
  it("gives a phone its own screen", () => {
    state.readOnly = true;
    render(<UsagePage />);
    expect(screen.getByText("phone usage")).toBeTruthy();
  });
});
