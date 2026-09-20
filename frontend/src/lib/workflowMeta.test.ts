import { describe, expect, it } from "vitest";
import type { Workflow } from "./types";
import {
  formatDollars,
  spendDollars,
  totalSpend,
  workflowAriaLabel,
  workflowMeta,
} from "./workflowMeta";

const NOW = Date.parse("2026-09-20T12:00:00.000Z");
const at = (ms: number) => new Date(NOW + ms).toISOString();

function wf(over: Partial<Workflow> = {}): Workflow {
  return {
    id: "w1",
    name: "Daily Market Brief",
    nodes: [],
    edges: [],
    status: "deployed",
    runs: 38,
    spend: "1.48",
    ...over,
  };
}

// The line reads "$1.48 · next in 2 h", and its last part is the only one
// that changes colour.
describe("workflowMeta", () => {
  it("ends a scheduled workflow with the time until its next run", () => {
    const m = workflowMeta(wf({ scheduleNextRunAt: at(2 * 3_600_000) }), NOW);
    expect(m.spent).toBe("$1.48");
    expect(m.state).toEqual({ text: "next in 2 h", tone: "accent" });
    expect(m.draft).toBe(false);
  });

  it("says a run already due is due now, never 'next now'", () => {
    const m = workflowMeta(wf({ scheduleNextRunAt: at(-30_000) }), NOW);
    expect(m.state).toEqual({ text: "due now", tone: "accent" });
  });

  it("says nothing is queued when a deployed workflow has no schedule", () => {
    const m = workflowMeta(wf({ runs: 1842, spend: "4.218" }), NOW);
    expect(m.spent).toBe("$4.22");
    expect(m.state).toEqual({ text: "no run queued", tone: "dim" });
  });

  it("falls back to no run queued when the next run is unreadable", () => {
    const m = workflowMeta(wf({ scheduleNextRunAt: "soon" }), NOW);
    expect(m.state).toEqual({ text: "no run queued", tone: "dim" });
  });

  it("names paused and error states in their own tones", () => {
    expect(
      workflowMeta(wf({ status: "paused", spend: "0.89" }), NOW).state,
    ).toEqual({ text: "paused", tone: "warm" });
    expect(workflowMeta(wf({ status: "error" }), NOW).state).toEqual({
      text: "error",
      tone: "danger",
    });
  });

  it("describes a workflow that never ran as a draft, without figures", () => {
    const m = workflowMeta(
      wf({ status: "draft", runs: 0, spend: undefined }),
      NOW,
    );
    expect(m.draft).toBe(true);
  });

  it("still shows the figures of a draft that has run before", () => {
    const m = workflowMeta(wf({ status: "draft", runs: 4 }), NOW);
    expect(m.draft).toBe(false);
    expect(m.state).toEqual({ text: "not deployed", tone: "dim" });
  });

  // The count named no period, so it read as neither a rate nor a total, and
  // the website's Usage page answers that question properly.
  it("carries no run count", () => {
    expect(workflowMeta(wf({ runs: 1842 }), NOW)).not.toHaveProperty("runs");
  });

  // `draft` means "never ran", not "status is draft", so it still reads the
  // count even though nothing prints it. Removing the read would relabel a
  // draft that has runs behind it.
  it("still reads the count to tell a never-run draft from a stopped one", () => {
    expect(workflowMeta(wf({ status: "draft", runs: 0 }), NOW).draft).toBe(
      true,
    );
    expect(
      workflowMeta(wf({ status: "draft", runs: undefined }), NOW).draft,
    ).toBe(true);
    expect(workflowMeta(wf({ status: "draft", runs: 1 }), NOW).draft).toBe(
      false,
    );
  });

  it("treats a missing or unreadable spend as zero", () => {
    expect(workflowMeta(wf({ spend: undefined }), NOW).spent).toBe("$0");
    expect(workflowMeta(wf({ spend: "" }), NOW).spent).toBe("$0");
  });

  it("calls the legacy active status deployed", () => {
    expect(workflowMeta(wf({ status: "active" }), NOW).statusWord).toBe(
      "deployed",
    );
  });
});

describe("spendDollars and totalSpend", () => {
  it("adds up the sample workspace to $8.71", () => {
    const list = [
      wf({ spend: "4.218" }),
      wf({ spend: "1.48" }),
      wf({ spend: "0.89" }),
      wf({ spend: undefined }),
      wf({ spend: "2.12" }),
      wf({ spend: undefined }),
    ];
    expect(formatDollars(totalSpend(list))).toBe("$8.71");
  });

  it("reads a dollar string, and zero when there is none", () => {
    expect(spendDollars("4.218")).toBeCloseTo(4.218);
    expect(spendDollars(undefined)).toBe(0);
    expect(spendDollars("nope")).toBe(0);
  });
});

// The rail carries the status as colour alone, so the label has to say it.
describe("workflowAriaLabel", () => {
  it("names the workflow, its status and its figures", () => {
    const w = wf({
      name: "Customer Support Triage",
      runs: 1842,
      spend: "4.218",
    });
    expect(workflowAriaLabel(w, workflowMeta(w, NOW))).toBe(
      "Customer Support Triage, deployed. $4.22 spent, no run queued.",
    );
  });

  it("does not say paused twice", () => {
    const w = wf({
      name: "Invoice Reconciliation",
      status: "paused",
      runs: 217,
      spend: "0.89",
    });
    expect(workflowAriaLabel(w, workflowMeta(w, NOW))).toBe(
      "Invoice Reconciliation, paused. $0.890 spent.",
    );
  });

  it("keeps a never-run draft short", () => {
    const w = wf({
      name: "Content Pipeline",
      status: "draft",
      runs: 0,
      spend: undefined,
    });
    expect(workflowAriaLabel(w, workflowMeta(w, NOW))).toBe(
      "Content Pipeline, draft. Never run.",
    );
  });
});
