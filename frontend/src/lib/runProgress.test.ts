import { describe, expect, it } from "vitest";
import { runProgress, workflowSteps } from "./runProgress";
import type { Workflow } from "./types";

// A trigger, an agent with a model provider and a tool attached, then two
// nodes after it. The engine runs the trigger, the agent and those two; the
// provider and the tool hang off the agent and are never steps of their own.
function workflow(): Workflow {
  return {
    id: "wf-1",
    name: "Support triage",
    nodes: [
      { id: "n3", type: "action", name: "Reply", x: 0, y: 0 },
      { id: "t", type: "trigger", template: "manual", x: 0, y: 0 },
      { id: "a", type: "agent", name: "Triage Agent", x: 0, y: 0 },
      { id: "p", type: "provider", name: "Anthropic", x: 0, y: 0 },
      { id: "tool", type: "tool402", name: "Lookup", x: 0, y: 0 },
      { id: "n4", type: "end", name: "Done", x: 0, y: 0 },
    ],
    edges: [
      { id: "e1", from: "t", to: "a", kind: "flow" },
      { id: "e2", from: "a", to: "n3", kind: "flow" },
      { id: "e3", from: "n3", to: "n4", kind: "flow" },
      { id: "e4", from: "p", to: "a", kind: "attach", toPort: "model" },
      { id: "e5", from: "tool", to: "a", kind: "attach", toPort: "tools" },
    ],
  };
}

describe("workflowSteps", () => {
  it("lists the nodes the engine runs, in the order it reaches them", () => {
    expect(workflowSteps(workflow()).map((s) => s.id)).toEqual([
      "t",
      "a",
      "n3",
      "n4",
    ]);
  });

  it("leaves out what is attached to another node", () => {
    const ids = workflowSteps(workflow()).map((s) => s.id);
    // The provider and the agent's tool are configuration, not steps -- the
    // same rule the engine applies (attach edge, not node type).
    expect(ids).not.toContain("p");
    expect(ids).not.toContain("tool");
  });

  it("names a node by what it is called", () => {
    const steps = workflowSteps(workflow());
    expect(steps.map((s) => s.name)).toEqual([
      "manual",
      "Triage Agent",
      "Reply",
      "Done",
    ]);
  });

  it("keeps every node even when the graph has a cycle", () => {
    const wf = workflow();
    wf.edges.push({ id: "e6", from: "n4", to: "t", kind: "flow" });
    expect(workflowSteps(wf)).toHaveLength(4);
  });

  it("copes with a workflow that has no edges", () => {
    const wf = { ...workflow(), edges: [] };
    expect(workflowSteps(wf).map((s) => s.id)).toEqual([
      "n3",
      "t",
      "a",
      "p",
      "tool",
      "n4",
    ]);
  });
});

describe("runProgress", () => {
  const steps = workflowSteps(workflow());

  it("has nothing done before the first log", () => {
    const p = runProgress(steps, [], "running");
    expect(p.completed).toBe(0);
    expect(p.total).toBe(4);
    expect(p.percent).toBe(0);
    // The first milestone is the one being worked on: the server only says a
    // node finished, never that it started.
    expect(p.current?.id).toBe("t");
    expect(p.steps.map((s) => s.state)).toEqual([
      "running",
      "pending",
      "pending",
      "pending",
    ]);
  });

  it("moves along as each node answers", () => {
    const p = runProgress(
      steps,
      [
        { nodeId: "t", status: "success", durationMs: 12 },
        { nodeId: "a", status: "success", durationMs: 5200 },
      ],
      "running",
    );
    expect(p.completed).toBe(2);
    expect(p.percent).toBe(50);
    expect(p.current?.id).toBe("n3");
    expect(p.steps[1].durationMs).toBe(5200);
  });

  it("counts a degraded node as done", () => {
    const p = runProgress(
      steps,
      [{ nodeId: "t", status: "degraded" }],
      "running",
    );
    expect(p.steps[0].state).toBe("done");
    expect(p.completed).toBe(1);
  });

  it("stops at a failure instead of pretending the next one started", () => {
    const p = runProgress(
      steps,
      [
        { nodeId: "t", status: "success" },
        { nodeId: "a", status: "failed" },
      ],
      "failed",
    );
    expect(p.failed).toBe(true);
    expect(p.current).toBeNull();
    expect(p.steps.map((s) => s.state)).toEqual([
      "done",
      "failed",
      "pending",
      "pending",
    ]);
  });

  it("leaves nothing running once the run has stopped", () => {
    const p = runProgress(
      steps,
      [{ nodeId: "t", status: "success" }],
      "stopped",
    );
    expect(p.current).toBeNull();
    expect(p.steps[1].state).toBe("pending");
  });

  it("is complete when every node has answered", () => {
    const p = runProgress(
      steps,
      steps.map((s) => ({ nodeId: s.id, status: "success" })),
      "success",
    );
    expect(p.percent).toBe(100);
    expect(p.current).toBeNull();
  });

  // An attached tool that charges gets a log row of its own, keyed by a node
  // that is not a milestone. It must not count towards the total.
  it("ignores a log for a node that is not a step", () => {
    const p = runProgress(
      steps,
      [
        { nodeId: "t", status: "success" },
        { nodeId: "tool", status: "success" },
      ],
      "running",
    );
    expect(p.completed).toBe(1);
    expect(p.total).toBe(4);
  });

  it("takes the newest attempt of a retried node", () => {
    const p = runProgress(
      steps,
      [
        { nodeId: "t", status: "failed" },
        { nodeId: "t", status: "success", durationMs: 30 },
      ],
      "running",
    );
    expect(p.steps[0].state).toBe("done");
    expect(p.failed).toBe(false);
  });

  // Found on a phone: a run finished, but two of its four nodes never
  // reported (a branch not taken), so the dock sat at "2/4 Finished".
  it("counts what a finished run never reached as behind it", () => {
    const p = runProgress(
      steps,
      [
        { nodeId: "t", status: "success" },
        { nodeId: "a", status: "success" },
      ],
      "success",
    );
    expect(p.percent).toBe(100);
    expect(p.completed).toBe(4);
    expect(p.steps.map((s) => s.state)).toEqual([
      "done",
      "done",
      "skipped",
      "skipped",
    ]);
  });

  // A stopped run is different: those nodes were going to run, and did not.
  it("leaves a stopped run's remaining steps pending", () => {
    const p = runProgress(
      steps,
      [{ nodeId: "t", status: "success" }],
      "stopped",
    );
    expect(p.steps[2].state).toBe("pending");
    expect(p.percent).toBe(25);
  });

  it("calls a workflow with no steps complete rather than 0%", () => {
    expect(runProgress([], [], "success").percent).toBe(100);
  });
});
