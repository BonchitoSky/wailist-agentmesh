import type { LogEvent } from "../useRunTranscript";
import { isX402Payment } from "../useRunTranscript";

// Turns a run's raw log events into the one thing a non-technical reader
// actually wants: the agent's answer, plus a one-line summary of what it cost.
//
// This exists because the answer was already in the transcript and was being
// shown as a collapsed "▸ response · 1.2 KB" toggle -- correct for a debugger,
// useless for someone who just asked a question. Everything here is a pure
// function of the logs so it can be unit-tested without a live run.

export interface RunSummary {
  /** The prose to show in the assistant bubble. */
  text: string;
  /** True when the run failed; the bubble renders in the danger tone. */
  isError: boolean;
  /** Successful tool / tool402 steps, for the activity strip. */
  toolCount: number;
  /** Total settled spend in USD across every x402 payment in the run. */
  spendUSD: number;
}

/**
 * Pull display text out of one node's output.
 *
 * Agent outputs come in three shapes, all produced by ExecuteAgent:
 *   - a bare string (BYOK, no payments)
 *   - { message, x402Payments? }
 *   - { message, x402Payments?, platformKeyUsage } (platform key)
 * so `.message` is checked before falling back to the whole payload.
 */
function textFromOutput(output: unknown): string {
  if (output === null || output === undefined) return "";
  if (typeof output === "string") return output;
  if (typeof output === "object") {
    const rec = output as Record<string, unknown>;
    for (const key of ["message", "text", "output", "answer"]) {
      const v = rec[key];
      if (typeof v === "string" && v.trim() !== "") return v;
    }
    // A paid step nests the endpoint's own body under `response`; only worth
    // showing once the agent-shaped keys above have all missed.
    if (typeof rec.response === "string") return rec.response;
    try {
      return JSON.stringify(output, null, 2);
    } catch {
      return "";
    }
  }
  return String(output);
}

/** Best-effort human-readable error out of a failed step's output. */
function errorFromOutput(output: unknown): string {
  if (typeof output === "string" && output.trim() !== "") return output;
  if (output && typeof output === "object") {
    const rec = output as Record<string, unknown>;
    for (const key of ["error", "message", "reason"]) {
      const v = rec[key];
      if (typeof v === "string" && v.trim() !== "") return v;
    }
  }
  return "The run failed without returning a reason.";
}

/** Settled USD for one log event, 0 when the step paid for nothing. */
function spendOf(log: LogEvent): number {
  if (!isX402Payment(log.output)) return 0;
  const micros = (log.output as { settledUsdMicros?: unknown })
    .settledUsdMicros;
  return typeof micros === "number" ? micros / 1e6 : 0;
}

export function resolveReply(logs: LogEvent[]): RunSummary {
  const toolCount = logs.filter(
    (l) =>
      (l.nodeType === "tool" || l.nodeType === "tool402") &&
      l.status === "success",
  ).length;
  const spendUSD = logs.reduce((sum, l) => sum + spendOf(l), 0);

  // A failure is the headline: showing an earlier node's partial output as if
  // it were the answer would be a lie about what happened.
  const failed = [...logs].reverse().find((l) => l.status === "failed");
  if (failed) {
    return {
      text: errorFromOutput(failed.output),
      isError: true,
      toolCount,
      spendUSD,
    };
  }

  // The agent's answer is the reply whenever there is one. Search backwards:
  // a workflow with several agents ends on the last one to speak.
  const agent = [...logs]
    .reverse()
    .find((l) => l.nodeType === "agent" && l.status === "success");
  if (agent) {
    const text = textFromOutput(agent.output);
    if (text.trim() !== "") {
      return { text, isError: false, toolCount, spendUSD };
    }
  }

  // No agent ran (or it answered with nothing) -- fall back to whatever the
  // last successful step produced, so a non-agent workflow still says
  // something rather than rendering an empty bubble.
  const last = [...logs].reverse().find((l) => l.status === "success");
  const fallback = last ? textFromOutput(last.output) : "";
  return {
    text: fallback.trim() === "" ? "The run finished." : fallback,
    isError: false,
    toolCount,
    spendUSD,
  };
}
