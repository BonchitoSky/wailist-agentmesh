import type { Workflow } from "./types";

// Which workflows a list shows. Shared by the desktop table and the phone
// list so the two can never disagree about what a filter means.

export type StatusFilter = "all" | "active" | "deployed" | "paused" | "draft";

// "Active" is the desktop tab's word for a running workflow, but the backend
// stores that state as "deployed" (models.WorkflowStatusDeployed) and never
// writes "active". Matching the literal left the tab permanently empty.
function matchesStatus(wf: Workflow, status: StatusFilter): boolean {
  if (status === "all") return true;
  if (status === "active" || status === "deployed") {
    return wf.status === "deployed" || wf.status === "active";
  }
  return wf.status === status;
}

export function filterWorkflows(
  list: Workflow[],
  { query, status }: { query: string; status: StatusFilter },
): Workflow[] {
  const q = query.trim().toLowerCase();
  return list.filter(
    (wf) =>
      matchesStatus(wf, status) &&
      (!q ||
        wf.name?.toLowerCase().includes(q) ||
        (wf.tags?.join(" ").toLowerCase().includes(q) ?? false)),
  );
}
