"use client";
import { useEffect, useMemo, useRef, useState } from "react";
import { runs as runsApi, auth as authApi, type RunLogRecord } from "@/lib/api";
import { recordSettlements } from "@/lib/settlements";

// This hook owns everything about *what happened in a run*: the live SSE
// stream, the DB reconciliation that covers the stream's gaps, the cached
// last-run transcript, and the settlement rows a finished run produces.
// It was extracted verbatim from LogDrawer -- every comment below is a
// postmortem of a real failure, so treat behavioural changes here as bugs
// unless they come with their own investigation.
//
// Presentation (height, tabs, open/closed) deliberately stays in the panel
// components: this hook is the data layer they share.

export interface LogEvent {
  stepIndex: number;
  nodeId: string;
  nodeType: string;
  status: "running" | "success" | "failed" | "stopped";
  output: unknown;
  durationMs: number;
  ts: string;
}

export interface X402Payment {
  txId: string;
  amount?: string;
  explorerURL?: string;
  // outboundTxId/outboundExplorerURL are the SECOND real settlement leg --
  // txId above is always the inbound leg (caller -> Wallet 2), this is
  // Wallet 2 -> the actual target, when the target returned one (not
  // every target does).
  outboundTxId?: string;
  outboundExplorerURL?: string;
  nodeName?: string;
  nodeId?: string;
}

export function isX402Payment(output: unknown): output is X402Payment {
  return typeof output === "object" && output !== null && "txId" in output;
}

const RUN_CACHE_PREFIX = "agentmesh_lastrun_";
// localStorage is ~5 MB per origin and a single x402 response can be tens of
// KB. Cap what we cache so one large transcript can't evict everything else
// (or throw on write) — the live view is never truncated, only the cache.
const MAX_CACHED_RUN_BYTES = 512 * 1024;

interface CachedRun {
  runId: string;
  logs: LogEvent[];
  elapsed: number | null;
}

function readCachedRun(workflowId: string | undefined): CachedRun | null {
  if (!workflowId || typeof window === "undefined") return null;
  try {
    const raw = window.localStorage.getItem(RUN_CACHE_PREFIX + workflowId);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as CachedRun;
    return Array.isArray(parsed.logs) ? parsed : null;
  } catch {
    return null;
  }
}

function writeCachedRun(workflowId: string | undefined, run: CachedRun): void {
  if (!workflowId || typeof window === "undefined") return;
  try {
    const serialized = JSON.stringify(run);
    if (serialized.length > MAX_CACHED_RUN_BYTES) return;
    window.localStorage.setItem(RUN_CACHE_PREFIX + workflowId, serialized);
  } catch {
    /* quota or unavailable storage: caching is best-effort */
  }
}

const _CONFIGURED = process.env.NEXT_PUBLIC_API_URL ?? "";

// The SSE stream deliberately bypasses the /api rewrite proxy that every
// other API call in this codebase uses (see lib/api.ts's BASE pattern, kept
// for those calls since they're quick request/response and benefit from the
// same-site cookie handling that proxy exists for). Confirmed live
// (2026-08-01) that Next's rewrite (both `next dev`'s built-in proxy,
// unverified but plausibly also true of Vercel's edge rewrite in
// production) does not correctly hold a long-lived text/event-stream
// connection open: a real x402 settlement taking ~30s+ (a completely
// normal duration for a real facilitator+algod round trip) caused the
// proxied stream to silently fail with a bare 500 after ~30s, never
// delivering a single log event, while the exact same run watched directly
// against the backend delivered every event correctly. The frontend has no
// way to tell "the run legitimately finished with nothing to show" apart
// from "my SSE connection just broke" (see the identical es.onerror
// handling below), so this presented as "run complete · 0/0 nodes
// succeeded" for a run that had, in fact, fully succeeded and moved real
// money.
//
// Safe to go directly to the backend here specifically because it already
// sets the auth cookie as SameSite=None; Secure whenever its own BASE_URL
// is https (auth.go's setAuthCookie) -- exactly the condition under which a
// direct cross-origin EventSource with withCredentials:true correctly
// carries it. That's true in real production (Railway's BASE_URL is https)
// and was true in the local setup this bug was diagnosed against (BASE_URL
// pointed at an https cloudflared tunnel).
const SSE_BASE = _CONFIGURED;

interface UseRunTranscriptArgs {
  runId: string | null;
  running: boolean;
  onRunComplete: () => void;
  // Scopes the cached last-run transcript, so reopening a workflow shows that
  // workflow's own last result rather than whatever ran most recently.
  workflowId?: string;
}

export interface RunTranscript {
  logs: LogEvent[];
  elapsed: number | null;
  done: boolean;
  /** Lease opened by a Tendril rent step in this run, if any. */
  leaseId: string | null;
}

export function useRunTranscript({
  runId,
  running,
  onRunComplete,
  workflowId,
}: UseRunTranscriptArgs): RunTranscript {
  // With no live run, start from this workflow's cached last transcript so
  // reopening it still shows what happened, instead of an empty console.
  const cached = runId ? null : readCachedRun(workflowId);
  const [logs, setLogs] = useState<LogEvent[]>(cached?.logs ?? []);
  const [elapsed, setElapsed] = useState<number | null>(
    cached?.elapsed ?? null,
  );
  const [done, setDone] = useState(!!cached);
  const esRef = useRef<EventSource | null>(null);
  const startRef = useRef<number | null>(null);
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null);

  // onRunComplete is invoked from inside the SSE effect, which must stay keyed
  // on runId alone -- re-running it because the parent passed a fresh closure
  // would tear down and re-open the stream mid-run. Held in a ref so the
  // effect's dependency list is identical to the one LogDrawer had.
  const onRunCompleteRef = useRef(onRunComplete);
  useEffect(() => {
    onRunCompleteRef.current = onRunComplete;
  }, [onRunComplete]);

  // Connect SSE for the run. No state resets needed: the parent renders the
  // console with key={runId}, so a new run remounts it with fresh initial
  // state.
  useEffect(() => {
    if (!runId) return;
    startRef.current = Date.now();

    // Start elapsed timer
    timerRef.current = setInterval(() => {
      setElapsed(Math.floor((Date.now() - startRef.current!) / 100) / 10);
    }, 100);

    const url = SSE_BASE ? `${SSE_BASE}/runs/${runId}/stream` : null;

    if (!url) return;

    // withCredentials sends the HttpOnly auth cookie automatically.
    const es = new EventSource(url, { withCredentials: true });
    esRef.current = es;

    es.addEventListener("log", (e) => {
      try {
        const ev: LogEvent = JSON.parse((e as MessageEvent).data);
        setLogs((prev) => {
          // Replace running entry for same nodeId, or append
          const idx = prev.findIndex(
            (l) => l.nodeId === ev.nodeId && l.status === "running",
          );
          if (idx >= 0) {
            const next = [...prev];
            next[idx] = ev;
            return next;
          }
          return [...prev, ev];
        });
      } catch {
        /* ignore parse errors */
      }
    });

    // The live stream only delivers events to a client subscribed at the
    // exact moment they're published (broker.go's Publish is a non-blocking,
    // unbuffered-per-subscriber send with no replay) -- a run that finishes
    // a step before this component's SSE connection is fully established
    // drops that step's event silently. Reconciling against the DB-backed
    // /runs/:id record once the stream ends closes that gap: this is what
    // actually fixes "run complete · 0/0 nodes succeeded" for a run that, in
    // truth, ran and failed (or succeeded) with a real, visible reason.
    // The DB is the only reliable source here: the live stream can deliver
    // nothing at all (confirmed 2026-08-02 — a 37s connection that carried a
    // single keepalive and zero events), and even when it works it can miss
    // steps. One shot at stream-close was not enough: a node's row is still
    // `running` until its work finishes and UpdateRunLog lands, so a single
    // fetch fired the moment the stream died filtered that row out and showed
    // an empty console for a run that had really completed and paid. Poll
    // until the run itself reports a terminal status.
    let cancelled = false;
    const TERMINAL = new Set(["success", "failed", "stopped"]);

    const mergeDBLogs = (dbLogs: RunLogRecord[]) => {
      setLogs((prev) => {
        // Merge, never replace: a stream-delivered entry carries the node's
        // full output, so it always wins over the stored row.
        const seen = new Set(prev.map((l) => l.nodeId));
        const missing = dbLogs
          .filter(
            (l) =>
              !seen.has(l.nodeId) &&
              (l.status === "success" || l.status === "failed"),
          )
          .map((l) => ({
            stepIndex: l.stepIndex,
            nodeId: l.nodeId,
            nodeType: l.nodeType,
            status: l.status as "success" | "failed",
            output: l.output,
            durationMs: l.durationMs ?? 0,
            ts: l.ts,
          }));
        if (missing.length === 0) return prev;
        return [...prev, ...missing].sort((a, b) => a.stepIndex - b.stepIndex);
      });
    };

    const reconcile = async () => {
      if (!runId) return;
      // Budget generously. A single agent step routinely runs ~70s (LLM
      // round trips plus a real x402 settlement per tool call, confirmed
      // 2026-08-02), and an agent may take several tool iterations — while
      // the SSE stream dies around 35s, so polling starts long before the
      // run ends. A budget that expires first shows an empty console for a
      // run that completed and charged the user, which is exactly the
      // failure this polling exists to prevent.
      for (let attempt = 0; attempt < 150 && !cancelled; attempt++) {
        try {
          const { run, logs: dbLogs } = await runsApi.get(runId);
          if (cancelled) return;
          if (dbLogs.length > 0) mergeDBLogs(dbLogs);
          if (TERMINAL.has(run.status)) {
            // The run itself is finished — this, not a dropped stream, is
            // what "complete" means.
            clearInterval(timerRef.current!);
            setDone(true);
            onRunCompleteRef.current();
            return;
          }
        } catch {
          // Transient failure: keep polling rather than giving up on a run
          // whose result is already recorded server-side.
        }
        await new Promise((r) => setTimeout(r, 2000));
      }
    };

    es.addEventListener("done", () => {
      clearInterval(timerRef.current!);
      setDone(true);
      onRunCompleteRef.current();
      es.close();
      void reconcile();
    });

    // A dropped stream says nothing about the run. EventSource fires onerror
    // on any transient hiccup (including its own reconnect attempts), and
    // treating that as "finished" reported "run complete · 1/1 nodes
    // succeeded" three seconds into a run whose agent still had ~60s of work
    // left — then closed the stream, guaranteeing the real result never
    // arrived live. Hand off to polling instead and let the run's own status
    // decide when it is over.
    es.onerror = () => {
      es.close();
      void reconcile();
    };

    return () => {
      cancelled = true;
      clearInterval(timerRef.current!);
      es.close();
    };
  }, [runId]);

  // Close SSE when stopped externally
  useEffect(() => {
    if (!running && esRef.current) {
      clearInterval(timerRef.current!);
      esRef.current.close();
      esRef.current = null;
    }
  }, [running]);

  // Persist any settlements this run produced, scoped to the signed-in user,
  // so the usage page can show them (see lib/settlements.ts for why this is
  // client-side for now).
  useEffect(() => {
    if (!done || logs.length === 0) return;
    const rows = logs
      .filter((l) => isX402Payment(l.output))
      .map((l) => {
        const p = l.output as X402Payment & { settledUsdMicros?: number };
        return {
          ts: l.ts,
          endpoint: p.nodeName ?? "x402 endpoint",
          amountAlgo: (p.settledUsdMicros ?? 0) / 1e6,
          txId: p.txId,
          explorerURL: p.explorerURL ?? "",
          workflowId: workflowId ?? "",
        };
      });
    if (rows.length === 0) return;
    let stale = false;
    authApi
      .me()
      .then((u) => {
        if (!stale) recordSettlements(u.id, rows);
      })
      .catch(() => {
        /* not signed in / offline: nothing to attribute the rows to */
      });
    return () => {
      stale = true;
    };
  }, [done, logs, workflowId]);

  // Cache the finished transcript for this workflow. Runs after `done` so it
  // captures the reconciled set (including any steps the live stream missed),
  // not a partial mid-run snapshot.
  useEffect(() => {
    if (!done || !runId || logs.length === 0) return;
    writeCachedRun(workflowId, { runId, logs, elapsed });
  }, [done, runId, logs, elapsed, workflowId]);

  // Detect the lease a Tendril rent step in this run opened, so the console
  // can offer a Terminal tab into it. Takes the most recent one — a run
  // renting two machines would need explicit lease-id wiring between nodes,
  // out of scope here (see the plan's "Multiple concurrent leases per run").
  const leaseId = useMemo(() => {
    for (let i = logs.length - 1; i >= 0; i--) {
      const output = logs[i].output;
      if (
        logs[i].nodeType === "tendril" &&
        typeof output === "object" &&
        output !== null &&
        "agentMeshLeaseId" in output
      ) {
        const id = (output as { agentMeshLeaseId?: unknown }).agentMeshLeaseId;
        if (typeof id === "string" && id) return id;
      }
    }
    return null;
  }, [logs]);

  return { logs, elapsed, done, leaseId };
}
