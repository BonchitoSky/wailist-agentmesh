"use client";
import { useEffect, useRef, useState } from "react";
import { runProgress, type ProgressLog, type RunStep } from "@/lib/runProgress";
import { tapFeedback } from "@/native/haptics";

// What a run is doing, while it does it.
//
// Tapping Run used to change a button and add a row: everything else happened
// somewhere the reader could not see. This docks a line at the bottom of the
// workflow screen with one milestone per node, so a run that takes twenty
// seconds looks like twenty seconds of work rather than a frozen screen.
//
// Docked rather than a sheet on purpose: a sheet would cover the screen the
// run belongs to, and the point is to keep watching it.

const EXPANDED_KEY = "agentmesh.runprogress.expanded";
/** How long a finished run stays on screen before it sees itself out. */
const SUCCESS_LINGER_MS = 4000;

const DOCK_CSS = `
.run-dock {
  position: fixed;
  left: 0;
  right: 0;
  bottom: 0;
  z-index: 50;
  background: var(--bg-elev-2);
  border-top: 1px solid var(--border);
  box-shadow: 0 -8px 24px rgba(0, 0, 0, 0.45);
  padding: 10px 16px calc(10px + var(--safe-bottom, 0px));
}
.run-dock__bar {
  position: relative;
  height: 4px;
  border-radius: 999px;
  background: var(--bg-elev-3);
  overflow: hidden;
  margin-bottom: 8px;
}
.run-dock__fill {
  position: absolute;
  inset: 0 auto 0 0;
  border-radius: 999px;
  background: var(--accent);
  transition: width 0.35s var(--ease);
}
.run-dock[data-state="failed"] .run-dock__fill { background: var(--danger); }
.run-dock[data-state="success"] .run-dock__fill { background: var(--ok, #3ecf8e); }
.run-dock__line {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  background: none;
  border: none;
  padding: 0;
  color: var(--fg);
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.run-dock__count {
  font-family: var(--font-mono);
  font-size: 11.5px;
  color: var(--fg-muted);
  flex: none;
}
.run-dock__now {
  font-size: 12.5px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
  min-width: 0;
}
.run-dock__chevron { flex: none; color: var(--fg-dim); font-size: 11px; }
.run-dock__steps { list-style: none; margin: 10px 0 0; padding: 0; }
.run-dock__step {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 5px 0;
  font-size: 12.5px;
  color: var(--fg-muted);
}
.run-dock__step[data-state="done"] { color: var(--fg); }
.run-dock__step[data-state="running"] { color: var(--accent); }
.run-dock__step[data-state="failed"] { color: var(--danger); }
.run-dock__step[data-state="skipped"] { color: var(--fg-dim); }
.run-dock__dot {
  width: 8px;
  height: 8px;
  border-radius: 999px;
  background: currentColor;
  opacity: 0.35;
  flex: none;
}
.run-dock__step[data-state="done"] .run-dock__dot,
.run-dock__step[data-state="failed"] .run-dock__dot { opacity: 1; }
/* The one thing in motion is the node actually working. */
.run-dock__step[data-state="running"] .run-dock__dot {
  opacity: 1;
  animation: run-dock-pulse 1.2s ease-in-out infinite;
}
.run-dock__took {
  margin-left: auto;
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--fg-dim);
}
.run-dock__actions { display: flex; gap: 8px; margin-top: 10px; }
@keyframes run-dock-pulse {
  0%, 100% { transform: scale(1); opacity: 1; }
  50% { transform: scale(1.5); opacity: 0.45; }
}
@media (prefers-reduced-motion: reduce) {
  .run-dock__fill { transition: none; }
  .run-dock__step[data-state="running"] .run-dock__dot { animation: none; }
}
`;

function tookLabel(ms: number | undefined): string {
  if (ms === undefined) return "";
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`;
}

function stepMark(state: RunStep["state"]): string {
  if (state === "done") return "✓";
  if (state === "failed") return "✕";
  // A node a finished run never reached. Named rather than ticked: it did
  // not run, and saying it did would be untrue.
  if (state === "skipped") return "not run";
  return "";
}

export function RunProgressDock({
  steps,
  logs,
  runStatus,
  onDetails,
  onRunAgain,
  onDismiss,
}: {
  steps: { id: string; name: string }[];
  logs: ProgressLog[];
  /** The run's own status: running, success, failed or stopped. */
  runStatus: string;
  /** Opens the run's sheet, which already shows logs and problems. */
  onDetails: () => void;
  onRunAgain: () => void;
  onDismiss: () => void;
}) {
  const progress = runProgress(steps, logs, runStatus);
  // Remembered per reader, and never load-bearing: a device that refuses
  // storage just gets the collapsed default. Read at mount rather than in an
  // effect -- the dock only appears once a run has started, so there is no
  // server-rendered markup for it to disagree with.
  const [expanded, setExpanded] = useState(() => {
    try {
      return window.localStorage.getItem(EXPANDED_KEY) === "1";
    } catch {
      // Private mode, or storage turned off. The default stands.
      return false;
    }
  });
  const celebrated = useRef(false);
  const toggle = () => {
    setExpanded((was) => {
      const next = !was;
      try {
        window.localStorage.setItem(EXPANDED_KEY, next ? "1" : "0");
      } catch {
        // See above.
      }
      return next;
    });
  };

  const done = runStatus === "success";
  const failed = runStatus === "failed" || progress.failed;

  // A run that worked says so and leaves. One that failed stays: it is the
  // only thing on screen that can explain what went wrong.
  useEffect(() => {
    if (!done || celebrated.current) return;
    celebrated.current = true;
    void tapFeedback();
    const timer = window.setTimeout(onDismiss, SUCCESS_LINGER_MS);
    return () => window.clearTimeout(timer);
  }, [done, onDismiss]);

  const state = failed ? "failed" : done ? "success" : "running";
  const failedStep = progress.steps.find((s) => s.state === "failed");
  const headline = failed
    ? `Failed at ${failedStep?.name ?? "a step"}`
    : done
      ? "Finished"
      : (progress.current?.name ?? "Starting…");

  return (
    <div
      className="run-dock"
      data-state={state}
      role="status"
      aria-live="polite"
      aria-label={`Run progress: ${progress.completed} of ${progress.total} steps. ${headline}`}
    >
      <style>{DOCK_CSS}</style>
      <div className="run-dock__bar">
        <div
          className="run-dock__fill"
          style={{ width: `${progress.percent}%` }}
        />
      </div>
      <button
        type="button"
        className="run-dock__line"
        onClick={toggle}
        aria-expanded={expanded}
      >
        <span className="run-dock__count">
          {progress.completed}/{progress.total}
        </span>
        <span className="run-dock__now">{headline}</span>
        <span className="run-dock__chevron" aria-hidden="true">
          {expanded ? "▾" : "▴"}
        </span>
      </button>

      {expanded && (
        <ol className="run-dock__steps">
          {progress.steps.map((step) => (
            <li
              key={step.id}
              className="run-dock__step"
              data-state={step.state}
            >
              <span className="run-dock__dot" aria-hidden="true" />
              <span className="run-dock__now">{step.name}</span>
              <span className="run-dock__took">
                {stepMark(step.state)} {tookLabel(step.durationMs)}
              </span>
            </li>
          ))}
        </ol>
      )}

      {(failed || done) && (
        <div className="run-dock__actions">
          <button type="button" onClick={onDetails} style={dockBtn}>
            Details
          </button>
          {failed && (
            <button type="button" onClick={onRunAgain} style={dockBtn}>
              Run again
            </button>
          )}
          <button
            type="button"
            onClick={onDismiss}
            style={{ ...dockBtn, marginLeft: "auto" }}
          >
            Dismiss
          </button>
        </div>
      )}
    </div>
  );
}

const dockBtn: React.CSSProperties = {
  padding: "7px 12px",
  minHeight: 36,
  fontSize: 12.5,
  borderRadius: 6,
  border: "1px solid var(--border-strong)",
  background: "transparent",
  color: "var(--fg)",
  cursor: "pointer",
};
