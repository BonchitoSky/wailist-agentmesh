import { describe, it, expect } from "vitest";
import { resolveReply } from "./resolveReply";
import type { LogEvent } from "../useRunTranscript";

// Minimal log-event factory: every test only cares about a few fields, and
// spelling out all seven each time buries the case being made.
function log(partial: Partial<LogEvent>): LogEvent {
  return {
    stepIndex: 0,
    nodeId: "n1",
    nodeType: "agent",
    status: "success",
    output: null,
    durationMs: 0,
    ts: "2026-08-07T09:00:00.000Z",
    ...partial,
  };
}

describe("resolveReply", () => {
  it("reads a bare-string agent answer (the BYOK shape)", () => {
    const r = resolveReply([log({ output: "Gold is about $2,640/oz." })]);
    expect(r.text).toBe("Gold is about $2,640/oz.");
    expect(r.isError).toBe(false);
  });

  it("prefers .message on the paid/platform-key agent shape", () => {
    const r = resolveReply([
      log({
        output: {
          message: "Gold is about $2,640/oz.",
          x402Payments: [{ txId: "abc" }],
          platformKeyUsage: { tier: "pro", tokensIn: 10, tokensOut: 20 },
        },
      }),
    ]);
    expect(r.text).toBe("Gold is about $2,640/oz.");
  });

  it("takes the last agent to speak when several ran", () => {
    const r = resolveReply([
      log({ stepIndex: 0, nodeId: "a1", output: "first" }),
      log({ stepIndex: 1, nodeId: "a2", output: "second" }),
    ]);
    expect(r.text).toBe("second");
  });

  it("reports a failure instead of an earlier partial answer", () => {
    const r = resolveReply([
      log({ stepIndex: 0, nodeId: "a1", output: "partial thought" }),
      log({
        stepIndex: 1,
        nodeId: "t1",
        nodeType: "tool402",
        status: "failed",
        output: { error: "endpoint returned 502" },
      }),
    ]);
    expect(r.isError).toBe(true);
    expect(r.text).toBe("endpoint returned 502");
  });

  it("never leaves a failed run without a reason", () => {
    const r = resolveReply([log({ status: "failed", output: null })]);
    expect(r.isError).toBe(true);
    expect(r.text).toBe("The run failed without returning a reason.");
  });

  it("counts only successful tool steps", () => {
    const r = resolveReply([
      log({ stepIndex: 0, nodeId: "t1", nodeType: "tool402" }),
      log({ stepIndex: 1, nodeId: "t2", nodeType: "tool" }),
      log({ stepIndex: 2, nodeId: "t3", nodeType: "tool", status: "stopped" }),
      log({ stepIndex: 3, nodeId: "a1", output: "done" }),
    ]);
    expect(r.toolCount).toBe(2);
  });

  it("sums settled spend across x402 payments, in USD", () => {
    const r = resolveReply([
      log({
        stepIndex: 0,
        nodeId: "t1",
        nodeType: "tool402",
        output: { txId: "aa", settledUsdMicros: 4200 },
      }),
      log({
        stepIndex: 1,
        nodeId: "t2",
        nodeType: "tool402",
        output: { txId: "bb", settledUsdMicros: 1800 },
      }),
      log({ stepIndex: 2, nodeId: "a1", output: "done" }),
    ]);
    expect(r.spendUSD).toBeCloseTo(0.006, 6);
  });

  it("treats a payment with no settled amount as free rather than NaN", () => {
    const r = resolveReply([
      log({ nodeId: "t1", nodeType: "tool402", output: { txId: "aa" } }),
    ]);
    expect(r.spendUSD).toBe(0);
  });

  it("falls back to the last successful step when no agent ran", () => {
    const r = resolveReply([
      log({ nodeId: "x1", nodeType: "action", output: "posted to slack" }),
    ]);
    expect(r.text).toBe("posted to slack");
    expect(r.isError).toBe(false);
  });

  it("says something rather than rendering an empty bubble", () => {
    const r = resolveReply([log({ output: "" })]);
    expect(r.text).toBe("The run finished.");
  });

  it("handles an empty transcript", () => {
    const r = resolveReply([]);
    expect(r.text).toBe("The run finished.");
    expect(r.toolCount).toBe(0);
    expect(r.spendUSD).toBe(0);
  });

  it("serialises a structured answer that has no text-like key", () => {
    const r = resolveReply([log({ output: { rows: [1, 2] } })]);
    expect(r.text).toContain("rows");
  });
});
