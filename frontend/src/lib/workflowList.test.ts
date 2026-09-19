import { describe, expect, it } from "vitest";
import type { Workflow } from "./types";
import { filterWorkflows } from "./workflowList";

function wf(overrides: Partial<Workflow>): Workflow {
  return { id: "wf", name: "Workflow", nodes: [], edges: [], ...overrides };
}

const LIST = [
  wf({
    id: "a",
    name: "Customer Support Triage",
    status: "deployed",
    tags: ["support"],
  }),
  wf({ id: "b", name: "Invoice check", status: "paused" }),
  wf({ id: "c", name: "Lead scoring", status: "draft" }),
];

const ids = (list: Workflow[]) => list.map((w) => w.id);

describe("filterWorkflows", () => {
  it("shows a deployed workflow under Active", () => {
    expect(ids(filterWorkflows(LIST, { query: "", status: "active" }))).toEqual(
      ["a"],
    );
    expect(
      ids(filterWorkflows(LIST, { query: "", status: "deployed" })),
    ).toEqual(["a"]);
  });

  it("filters the other statuses by name", () => {
    expect(ids(filterWorkflows(LIST, { query: "", status: "paused" }))).toEqual(
      ["b"],
    );
    expect(ids(filterWorkflows(LIST, { query: "", status: "all" }))).toEqual([
      "a",
      "b",
      "c",
    ]);
  });

  it("searches names and tags, ignoring case and outer spaces", () => {
    expect(
      ids(filterWorkflows(LIST, { query: "  INVOICE ", status: "all" })),
    ).toEqual(["b"]);
    expect(
      ids(filterWorkflows(LIST, { query: "Support", status: "all" })),
    ).toEqual(["a"]);
  });
});
