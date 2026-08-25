import { COMPACT_QUERY } from "@/hooks/useIsCompact";

// The web app is an editor on a desktop and a viewer on a small screen.
//
// This is the GitHub arrangement: browse anywhere, author where there is room
// to. It is not an arbitrary line. The studio is a three-column IDE whose own
// constants need 780px before it can lay out at all, so below the compact
// breakpoint there is no editor to offer -- the palette does not fit, and
// dragging a node into place with a thumb is not the same gesture. Rather than
// serve a cramped editor, a narrow client gets a viewer that fits.
//
// The threshold is COMPACT_QUERY, the same one the studio uses to stack its
// columns, and deliberately so: if the two disagreed there would be a band of
// widths where the palette renders inside a stacked layout it was never
// designed for.

// Named for what the user is trying to do, not for the endpoint behind it, so
// a call site reads as intent: `can("workflow.deploy", readOnly)`.
export type Capability =
  // Authoring — needs the room a desktop has.
  | "workflow.create"
  | "workflow.delete"
  | "workflow.editGraph"
  | "workflow.deploy"
  | "workflow.buildFromChat"
  // Operating — available on any screen.
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

// Pure on purpose: `readOnly` is passed in rather than measured here, so the
// policy can be tested without a DOM and so React components drive it from
// useReadOnly() -- a function that measured the viewport itself would not
// re-render anything when the window crossed the breakpoint.
export function can(capability: Capability, readOnly: boolean): boolean {
  if (!readOnly) return true;
  return !WITHHELD.has(capability);
}

// Defence in depth for the API layer. Hiding a control is a UX decision; this
// is the guarantee that a missed control, a stale bundle, or a deep link
// cannot still put a write on the wire from a viewer.
//
// The rules deliberately mirror backend/internal/api/readonly.go one for one.
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
export function isWriteBlocked(
  method: string,
  path: string,
  readOnly: boolean,
): boolean {
  if (!readOnly) return false;
  const m = method.toUpperCase();
  let p = path.split("?")[0].split("#")[0];
  if (p.length > 1 && p.endsWith("/")) p = p.slice(0, -1);
  return WRITE_RULES.some((r) => r.method === m && r.pattern.test(p));
}

// For callers outside React, which is really just the fetch guard in lib/api.
// A one-shot reading is the right shape there: it answers "may this call go
// out, right now", and nothing needs to re-render when the answer changes.
// Components must use useReadOnly() instead.
export function isReadOnlyNow(): boolean {
  if (typeof window === "undefined" || !window.matchMedia) return false;
  return window.matchMedia(COMPACT_QUERY).matches;
}
