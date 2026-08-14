"use client";
import { useEffect, useRef, useState } from "react";
import { Pill } from "@/components/ui";
import { TerminalTab } from "./TerminalTab";
import {
  useRunTranscript,
  isX402Payment,
  type LogEvent,
  type X402Payment,
} from "./useRunTranscript";
import { ChatPane } from "./chat/ChatPane";
import { useChatSession } from "./chat/useChatSession";
import { resolveReply } from "./chat/resolveReply";
import { recoverPendingTurn } from "./chat/recoverPendingTurn";
import { runs as runsApi, type RunLogRecord } from "@/lib/api";

interface ConsolePanelProps {
  open: boolean;
  onToggle: () => void;
  runId: string | null;
  running: boolean;
  onRunComplete: () => void;
  // Scopes the cached last-run transcript, so reopening a workflow shows that
  // workflow's own last result rather than whatever ran most recently.
  workflowId?: string;
  /** True when the workflow has a chat trigger, so it can be talked to. */
  chatEnabled?: boolean;
  /** Starts a run from a chat message; resolves false if none started. */
  onSendMessage?: (text: string) => Promise<boolean>;
}

const HEIGHT_KEY = "agentmesh_console_height";
// Whether a chat-capable workflow also shows the log rows. Persisted per user
// rather than per workflow: it tracks who you are (developer or not), not
// which graph you happen to have open. A developer flips it once; someone who
// only wants the answer never touches it.
const LOGS_VISIBLE_KEY = "agentmesh_console_logs_visible";
// Chat keeps a comfortable reading column when the logs sit beside it; when
// they are hidden it takes the full width instead. Draggable via the divider,
// so this is just the fallback before a saved width loads.
const CHAT_WIDTH = 380;
const CHAT_WIDTH_KEY = "agentmesh_console_chat_width";
const MIN_CHAT_WIDTH = 260;
// Leave room for the logs column: a chat pane dragged to fill the console
// would hide the steps it caused.
const MIN_LOGS_WIDTH = 220;
const DEFAULT_HEIGHT = 240;
const MIN_HEIGHT = 120;
// Leave room for the topbar and some canvas: a console dragged to fill the
// window would hide the graph it is reporting on.
const maxHeight = () =>
  typeof window === "undefined"
    ? 640
    : Math.max(MIN_HEIGHT, window.innerHeight - 220);

export function ConsolePanel({
  open,
  onToggle,
  runId,
  running,
  onRunComplete,
  workflowId,
  chatEnabled = false,
  onSendMessage,
}: ConsolePanelProps) {
  // Everything about the run itself -- SSE, DB reconciliation, the cached
  // last-run transcript, settlement recording -- lives in useRunTranscript.
  // This component owns only how that transcript is presented.
  const { logs, elapsed, done, leaseId } = useRunTranscript({
    runId,
    running,
    onRunComplete,
    workflowId,
  });

  const session = useChatSession(workflowId);
  const [height, setHeight] = useState(DEFAULT_HEIGHT);
  const [resizing, setResizing] = useState(false);
  const [chatWidth, setChatWidth] = useState(CHAT_WIDTH);
  const [hResizing, setHResizing] = useState(false);
  const [tab, setTab] = useState<"logs" | "terminal">("logs");
  const [logsVisible, setLogsVisible] = useState(false);
  const bottomRef = useRef<HTMLDivElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);

  // A workflow with no chat trigger has nothing but logs to show, so the
  // toggle only applies to chat-capable ones.
  const showLogs = !chatEnabled || logsVisible;

  // Restore the logs-visible preference. Same next-frame pattern as the
  // height below, and for the same hydration reason.
  useEffect(() => {
    const id = requestAnimationFrame(() => {
      try {
        setLogsVisible(window.localStorage.getItem(LOGS_VISIBLE_KEY) === "1");
      } catch {
        /* unavailable storage: default to chat-only */
      }
    });
    return () => cancelAnimationFrame(id);
  }, []);

  const toggleLogs = () => {
    setLogsVisible((v) => {
      const next = !v;
      try {
        window.localStorage.setItem(LOGS_VISIBLE_KEY, next ? "1" : "0");
      } catch {
        /* best-effort */
      }
      return next;
    });
  };

  // Bind the turn the user just sent to the run the backend actually started.
  // The console is remounted with key={runId} the moment a run begins, so the
  // pending message arrives here by way of localStorage, not props.
  const { attachRun, completeTurnForRun, completeTurnById, hydrated } = session;
  useEffect(() => {
    if (!runId || !hydrated) return;
    attachRun(runId);
  }, [runId, hydrated, attachRun]);

  // Fill that turn in once the run reaches a terminal state. Guarded on runId
  // so a transcript restored from cache (which arrives already `done`) can't
  // resolve a turn that belongs to some earlier run.
  useEffect(() => {
    if (!done || !runId || !hydrated) return;
    const summary = resolveReply(logs);
    completeTurnForRun(runId, {
      text: summary.text,
      isError: summary.isError,
      toolCount: summary.toolCount,
      spendUSD: summary.spendUSD,
      elapsedS: elapsed ?? undefined,
      runId,
    });
  }, [done, runId, hydrated, logs, elapsed, completeTurnForRun]);

  // Recover a turn stranded by a page reload.
  //
  // CanvasPage holds runId in useState, so a refresh puts it back to null while
  // useChatSession has already persisted the pending turn. The two effects
  // above are both gated on a live runId, so nothing would otherwise settle it.
  //
  // The ref is armed the moment hydration completes, BEFORE looking for a
  // stranded turn -- not after. Arming it only when one was found left it live
  // on a clean mount, and the next thing to touch the transcript is startTurn,
  // whose brand-new turn has no runId yet: this effect would read that as
  // stranded and settle it as "interrupted" before its run even began, then
  // discard the real answer when it arrived. Against a real backend the
  // network round trip guarantees that ordering, so it destroyed the first
  // message after every page load.
  //
  // `messages` is deliberately NOT a dependency. Re-firing on every transcript
  // change is what made the above possible, and its cleanup could also cancel
  // an in-flight recovery fetch that the armed ref then refused to retry --
  // stranding the turn permanently. One snapshot, one attempt.
  const recoveredRef = useRef(false);
  // Mirrored in an effect rather than assigned during render (refs must not be
  // written while rendering). Declared before the recovery effect so it is
  // already current when that one runs on the same commit.
  const messagesRef = useRef(session.messages);
  useEffect(() => {
    messagesRef.current = session.messages;
  }, [session.messages]);
  useEffect(() => {
    if (!hydrated || runId || recoveredRef.current) return;
    recoveredRef.current = true;

    const stranded = [...messagesRef.current].reverse().find((m) => m.pending);
    if (!stranded) return;

    let cancelled = false;
    void (async () => {
      let record: { status: string } | null = null;
      let rows: RunLogRecord[] = [];
      if (stranded.runId) {
        try {
          const res = await runsApi.get(stranded.runId);
          record = res.run;
          rows = res.logs;
        } catch {
          // Leave record null: recoverPendingTurn reports it as interrupted
          // rather than inventing an outcome.
        }
      }
      if (cancelled) return;
      const recovery = recoverPendingTurn(record, rows);
      completeTurnById(
        stranded.id,
        recovery.kind === "resolved"
          ? {
              text: recovery.text,
              isError: recovery.isError,
              toolCount: recovery.toolCount,
              spendUSD: recovery.spendUSD,
              runId: stranded.runId,
            }
          : {
              text: recovery.text,
              interrupted: true,
              runId: stranded.runId,
            },
      );
    })();

    return () => {
      cancelled = true;
    };
  }, [hydrated, runId, completeTurnById]);

  const handleSend = (text: string) => {
    const turnId = session.startTurn(text);
    // A send that never becomes a run (offline, not deployed, backend refusal)
    // has nothing left to settle its turn -- startRun only surfaces a toast --
    // so the bubble would spin forever. Settle it here instead.
    void (async () => {
      const ok = (await onSendMessage?.(text)) ?? false;
      if (!ok) {
        completeTurnById(turnId, {
          text: "Could not start this run — see the notification for why, then try again.",
          isError: true,
        });
      }
    })();
  };

  // The composer must lock the instant a turn is created, not when `running`
  // flips -- CanvasPage sets that only after workflows.run() resolves, leaving
  // the whole round trip open to a double Send that starts two real, billed
  // runs. A pending turn is the earliest honest signal that we are busy.
  const busy = running || session.messages.some((m) => m.pending);

  // Restore the last chosen console height. Read in an effect rather than in
  // useState's initializer so the server render and first client render agree.
  // Applied on the next frame rather than synchronously: the server renders
  // the default height, so setting the stored one during the effect body
  // would both trip the cascading-render rule and mismatch hydration.
  useEffect(() => {
    const id = requestAnimationFrame(() => {
      try {
        const saved = Number(window.localStorage.getItem(HEIGHT_KEY));
        if (saved >= MIN_HEIGHT) setHeight(Math.min(saved, maxHeight()));
      } catch {
        /* unavailable storage: keep the default */
      }
    });
    return () => cancelAnimationFrame(id);
  }, []);

  // Restore the last chosen chat/logs split, same next-frame pattern as
  // height above and for the same hydration reason.
  useEffect(() => {
    const id = requestAnimationFrame(() => {
      try {
        const saved = Number(window.localStorage.getItem(CHAT_WIDTH_KEY));
        if (saved >= MIN_CHAT_WIDTH) setChatWidth(saved);
      } catch {
        /* unavailable storage: keep the default */
      }
    });
    return () => cancelAnimationFrame(id);
  }, []);

  // Drag the divider between chat and logs to resize the split. Bounded on
  // both sides so neither pane can be dragged out from under its content.
  const startHResize = (e: React.MouseEvent) => {
    e.preventDefault();
    setHResizing(true);
    const startX = e.clientX;
    const startWidth = chatWidth;
    const onMove = (ev: MouseEvent) => {
      const bodyWidth = bodyRef.current?.clientWidth ?? Infinity;
      const max = Math.max(MIN_CHAT_WIDTH, bodyWidth - MIN_LOGS_WIDTH);
      const next = Math.min(
        max,
        Math.max(MIN_CHAT_WIDTH, startWidth + (ev.clientX - startX)),
      );
      setChatWidth(next);
    };
    const onUp = () => {
      setHResizing(false);
      window.removeEventListener("mousemove", onMove);
      window.removeEventListener("mouseup", onUp);
      setChatWidth((w) => {
        try {
          window.localStorage.setItem(CHAT_WIDTH_KEY, String(w));
        } catch {
          /* best-effort */
        }
        return w;
      });
    };
    window.addEventListener("mousemove", onMove);
    window.addEventListener("mouseup", onUp);
  };

  // Drag the top edge to resize. Listeners live on the window, not the handle,
  // so a fast drag that outruns the pointer doesn't drop the gesture.
  const startResize = (e: React.MouseEvent) => {
    e.preventDefault();
    setResizing(true);
    const startY = e.clientY;
    const startHeight = height;
    const onMove = (ev: MouseEvent) => {
      // Dragging up grows the console, hence the inverted delta.
      const next = Math.min(
        maxHeight(),
        Math.max(MIN_HEIGHT, startHeight + (startY - ev.clientY)),
      );
      setHeight(next);
    };
    const onUp = () => {
      setResizing(false);
      window.removeEventListener("mousemove", onMove);
      window.removeEventListener("mouseup", onUp);
      setHeight((h) => {
        try {
          window.localStorage.setItem(HEIGHT_KEY, String(h));
        } catch {
          /* best-effort */
        }
        return h;
      });
    };
    window.addEventListener("mousemove", onMove);
    window.addEventListener("mouseup", onUp);
  };

  // Auto-scroll to bottom on new logs
  useEffect(() => {
    if (open) bottomRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [logs, open]);

  const statusColor = (s: LogEvent["status"]) => {
    if (s === "success") return "var(--accent)";
    if (s === "failed") return "#F87171";
    if (s === "stopped") return "#FB923C";
    return "var(--fg-dim)";
  };

  const statusLabel = (s: LogEvent["status"]) => {
    if (s === "success") return "OK";
    if (s === "failed") return "ERR";
    if (s === "stopped") return "STP";
    return "RUN";
  };

  // A run that stops offering a lease (a fresh run with no Tendril step)
  // must not leave the console stuck on a dead Terminal tab — derived at
  // render time rather than synced back via an effect.
  const effectiveTab = leaseId ? tab : "logs";

  const elapsedStr = (elapsed ?? 0).toFixed(1);
  const headerPill = runId
    ? done
      ? `run · ${runId.slice(0, 8)} · ${elapsedStr}s`
      : running
        ? `running · ${elapsedStr}s`
        : `run · ${runId.slice(0, 8)}`
    : "console";

  const pillTone = done ? "ok" : running ? "warm" : "default";

  return (
    <div
      style={{
        position: "absolute",
        left: 0,
        right: 0,
        bottom: 0,
        background: "var(--bg-elev-1)",
        borderTop: "1px solid var(--border)",
        height: open ? height : 32,
        // No animation mid-drag: the tween lags the pointer and feels broken.
        transition: resizing ? "none" : "height .2s var(--ease)",
        display: "flex",
        flexDirection: "column",
        zIndex: 5,
      }}
    >
      {/* Resize grip. Sits above the header and slightly outside the drawer
          so it stays grabbable without stealing clicks from the header's
          expand/collapse toggle. */}
      {open && (
        <div
          onMouseDown={startResize}
          title="Drag to resize console"
          style={{
            position: "absolute",
            top: -3,
            left: 0,
            right: 0,
            height: 7,
            cursor: "ns-resize",
            zIndex: 6,
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
          }}
        >
          <div
            style={{
              width: 40,
              height: 3,
              borderRadius: 2,
              background: resizing ? "var(--accent)" : "var(--border-strong)",
            }}
          />
        </div>
      )}

      {/* Header bar */}
      <div
        onClick={onToggle}
        style={{
          height: 32,
          padding: "0 14px",
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          cursor: "pointer",
          borderBottom: open ? "1px solid var(--border)" : "none",
          flexShrink: 0,
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: 12 }}>
          <span
            style={{
              fontFamily: "var(--font-mono)",
              fontSize: 10,
              color: "var(--fg-muted)",
              textTransform: "uppercase",
              letterSpacing: "0.08em",
            }}
          >
            console
          </span>
          <Pill mono tone={pillTone} dot={running}>
            {headerPill}
          </Pill>
          {logs.length > 0 && (
            <span
              style={{
                fontFamily: "var(--font-mono)",
                fontSize: 10,
                color: "var(--fg-dim)",
              }}
            >
              {logs.length} step{logs.length !== 1 ? "s" : ""}
            </span>
          )}
          {/* The one control that decides which audience this panel serves.
              Off by default: the answer is the thing most people came for,
              and the log rows are opt-in rather than mandatory. */}
          {chatEnabled && open && (
            <button
              onClick={(e) => {
                e.stopPropagation();
                toggleLogs();
              }}
              aria-pressed={logsVisible}
              style={{
                fontFamily: "var(--font-mono)",
                fontSize: 9.5,
                textTransform: "uppercase",
                letterSpacing: "0.06em",
                padding: "2px 8px",
                borderRadius: 999,
                border: `1px solid ${logsVisible ? "var(--accent)" : "var(--border)"}`,
                color: logsVisible ? "var(--accent)" : "var(--fg-dim)",
                background: "transparent",
                cursor: "pointer",
              }}
            >
              Logs
            </button>
          )}
          {leaseId && open && showLogs && (
            <div
              onClick={(e) => e.stopPropagation()}
              style={{ display: "flex", gap: 4 }}
            >
              {(["logs", "terminal"] as const).map((tb) => (
                <button
                  key={tb}
                  onClick={() => setTab(tb)}
                  style={{
                    fontFamily: "var(--font-mono)",
                    fontSize: 9.5,
                    textTransform: "uppercase",
                    letterSpacing: "0.06em",
                    padding: "2px 8px",
                    borderRadius: 999,
                    border: `1px solid ${tab === tb ? "var(--accent)" : "var(--border)"}`,
                    color: tab === tb ? "var(--accent)" : "var(--fg-dim)",
                    background: "transparent",
                    cursor: "pointer",
                  }}
                >
                  {tb === "logs" ? "Logs" : "Terminal"}
                </button>
              ))}
            </div>
          )}
        </div>
        <span
          style={{
            display: "inline-flex",
            alignItems: "center",
            gap: 5,
            fontFamily: "var(--font-mono)",
            fontSize: 13,
            color: "var(--fg-muted)",
          }}
        >
          <span style={{ fontSize: 15, lineHeight: 1 }}>
            {open ? "▾" : "▴"}
          </span>
          {open ? "collapse" : "expand"}
        </span>
      </div>

      {/* Body: chat on the left (the human view), logs on the right (the
          technical one). Siblings in one surface rather than separate
          features, so a message and the steps it caused sit side by side. */}
      {open && (
        <div
          ref={bodyRef}
          style={{ flex: 1, minHeight: 0, display: "flex" }}
        >
          {chatEnabled && (
            <div
              style={{
                width: showLogs ? chatWidth : "100%",
                flexShrink: 0,
                minWidth: 0,
                // An explicit column rather than relying on the pane's
                // height:100% resolving against a stretched flex item, which
                // collapses to zero the moment this wrapper's own height is
                // indefinite.
                display: "flex",
                flexDirection: "column",
                minHeight: 0,
              }}
            >
              <ChatPane
                session={session}
                onSend={handleSend}
                busy={busy}
                onShowLogs={() => {
                  if (!logsVisible) toggleLogs();
                }}
              />
            </div>
          )}

          {/* Divider between chat and logs. Only meaningful with both panes
              on screen -- chat-only and logs-only workflows have nothing to
              split. */}
          {chatEnabled && showLogs && (
            <div
              onMouseDown={startHResize}
              title="Drag to resize chat"
              style={{
                width: 9,
                flexShrink: 0,
                cursor: "col-resize",
                display: "flex",
                alignItems: "center",
                justifyContent: "center",
                zIndex: 1,
              }}
            >
              <div
                style={{
                  width: 3,
                  height: 40,
                  borderRadius: 2,
                  background: hResizing ? "var(--accent)" : "var(--border-strong)",
                }}
              />
            </div>
          )}

          {showLogs && (
            <div
              style={{
                flex: 1,
                minWidth: 0,
                minHeight: 0,
                display: "flex",
                flexDirection: "column",
              }}
            >
              {/* Terminal — mounted only while selected; unmounting on tab
                  switch away is intentional (it owns a live WebSocket + SSH
                  session, unlike the log list which is cheap to keep mounted
                  underneath). */}
              {effectiveTab === "terminal" && leaseId && (
                <div style={{ flex: 1, minHeight: 0 }}>
                  <TerminalTab
                    leaseId={leaseId}
                    onClose={() => setTab("logs")}
                  />
                </div>
              )}

              {/* Log lines. Stays mounted (hidden via CSS) when the Terminal
                  tab is active, so switching back does not lose the
                  transcript. */}
              <div
                style={{
                  display: effectiveTab === "terminal" ? "none" : "block",
                  flex: 1,
                  overflow: "auto",
                  padding: "6px 14px 10px",
                  fontFamily: "var(--font-mono)",
                  fontSize: 11,
                  lineHeight: 1.7,
                }}
              >
                {logs.length === 0 && !running && !runId && (
                  <div style={{ color: "var(--fg-dim)", paddingTop: 8 }}>
                    run a workflow to see execution logs here.
                  </div>
                )}
                {logs.length === 0 && running && (
                  <div style={{ color: "var(--fg-dim)", paddingTop: 8 }}>
                    waiting for first node…
                  </div>
                )}
                {logs.map((l, i) => (
                  <div
                    key={i}
                    style={{
                      display: "grid",
                      gridTemplateColumns: "52px 34px 110px 1fr",
                      gap: 10,
                      alignItems: "baseline",
                      borderBottom: "1px solid var(--border-soft)",
                      padding: "3px 0",
                    }}
                  >
                    <span style={{ color: "var(--fg-dim)", fontSize: 9.5 }}>
                      {new Date(l.ts).toLocaleTimeString("en", {
                        hour12: false,
                        hour: "2-digit",
                        minute: "2-digit",
                        second: "2-digit",
                      })}
                    </span>
                    <span
                      style={{
                        color: statusColor(l.status),
                        fontWeight: 600,
                        fontSize: 9.5,
                      }}
                    >
                      {statusLabel(l.status)}
                    </span>
                    <span
                      style={{
                        color: "var(--fg-muted)",
                        overflow: "hidden",
                        textOverflow: "ellipsis",
                        whiteSpace: "nowrap",
                      }}
                    >
                      <span style={{ color: nodeTypeColor(l.nodeType) }}>
                        {l.nodeType}
                      </span>
                      {l.durationMs > 0 && (
                        <span style={{ color: "var(--fg-dim)" }}>
                          {" "}
                          · {l.durationMs}ms
                        </span>
                      )}
                    </span>
                    <OutputCell output={l.output} />
                  </div>
                ))}
                {done &&
                  (() => {
                    const succeeded = logs.filter(
                      (l) => l.status === "success",
                    ).length;
                    const hasFailure = logs.some((l) => l.status === "failed");
                    return (
                      <div
                        style={{
                          color: hasFailure ? "#F87171" : "var(--accent)",
                          paddingTop: 6,
                          fontSize: 10,
                        }}
                      >
                        {hasFailure ? "✕ run failed" : "✓ run complete"} ·{" "}
                        {(elapsed ?? 0).toFixed(1)}s · {succeeded}/{logs.length}{" "}
                        nodes succeeded
                      </div>
                    );
                  })()}
                <div ref={bottomRef} />
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function formatSize(chars: number): string {
  return chars < 1024 ? `${chars} B` : `${(chars / 1024).toFixed(1)} KB`;
}

// OutputCell renders a step's result: the settlement receipt (amount + both
// on-chain tx ids) when the step paid for something, AND the payload the
// endpoint actually returned. Previously these were either/or — a paid
// tool402 step matched the receipt branch and its response body was never
// shown at all, so the whole point of the call (the data) was invisible.
// Large payloads collapse behind a toggle rather than flooding the console.
function OutputCell({ output }: { output: unknown }) {
  const [expanded, setExpanded] = useState(false);

  const payment = isX402Payment(output) ? output : null;
  // A paid step's own response lives under `response`; an unpaid step's
  // output IS the payload.
  const payload = payment
    ? (output as { response?: unknown }).response
    : output;

  let text = "";
  if (payload !== null && payload !== undefined) {
    text =
      typeof payload === "string" ? payload : JSON.stringify(payload, null, 2);
  }
  const collapsible = text.length > 200;

  return (
    <div style={{ minWidth: 0 }}>
      {payment && (
        <span
          style={{
            display: "flex",
            alignItems: "center",
            gap: 5,
            flexWrap: "wrap",
          }}
        >
          {/* Which endpoint this payment was for. A run-funded agent emits
              one row for the run's up-front funding settlement plus one per
              tool call, all against the same tx id — without a label they
              read as duplicate charges, and an agent with several attached
              tool402 nodes gave no way to tell its payment rows apart. */}
          {payment.nodeName && (
            <span style={{ color: "var(--fg-dim)" }}>{payment.nodeName} ·</span>
          )}
          <span style={{ color: "var(--fg)" }}>
            {payment.amount
              ? `${parseFloat(payment.amount).toFixed(6)} paid`
              : "paid"}
          </span>
          {payment.txId && payment.explorerURL && (
            <>
              <span style={{ color: "var(--fg-dim)" }}>· in</span>
              <a
                href={payment.explorerURL}
                target="_blank"
                rel="noopener noreferrer"
                style={txLinkStyle}
                onClick={(e) => e.stopPropagation()}
              >
                {payment.txId.slice(0, 8)}…
              </a>
            </>
          )}
          {payment.outboundTxId && payment.outboundExplorerURL && (
            <>
              <span style={{ color: "var(--fg-dim)" }}>· out</span>
              <a
                href={payment.outboundExplorerURL}
                target="_blank"
                rel="noopener noreferrer"
                style={txLinkStyle}
                onClick={(e) => e.stopPropagation()}
              >
                {payment.outboundTxId.slice(0, 8)}…
              </a>
            </>
          )}
        </span>
      )}
      {text === "" ? (
        !payment && <span style={{ color: "var(--fg-dim)" }}>—</span>
      ) : collapsible ? (
        <>
          <button
            onClick={() => setExpanded((v) => !v)}
            style={{
              background: "none",
              border: "none",
              padding: 0,
              cursor: "pointer",
              color: "var(--accent)",
              fontFamily: "var(--font-mono)",
              fontSize: 10,
            }}
          >
            {expanded ? "▾" : "▸"} response · {formatSize(text.length)}
          </button>
          {expanded && (
            <pre
              style={{
                margin: "4px 0 0",
                padding: 8,
                maxHeight: 220,
                overflow: "auto",
                background: "var(--bg)",
                border: "1px solid var(--border)",
                borderRadius: 4,
                fontFamily: "var(--font-mono)",
                fontSize: 10,
                lineHeight: 1.5,
                color: "var(--fg)",
                whiteSpace: "pre-wrap",
                wordBreak: "break-word",
              }}
            >
              {text}
            </pre>
          )}
        </>
      ) : (
        <span
          style={{
            color: "var(--fg)",
            whiteSpace: "pre-wrap",
            wordBreak: "break-word",
          }}
        >
          {text}
        </span>
      )}
    </div>
  );
}

const txLinkStyle: React.CSSProperties = {
  color: "#E879F9",
  textDecoration: "underline",
  fontFamily: "var(--font-mono)",
  fontSize: 9.5,
  whiteSpace: "nowrap",
};

function nodeTypeColor(t: string): string {
  if (t === "agent") return "var(--accent)";
  if (t === "tool402") return "#E879F9";
  if (t === "action") return "#FB923C";
  return "var(--fg-dim)";
}

export type { LogEvent, X402Payment };
