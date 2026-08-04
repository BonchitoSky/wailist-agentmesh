"use client";
import { useEffect, useState } from "react";
import { CanvasPage } from "@/components/canvas/CanvasPage";
import { TendrilConsolePage } from "@/components/tendril/TendrilConsolePage";
import { workflows as workflowsApi } from "@/lib/api";
import { TENDRIL_WORKFLOW_NAME } from "@/lib/tendril";

// Most workflow ids open the normal canvas. Any row named exactly
// TENDRIL_WORKFLOW_NAME (every "Load Tendril workflow" click creates a fresh
// one, same as the other demo buttons) opens the Tendril console instead:
// renting real hardware is a lookup-and-press-buttons task, not something
// that benefits from a node graph, so those rows never show the editor —
// there's nothing on their canvas to show in the first place.
export function WorkflowRoute({ workflowId }: { workflowId: string }) {
  const [isTendrilConsole, setIsTendrilConsole] = useState<boolean | null>(
    () => (workflowId === "new" ? false : null),
  );

  useEffect(() => {
    if (workflowId === "new") return;
    let stale = false;
    workflowsApi
      .get(workflowId)
      .then((wf) => {
        if (!stale) setIsTendrilConsole(wf.name === TENDRIL_WORKFLOW_NAME);
      })
      .catch(() => {
        if (!stale) setIsTendrilConsole(false);
      });
    return () => {
      stale = true;
    };
  }, [workflowId]);

  if (isTendrilConsole === null) {
    return (
      <div
        style={{
          height: "100vh",
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          background: "var(--bg)",
          color: "var(--fg-dim)",
          fontFamily: "var(--font-mono)",
          fontSize: 12,
        }}
      >
        loading…
      </div>
    );
  }
  if (isTendrilConsole) return <TendrilConsolePage />;
  return <CanvasPage key={workflowId} workflowId={workflowId} />;
}
