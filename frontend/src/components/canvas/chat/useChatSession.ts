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
  /** Activity-strip figures, filled in when the run finishes. */
  toolCount?: number;
  elapsedS?: number;
  spendUSD?: number;
}

export interface ChatSession {
  messages: ChatMessage[];
  sessionId: string;
  /** Records the user's turn plus the pending assistant turn awaiting it. */
  startTurn: (text: string) => void;
  /** Binds the pending turn to the run the backend actually started. */
  attachRun: (runId: string) => void;
  /** Fills the pending turn in with the finished run's answer. */
  completeTurn: (patch: Omit<ChatMessage, "id" | "sender" | "ts">) => void;
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

  const startTurn = useCallback((text: string) => {
    const now = new Date().toISOString();
    setMessages((prev) => [
      ...prev,
      { id: `u-${now}-${prev.length}`, sender: "user", text, ts: now },
      {
        id: `a-${now}-${prev.length}`,
        sender: "assistant",
        text: "",
        ts: now,
        pending: true,
      },
    ]);
  }, []);

  const attachRun = useCallback((runId: string) => {
    setMessages((prev) => {
      const idx = prev.findIndex((m) => m.pending);
      if (idx < 0) return prev;
      const next = [...prev];
      next[idx] = { ...next[idx], runId };
      return next;
    });
  }, []);

  const completeTurn = useCallback(
    (patch: Omit<ChatMessage, "id" | "sender" | "ts">) => {
      setMessages((prev) => {
        const idx = prev.findIndex((m) => m.pending);
        if (idx < 0) return prev;
        const next = [...prev];
        next[idx] = { ...next[idx], ...patch, pending: false };
        return next;
      });
    },
    [],
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
    completeTurn,
    reset,
    hydrated,
  };
}
