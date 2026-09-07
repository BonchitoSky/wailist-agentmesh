"use client";
import { CanvasPage } from "@/components/canvas/CanvasPage";

// Every workflow id opens the canvas. Partner consoles (Tendril, Prism) used
// to be dispatched from here too -- one hidden row per user per partner,
// matched by id against tendril/prism.consoleWorkflowIdIfExists() on every
// single workflow-page visit, canvas or not, just to answer "is this one of
// the two console ids". They now live at their own routes (/bazaar/tendril,
// /bazaar/prism): neither console page ever read a workflow id in the first
// place (they drive off /tendril/* and /prism/* directly), so the id-match
// dispatch was pure per-visit overhead -- two network calls and a loading
// flicker -- for a question a route segment now answers for free.
export function WorkflowRoute({ workflowId }: { workflowId: string }) {
  return <CanvasPage key={workflowId} workflowId={workflowId} />;
}
