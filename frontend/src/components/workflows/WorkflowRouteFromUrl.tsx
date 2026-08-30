"use client";
import { useSearchParams } from "next/navigation";
import { WorkflowRoute } from "./WorkflowRoute";

// Which workflow this page is showing, resolved from the URL.
//
// On the web the answer is simply the route segment: /workflows/<id>. The
// native shell cannot use that. Its bundle is a static export served from the
// device, and a static export can only contain pages that existed at build
// time -- it has no way to prerender a page per workflow, because the ids
// belong to users who have not signed up yet. So the mobile build emits ONE
// shell page and the real id travels as ?id=.
//
// Resolved here, in a client component, rather than from the server
// component's searchParams: a static export prerenders that server component
// once, with no request and therefore no query string, so the answer has to be
// read in the browser.
//
// `key` is what makes switching workflows reset the editor rather than carry
// state across, so it has to be the EFFECTIVE id -- keying on the route
// segment would be a constant in the native shell and never remount.
export function WorkflowRouteFromUrl({ routeId }: { routeId: string }) {
  const fromQuery = useSearchParams().get("id");
  const workflowId = fromQuery || routeId;
  return <WorkflowRoute key={workflowId} workflowId={workflowId} />;
}
