import { afterEach, describe, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
} from "@testing-library/react";

const haptics = vi.hoisted(() => ({ tapFeedback: vi.fn(async () => {}) }));
vi.mock("@/native/haptics", () => haptics);

import { RunProgressDock } from "./RunProgressDock";

const STEPS = [
  { id: "t", name: "Trigger" },
  { id: "a", name: "Triage Agent" },
  { id: "n3", name: "Reply" },
];

function show(props: Partial<React.ComponentProps<typeof RunProgressDock>>) {
  const onDetails = vi.fn();
  const onRunAgain = vi.fn();
  const onDismiss = vi.fn();
  const view = render(
    <RunProgressDock
      steps={STEPS}
      logs={[]}
      runStatus="running"
      onDetails={onDetails}
      onRunAgain={onRunAgain}
      onDismiss={onDismiss}
      {...props}
    />,
  );
  return { ...view, onDetails, onRunAgain, onDismiss };
}

const fill = () =>
  (document.querySelector(".run-dock__fill") as HTMLElement).style.width;

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  try {
    window.localStorage.clear();
  } catch {
    // jsdom always has storage; the component copes without it.
  }
});

describe("the run progress dock", () => {
  it("starts empty, on the first step", () => {
    show({});
    expect(screen.getByText("0/3")).toBeTruthy();
    expect(screen.getByText("Trigger")).toBeTruthy();
    expect(fill()).toBe("0%");
  });

  it("fills as each node answers", () => {
    const { rerender } = show({
      logs: [{ nodeId: "t", status: "success", durationMs: 40 }],
    });
    expect(screen.getByText("1/3")).toBeTruthy();
    expect(fill()).toBe("33%");
    // The next node is the one being worked on.
    expect(screen.getByText("Triage Agent")).toBeTruthy();

    rerender(
      <RunProgressDock
        steps={STEPS}
        logs={[
          { nodeId: "t", status: "success" },
          { nodeId: "a", status: "success" },
        ]}
        runStatus="running"
        onDetails={vi.fn()}
        onRunAgain={vi.fn()}
        onDismiss={vi.fn()}
      />,
    );
    expect(screen.getByText("2/3")).toBeTruthy();
    expect(fill()).toBe("67%");
  });

  it("lists every milestone when expanded, with what each took", () => {
    show({ logs: [{ nodeId: "t", status: "success", durationMs: 1500 }] });
    expect(screen.queryByRole("list")).toBeNull();

    fireEvent.click(screen.getByRole("button", { expanded: false }));
    const items = screen.getAllByRole("listitem");
    expect(items).toHaveLength(3);
    expect(items[0].getAttribute("data-state")).toBe("done");
    expect(items[0].textContent).toContain("1.5s");
    expect(items[1].getAttribute("data-state")).toBe("running");
    expect(items[2].getAttribute("data-state")).toBe("pending");
  });

  it("remembers that it was expanded", () => {
    const first = show({});
    fireEvent.click(screen.getByRole("button", { expanded: false }));
    first.unmount();

    show({});
    expect(screen.getByRole("button", { expanded: true })).toBeTruthy();
  });

  it("names the step that failed, and offers details and another run", () => {
    const { onDetails, onRunAgain } = show({
      logs: [
        { nodeId: "t", status: "success" },
        { nodeId: "a", status: "failed" },
      ],
      runStatus: "failed",
    });

    expect(screen.getByText("Failed at Triage Agent")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Details" }));
    expect(onDetails).toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Run again" }));
    expect(onRunAgain).toHaveBeenCalled();
  });

  it("stays put after a failure instead of dismissing itself", () => {
    vi.useFakeTimers();
    try {
      const { onDismiss } = show({
        logs: [{ nodeId: "t", status: "failed" }],
        runStatus: "failed",
      });
      act(() => {
        vi.advanceTimersByTime(30_000);
      });
      expect(onDismiss).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  it("sees itself out once the run has succeeded", () => {
    vi.useFakeTimers();
    try {
      const { onDismiss } = show({
        logs: STEPS.map((s) => ({ nodeId: s.id, status: "success" })),
        runStatus: "success",
      });
      expect(screen.getByText("Finished")).toBeTruthy();
      expect(fill()).toBe("100%");
      expect(haptics.tapFeedback).toHaveBeenCalledTimes(1);

      expect(onDismiss).not.toHaveBeenCalled();
      act(() => {
        vi.advanceTimersByTime(4000);
      });
      expect(onDismiss).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  // Found on a device: the screen re-renders every poll and passes new
  // inline callbacks, which used to cancel the dismissal timer and leave a
  // finished run docked for good.
  it("still dismisses when the screen re-renders with new callbacks", () => {
    vi.useFakeTimers();
    try {
      const onDismiss = vi.fn();
      const props = {
        steps: STEPS,
        logs: STEPS.map((s) => ({ nodeId: s.id, status: "success" })),
        runStatus: "success",
        onDetails: vi.fn(),
        onRunAgain: vi.fn(),
      };
      const { rerender } = render(
        <RunProgressDock {...props} onDismiss={() => onDismiss()} />,
      );
      // Two more renders, each with a brand-new function, as a poll would.
      act(() => {
        vi.advanceTimersByTime(1000);
      });
      rerender(<RunProgressDock {...props} onDismiss={() => onDismiss()} />);
      act(() => {
        vi.advanceTimersByTime(1000);
      });
      rerender(<RunProgressDock {...props} onDismiss={() => onDismiss()} />);

      act(() => {
        vi.advanceTimersByTime(4000);
      });
      expect(onDismiss).toHaveBeenCalledTimes(1);
      // And the haptic still only fires once, however often it re-renders.
      expect(haptics.tapFeedback).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("says where the run is for a screen reader", () => {
    show({ logs: [{ nodeId: "t", status: "success" }] });
    const dock = document.querySelector(".run-dock")!;
    expect(dock.getAttribute("aria-live")).toBe("polite");
    expect(dock.getAttribute("aria-label")).toBe(
      "Run progress: 1 of 3 steps. Triage Agent",
    );
  });

  // Motion is the one thing this screen adds, so it has to be switchable off.
  it("turns its motion off under reduced motion", () => {
    show({});
    const style = document.querySelector(".run-dock style")!.textContent!;
    expect(style).toContain("@media (prefers-reduced-motion: reduce)");
    expect(style).toMatch(/prefers-reduced-motion[\s\S]*animation: none/);
    expect(style).toMatch(/prefers-reduced-motion[\s\S]*transition: none/);
  });
});
