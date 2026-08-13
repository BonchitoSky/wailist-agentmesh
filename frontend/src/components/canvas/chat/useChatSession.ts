"use client";
import { useCallback, useEffect, useState } from "react";

// The chat transcript for one workflow.
//
// Persisted to localStorage because the console is remounted on every run
// (CanvasPage renders it with key={runId}), so in-memory state would be wiped
// the instant a message is sent. Writing the user's message *before* the run
// starts and hydrating on mount is what carries a turn across that remount.
//
// Scoped per workflow so opening a different workflow shows its own
// conversation rather than whatever was typed last, matching how
// useRunTranscript scopes its cached transcript.

export interface ChatMessage {
  id: string;
  sender: "user" | "assistant";
  text: string;
  /** ISO-8601 UTC. */
  ts: string;
  /** The run this message belongs to; set once the backend returns a run id. */
  runId?: string;
  /** An assistant turn still waiting on its run. */
  pending?: boolean;
  isError?: boolean;
  /**
   * The turn was stranded — a reload cut it off from its run. Distinct from
   * isError: the run itself may well have succeeded, we just stopped watching.
   */
  interrupted?: boolean;
  /** Activity-strip figures, filled in when the run finishes. */
  toolCount?: number;
  elapsedS?: number;
  spendUSD?: number;
}

export interface ChatSession {
  messages: ChatMessage[];
  sessionId: string;
  /**
   * Records the user's turn plus the pending assistant turn awaiting it.
   * Returns the assistant turn's id so the caller can settle that exact turn
   * if the run never starts.
   */
  startTurn: (text: string) => string;
  /** Binds the pending turn to the run the backend actually started. */
  attachRun: (runId: string) => void;
  /** Fills in the turn bound to this run. */
  completeTurnForRun: (
    runId: string,
    patch: Omit<ChatMessage, "id" | "sender" | "ts">,
  ) => void;
  /** Fills in one exact turn, by id. */
  completeTurnById: (
    id: string,
    patch: Omit<ChatMessage, "id" | "sender" | "ts">,
  ) => void;
  /** Clears the transcript and starts a new session id. */
  reset: () => void;
  hydrated: boolean;
}

const CHAT_PREFIX = "agentmesh_chat_";
// Keep the stored transcript bounded on both axes: a long conversation of
// large agent answers would otherwise grow without limit in a ~5 MB store
// shared with the run cache. Only the *stored* history is trimmed.
const MAX_STORED_MESSAGES = 60;
const MAX_STORED_BYTES = 256 * 1024;

interface StoredSession {
  sessionId: string;
  messages: ChatMessage[];
}

/**
 * Index of the most recent turn still waiting on a run, or -1.
 *
 * Deliberately the LAST pending turn, not the first. A turn stranded by a
 * reload sits earlier in the transcript than any turn sent afterwards, so
 * matching the first pending would hand a fresh turn's answer to the stale
 * bubble and leave the fresh one spinning — one interrupted run would corrupt
 * every turn after it. Recovery on hydration clears strays too; this is the
 * cheap structural guard that holds even if one slips through.
 */
function lastPendingIndex(messages: ChatMessage[]): number {
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i].pending) return i;
  }
  return -1;
}

/**
 * Settle the first pending turn matching `match`. Exported for tests: the
 * targeting rules here are what keep an answer attached to the question that
 * asked it, so they are worth asserting directly.
 */
export function settleIn(
  messages: ChatMessage[],
  match: (m: ChatMessage) => boolean,
  patch: Omit<ChatMessage, "id" | "sender" | "ts">,
): ChatMessage[] {
  const idx = messages.findIndex((m) => m.pending && match(m));
  if (idx < 0) return messages;
  const next = [...messages];
  next[idx] = { ...next[idx], ...patch, pending: false };
  return next;
}

function newSessionId(): string {
  // crypto.randomUUID is unavailable on http:// origins in some browsers, and
  // this id is a display/reset handle rather than a security token.
  return Math.random().toString(36).slice(2, 10);
}

function read(workflowId: string | undefined): StoredSession | null {
  if (!workflowId || typeof window === "undefined") return null;
  try {
    const raw = window.localStorage.getItem(CHAT_PREFIX + workflowId);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as StoredSession;
    return Array.isArray(parsed.messages) ? parsed : null;
  } catch {
    return null;
  }
}

function write(workflowId: string | undefined, session: StoredSession): void {
  if (!workflowId || typeof window === "undefined") return;
  try {
    const trimmed: StoredSession = {
      sessionId: session.sessionId,
      messages: session.messages.slice(-MAX_STORED_MESSAGES),
    };
    const serialized = JSON.stringify(trimmed);
    if (serialized.length > MAX_STORED_BYTES) return;
    window.localStorage.setItem(CHAT_PREFIX + workflowId, serialized);
  } catch {
    /* quota or unavailable storage: persistence is best-effort */
  }
}

export function useChatSession(workflowId: string | undefined): ChatSession {
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [sessionId, setSessionId] = useState("");
  // Hydration happens in an effect, not in useState's initializer, so the
  // server render and the first client render agree -- the same reason
  // ConsolePanel restores its height this way. Applied on the next frame
  // rather than synchronously in the effect body for that same reason: a
  // straight setState here trips the cascading-render rule.
  const [hydrated, setHydrated] = useState(false);

  useEffect(() => {
    const id = requestAnimationFrame(() => {
      const stored = read(workflowId);
      setMessages(stored?.messages ?? []);
      setSessionId(stored?.sessionId ?? newSessionId());
      setHydrated(true);
    });
    return () => cancelAnimationFrame(id);
  }, [workflowId]);

  // Persist on every change once hydrated. Guarded on `hydrated` so the
  // initial empty state can't overwrite a stored transcript before it loads.
  useEffect(() => {
    if (!hydrated || !sessionId) return;
    write(workflowId, { sessionId, messages });
  }, [hydrated, workflowId, sessionId, messages]);

  const startTurn = useCallback((text: string): string => {
    const now = new Date().toISOString();
    // Ids are built outside the updater so the assistant turn's id can be
    // returned; deriving them from prev.length kept them inside the closure
    // and left the caller with no handle on the turn it had just created.
    const seq = Math.random().toString(36).slice(2, 8);
    const assistantId = `a-${now}-${seq}`;
    setMessages((prev) => [
      ...prev,
      { id: `u-${now}-${seq}`, sender: "user", text, ts: now },
      {
        id: assistantId,
        sender: "assistant",
        text: "",
        ts: now,
        pending: true,
      },
    ]);
    return assistantId;
  }, []);

  const attachRun = useCallback((runId: string) => {
    setMessages((prev) => {
      const idx = lastPendingIndex(prev);
      if (idx < 0) return prev;
      const next = [...prev];
      next[idx] = { ...next[idx], runId };
      return next;
    });
  }, []);

  // Settling a turn targets it by identity, never by position. Position was
  // wrong in two ways: a recovery result could land on a turn sent while its
  // fetch was in flight, and two runs started before the first settled would
  // resolve in the wrong order. Both showed up as an answer appearing under
  // somebody else's question.
  const settle = useCallback(
    (
      match: (m: ChatMessage) => boolean,
      patch: Omit<ChatMessage, "id" | "sender" | "ts">,
    ) => setMessages((prev) => settleIn(prev, match, patch)),
    [],
  );

  /** Settle the turn bound to this run. Survives the key={runId} remount. */
  const completeTurnForRun = useCallback(
    (runId: string, patch: Omit<ChatMessage, "id" | "sender" | "ts">) =>
      settle((m) => m.runId === runId, patch),
    [settle],
  );

  /** Settle one exact turn — used by recovery, which knows the stranded id. */
  const completeTurnById = useCallback(
    (id: string, patch: Omit<ChatMessage, "id" | "sender" | "ts">) =>
      settle((m) => m.id === id, patch),
    [settle],
  );

  const reset = useCallback(() => {
    setMessages([]);
    setSessionId(newSessionId());
  }, []);

  return {
    messages,
    sessionId,
    startTurn,
    attachRun,
    completeTurnForRun,
    completeTurnById,
    reset,
    hydrated,
  };
}
