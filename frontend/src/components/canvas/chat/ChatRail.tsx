"use client";
import { useState } from "react";
import { ChatPane } from "./ChatPane";
import { TerminalTab } from "../TerminalTab";
import type { ChatSession } from "./useChatSession";

// The studio's right-hand column: one tall panel that toggles between the
// conversation, the selected node's config, and the run's Tendril terminal.
// Width/resize mechanics are owned by CanvasPage; the bottom dock is purely a
// log viewer and holds none of these.

export type RailTab = "chat" | "inspector" | "terminal";

interface ChatRailProps {
  session: ChatSession;
  onSend: (text: string) => void;
  busy: boolean;
  onShowLogs?: () => void;
  width?: number;
  // Build mode edits the graph instead of running the deployed agent.
  // canToggleBuildMode is false until a provider node exists -- before
  // that, build mode is forced on and there's nothing to toggle.
  buildMode?: boolean;
  canToggleBuildMode?: boolean;
  onToggleBuildMode?: () => void;
  /** Rendered under the INSPECT tab. Always mounted (hidden via CSS) so
   *  in-progress edits survive a tab switch. */
  inspectorNode: React.ReactNode;
  /** Drives the accent-dot badge on the INSPECT pill -- a node is selected
   *  but the user is looking at another tab. */
  hasSelection: boolean;
  /** Non-null only while the run holds a Tendril lease; gates the TERM pill. */
  leaseId: string | null;
}

export function ChatRail({
  session,
  onSend,
  busy,
  onShowLogs,
  width = 320,
  buildMode = false,
  canToggleBuildMode = false,
  onToggleBuildMode,
  inspectorNode,
  hasSelection,
  leaseId,
}: ChatRailProps) {
  const [tab, setTab] = useState<RailTab>("chat");
  // A lease that ends mid-run must not leave the rail stuck on a dead
  // terminal. Derived at render rather than synced through an effect: an
  // effect fires a second render pass and can lose a race with a pill click
  // in the same tick. "inspector" needs no fallback -- inspectorNode is
  // unconditional and renders its own empty state.
  const effectiveTab: RailTab = tab === "terminal" && !leaseId ? "chat" : tab;

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
          padding: "12px 12px",
          borderBottom: "1px solid var(--border)",
          flexShrink: 0,
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          // At INSPECTOR.min the Build/Run pill wraps to a second row rather
          // than clipping against the tab pills.
          flexWrap: "wrap",
          rowGap: 6,
          columnGap: 4,
        }}
      >
        <div style={{ display: "flex", gap: 4 }}>
          <TabPill
            label="Chat"
            active={effectiveTab === "chat"}
            onClick={() => setTab("chat")}
          />
          <TabPill
            label="Inspect"
            active={effectiveTab === "inspector"}
            badge={hasSelection && effectiveTab !== "inspector"}
            onClick={() => setTab("inspector")}
          />
          {leaseId && (
            <TabPill
              label="Term"
              active={effectiveTab === "terminal"}
              onClick={() => setTab("terminal")}
            />
          )}
        </div>
        {canToggleBuildMode ? (
          <button
            onClick={onToggleBuildMode}
            style={{
              fontFamily: "var(--font-mono)",
              fontSize: 9.5,
              textTransform: "uppercase",
              letterSpacing: "0.06em",
              padding: "2px 8px",
              borderRadius: 999,
              border: `1px solid ${buildMode ? "var(--accent)" : "var(--border)"}`,
              color: buildMode ? "var(--accent)" : "var(--fg-dim)",
              background: "transparent",
              cursor: "pointer",
            }}
          >
            {buildMode ? "Build" : "Run"}
          </button>
        ) : buildMode ? (
          <span
            style={{
              fontFamily: "var(--font-mono)",
              fontSize: 9.5,
              textTransform: "uppercase",
              letterSpacing: "0.06em",
              color: "var(--accent)",
            }}
          >
            build
          </span>
        ) : null}
      </div>

      <div
        style={{
          flex: 1,
          minHeight: 0,
          display: "flex",
          flexDirection: "column",
        }}
      >
        {/* Chat -- always mounted; ChatPane owns `draft` in local state, so
            unmounting would eat a half-typed message. */}
        <div
          style={{
            display: effectiveTab === "chat" ? "flex" : "none",
            flex: 1,
            minHeight: 0,
          }}
        >
          <ChatPane
            session={session}
            onSend={onSend}
            busy={busy}
            onShowLogs={onShowLogs}
          />
        </div>

        {/* Inspector -- always mounted so in-progress field edits survive a
            tab switch. */}
        <div
          style={{
            display: effectiveTab === "inspector" ? "flex" : "none",
            flex: 1,
            minHeight: 0,
            flexDirection: "column",
          }}
        >
          {inspectorNode}
        </div>

        {/* Terminal -- mounted only while selected: it owns a live WebSocket
            + SSH session, unlike the two above which are cheap to keep
            alive. */}
        {effectiveTab === "terminal" && leaseId && (
          <div style={{ flex: 1, minHeight: 0 }}>
            <TerminalTab leaseId={leaseId} onClose={() => setTab("chat")} />
          </div>
        )}
      </div>
    </div>
  );
}

function TabPill({
  label,
  active,
  badge = false,
  onClick,
}: {
  label: string;
  active: boolean;
  badge?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      style={{
        display: "inline-flex",
        alignItems: "center",
        gap: 5,
        fontFamily: "var(--font-mono)",
        fontSize: 9.5,
        textTransform: "uppercase",
        letterSpacing: "0.06em",
        padding: "2px 8px",
        borderRadius: 999,
        border: `1px solid ${active ? "var(--accent)" : "var(--border)"}`,
        color: active ? "var(--accent)" : "var(--fg-dim)",
        background: "transparent",
        cursor: "pointer",
      }}
    >
      {label}
      {badge && (
        <span
          style={{
            width: 5,
            height: 5,
            borderRadius: 999,
            background: "var(--accent)",
            flexShrink: 0,
          }}
        />
      )}
    </button>
  );
}
