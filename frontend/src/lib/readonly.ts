// The web app is a viewer, not an editor.
//
// Authoring a workflow -- creating one, moving or wiring its nodes, editing a
// node's config, deploying it -- happens in the AgentMesh desktop app. This
// client can watch a workflow, trigger a run, talk to a deployed one, and
// manage the account behind it, but it can never change what a workflow *is*.
//
// A constant rather than an env var, deliberately: the web build has exactly
// one mode, and a flag would advertise a supported editing deployment that no
// longer exists. Flipping this single value restores the editor everywhere,
// because every gate in the app reads it through `can()` below.
export const READ_ONLY = true;

// Named for what the user is trying to do, not for the endpoint behind it, so
// a call site reads as intent: `can("workflow.deploy")`.
export type Capability =
  // Authoring — desktop only.
  | "workflow.create"
  | "workflow.delete"
  | "workflow.editGraph"
  | "workflow.deploy"
  | "workflow.buildFromChat"
  // Operating — still available here.
  | "workflow.run"
  | "workflow.stop"
  | "workflow.chat"
  | "account.billing"
  | "account.settings";

// Everything absent from this set stays available. Listing what is *withheld*
// (rather than what is permitted) means a capability added later is readable
// by default, and has to be denied on purpose.
const WITHHELD: ReadonlySet<Capability> = new Set<Capability>([
  "workflow.create",
  "workflow.delete",
  "workflow.editGraph",
  "workflow.deploy",
  // Build mode rewrites the graph from a chat message, so it is authoring
  // wearing a conversation's clothes. Ordinary chat with an already-deployed
  // workflow is not, and stays open.
  "workflow.buildFromChat",
]);

export function can(capability: Capability): boolean {
  if (!READ_ONLY) return true;
  return !WITHHELD.has(capability);
}

// Defence in depth for the API layer. Hiding a button is a UX decision; this
// is the guarantee that a missed button, a stale bundle, or a deep link cannot
// still put a write on the wire.
//
// The rules deliberately mirror backend/internal/api/readonly.go one for one.
// Anything permitted here must be permitted there, or a user gets a control
// that fails at the server with no explanation.
const WRITE_RULES: ReadonlyArray<{ method: string; pattern: RegExp }> = [
  { method: "POST", pattern: /^\/workflows$/ },
  { method: "PUT", pattern: /^\/workflows\/[^/]+$/ },
  { method: "DELETE", pattern: /^\/workflows\/[^/]+$/ },
  { method: "POST", pattern: /^\/workflows\/[^/]+\/deploy$/ },
  { method: "POST", pattern: /^\/workflows\/[^/]+\/build$/ },
];

// `path` is the API path as written at the call site (leading slash, no
// origin, no query). Query strings and trailing slashes are normalised off
// first so a caller cannot slip a write past by appending either.
export function isWriteBlocked(method: string, path: string): boolean {
  if (!READ_ONLY) return false;
  const m = method.toUpperCase();
  let p = path.split("?")[0].split("#")[0];
  if (p.length > 1 && p.endsWith("/")) p = p.slice(0, -1);
  return WRITE_RULES.some((r) => r.method === m && r.pattern.test(p));
}
