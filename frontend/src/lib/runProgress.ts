// A run's progress as milestones: one per node the engine actually runs.
//
// The pieces this is built from already exist and disagree with each other,
// which is the reason for a module rather than a few lines in a component:
//
//   - `describeWorkflow` counts steps by node TYPE (everything but trigger and
//     provider). The engine does not: `runner.go` excludes a node when it is
//     the source of an `attach` edge, which is how a provider and an agent's
//     tools are left out. A trigger DOES run and gets a log row. Counting by
//     type therefore names milestones the run will never reach, and misses one
//     it always does.
//   - `stepIndex` on a log is the topological LEVEL, not a position: every node
//     in a level runs at once and shares the number. It cannot order a list.
//   - Logs arrive one per attempt, and an attached tool that charges gets a
//     synthetic row of its own, keyed by a node that is not a step.
//
// So the order comes from the graph, and the logs only say how far it got.
import type { Workflow, WorkflowNode } from "./types";

/**
 * What a milestone can be.
 *
 * "skipped" is a node a finished run never reported: a branch it did not
 * take, or one the engine had no work for. Without it, a run that succeeded
 * sat at "2 of 4" forever, which reads as unfinished.
 */
export type StepState = "pending" | "running" | "done" | "failed" | "skipped";

export interface RunStep {
  id: string;
  name: string;
  state: StepState;
  /** Milliseconds the node took, once it has finished. */
  durationMs?: number;
}

export interface RunProgressSummary {
  steps: RunStep[];
  /** Milestones finished, successfully or not. */
  completed: number;
  total: number;
  /** 0-100, for the bar. */
  percent: number;
  /** The milestone being worked on, if any. */
  current: RunStep | null;
  failed: boolean;
}

/** What a log row has to carry to be placed on a milestone. */
export interface ProgressLog {
  nodeId: string;
  status: string;
  durationMs?: number;
  stepIndex?: number;
}

function nodeName(n: WorkflowNode): string {
  return n.name || n.label || n.template || n.type;
}

/**
 * The nodes a run walks, in the order it reaches them.
 *
 * Kahn's algorithm over `flow` edges, which is what the engine's own
 * topological sort walks. Ties keep the order the nodes were saved in, so the
 * list is stable between reads. A cycle cannot be ordered, and the nodes left
 * in it are appended rather than dropped: a milestone missing from the list
 * would be worse than one in an odd position.
 */
export function workflowSteps(wf: Workflow): { id: string; name: string }[] {
  const nodes = wf.nodes ?? [];
  const edges = wf.edges ?? [];
  // A node that hangs off another one -- a provider, an agent's tools -- is
  // configuration for the node it attaches to, and never a step of its own.
  const attached = new Set(
    edges.filter((e) => e.kind === "attach").map((e) => e.from),
  );
  const steps = nodes.filter((n) => !attached.has(n.id));
  const isStep = new Set(steps.map((n) => n.id));

  const flow = edges.filter(
    (e) => e.kind !== "attach" && isStep.has(e.from) && isStep.has(e.to),
  );
  const waitingFor = new Map<string, number>(steps.map((n) => [n.id, 0]));
  const after = new Map<string, string[]>();
  for (const e of flow) {
    waitingFor.set(e.to, (waitingFor.get(e.to) ?? 0) + 1);
    after.set(e.from, [...(after.get(e.from) ?? []), e.to]);
  }

  const ordered: WorkflowNode[] = [];
  const ready = steps.filter((n) => (waitingFor.get(n.id) ?? 0) === 0);
  while (ready.length) {
    const n = ready.shift()!;
    ordered.push(n);
    for (const nextId of after.get(n.id) ?? []) {
      const left = (waitingFor.get(nextId) ?? 0) - 1;
      waitingFor.set(nextId, left);
      if (left === 0) {
        const next = steps.find((s) => s.id === nextId);
        if (next) ready.push(next);
      }
    }
  }
  // Anything left is in a cycle. Keep it, at the end.
  for (const n of steps) {
    if (!ordered.includes(n)) ordered.push(n);
  }
  return ordered.map((n) => ({ id: n.id, name: nodeName(n) }));
}

/**
 * Folds a run's logs onto its milestones.
 *
 * `runStatus` decides what an unfinished milestone means: while the run is
 * going, the first one without an answer is the one being worked on; once it
 * has stopped, nothing is running any more.
 *
 * The backend publishes a node's log when it FINISHES, not when it starts, so
 * "running" is this reading rather than something the server said. That is
 * also why a failure stops the derivation: after a failed node, the ones
 * behind it never started.
 */
export function runProgress(
  steps: { id: string; name: string }[],
  logs: ProgressLog[],
  runStatus: string,
): RunProgressSummary {
  // One log per node: the newest attempt wins, and rows for anything that is
  // not a milestone (an attached tool's payment row) are ignored.
  const byNode = new Map<string, ProgressLog>();
  const isStep = new Set(steps.map((s) => s.id));
  for (const log of logs) {
    if (!isStep.has(log.nodeId)) continue;
    byNode.set(log.nodeId, log);
  }

  const running = runStatus === "running";
  // A run that ended well has nothing left to do: whatever never reported was
  // not reached, rather than still pending.
  const succeeded = runStatus === "success";
  let firstUnanswered = true;
  let failed = false;

  const placed: RunStep[] = steps.map((step) => {
    const log = byNode.get(step.id);
    const status = log?.status;
    if (status === "success" || status === "degraded") {
      return {
        id: step.id,
        name: step.name,
        state: "done",
        durationMs: log?.durationMs,
      };
    }
    if (status === "failed") {
      failed = true;
      return {
        id: step.id,
        name: step.name,
        state: "failed",
        durationMs: log?.durationMs,
      };
    }
    // No answer yet. The first of these is what the run is working on, as
    // long as it is still going and nothing has failed.
    if (running && !failed && firstUnanswered) {
      firstUnanswered = false;
      return { id: step.id, name: step.name, state: "running" };
    }
    return {
      id: step.id,
      name: step.name,
      state: succeeded ? "skipped" : "pending",
    };
  });

  // Everything that will not happen again counts as behind us, so a finished
  // run reads as finished whether or not every node had work to do.
  const completed = placed.filter(
    (s) => s.state !== "pending" && s.state !== "running",
  ).length;
  const total = placed.length;
  return {
    steps: placed,
    completed,
    total,
    // A run with no steps is finished by definition rather than 0%.
    percent: total === 0 ? 100 : Math.round((completed / total) * 100),
    current: placed.find((s) => s.state === "running") ?? null,
    failed,
  };
}
