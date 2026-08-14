"use client";
import { ChatPane } from "./ChatPane";
import type { ChatSession } from "./useChatSession";

// The chat-enabled counterpart to Inspector's rail slot: same width/resize
// mechanics (owned by CanvasPage), but the tall column reads a conversation
// instead of a node's config. Node config for chat workflows moves to the
// bottom dock instead -- see CanvasPage's inspectorNode wiring.

interface ChatRailProps {
  session: ChatSession;
  onSend: (text: string) => void;
  busy: boolean;
  onShowLogs?: () => void;
  width?: number;
}

export function ChatRail({
  session,
  onSend,
  busy,
  onShowLogs,
  width = 320,
}: ChatRailProps) {
  return (
    <div
      style={{
        width,
        flexShrink: 0,
        minWidth: 0,
        borderLeft: "1px solid var(--border)",
        background: "var(--bg-elev-1)",
        height: "100%",
        display: "flex",
        flexDirection: "column",
      }}
    >
      <div
        style={{
          padding: "14px 16px",
          borderBottom: "1px solid var(--border)",
          flexShrink: 0,
        }}
      >
        <span
          style={{
            fontFamily: "var(--font-mono)",
            fontSize: 10,
            color: "var(--fg-muted)",
            textTransform: "uppercase",
            letterSpacing: "0.08em",
          }}
        >
          chat
        </span>
      </div>
      <div style={{ flex: 1, minHeight: 0, display: "flex" }}>
        <ChatPane
          session={session}
          onSend={onSend}
          busy={busy}
          onShowLogs={onShowLogs}
        />
      </div>
    </div>
  );
}
