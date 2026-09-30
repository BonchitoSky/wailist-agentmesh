"use client";
import { useState } from "react";
import { IconClose } from "@/components/ui";
import { useModalDismissal } from "@/hooks/useModalDismissal";
import { shares as sharesApi, workflows as workflowsApi } from "@/lib/api";
import { classifyShareInput } from "@/lib/shareInput";
import { requirementLines } from "@/lib/shareRequirements";
import type { ShareImportRequirements, WorkflowShare } from "@/lib/types";
import type { WorkflowShareData } from "@/lib/workflowShare";
import { decodeWorkflowShare } from "@/lib/workflowShare";

// One box, three kinds of paste: a link, a bare token, or a code.
//
// Which one it is gets worked out in lib/shareInput.ts rather than here, and
// the person pasting is never asked to say. They were handed one string by a
// friend; being made to classify it first is exactly the friction that leaves
// a feature unused.

const IconPaste = ({ size = 13 }: { size?: number }) => (
  <svg width={size} height={size} viewBox="0 0 16 16" fill="none">
    <rect
      x="4"
      y="2.5"
      width="8"
      height="11"
      rx="1.3"
      stroke="currentColor"
      strokeWidth="1.3"
    />
    <path
      d="M6.2 2.5V2a1 1 0 0 1 1-1h1.6a1 1 0 0 1 1 1v.5"
      stroke="currentColor"
      strokeWidth="1.3"
    />
  </svg>
);

// Mounted only while open (the parent renders it conditionally on importOpen,
// the same pattern AddToWorkflowDialog uses) -- so each open is a fresh mount
// and a fresh slate, with no reset-on-open effect needed.
// What the paste turned out to be, once it has been looked at but before
// anything has been created.
//
// A link resolves against the backend, which answers with the same snapshot
// and requirements the public /s/ page shows. A code is decoded locally and
// has no requirements: those are derived server-side from a stored snapshot,
// and a code has no row anywhere until it is imported.
type Resolved =
  | {
      kind: "link";
      token: string;
      share: WorkflowShare;
      req: ShareImportRequirements;
    }
  | { kind: "code"; data: WorkflowShareData };

export function ImportModal({
  onClose,
  onImported,
}: {
  onClose: () => void;
  onImported: (id: string) => void;
}) {
  const [input, setInput] = useState("");
  const [resolving, setResolving] = useState(false);
  const [resolved, setResolved] = useState<Resolved | null>(null);
  const [importing, setImporting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useModalDismissal(onClose);

  const handlePasteFromClipboard = async () => {
    try {
      const text = await navigator.clipboard.readText();
      setInput(text);
      setError(null);
    } catch {
      setError(
        "clipboard read was blocked -- paste manually with Ctrl+V (Cmd+V on Mac)",
      );
    }
  };

  // A code goes through POST /workflows/import, not create-then-update. That
  // endpoint is atomic and runs the graph through the server's sanitiser --
  // the ordinary save path deliberately passes an "enc:"-prefixed value
  // through untouched, which is right for the canvas round-tripping its own
  // ciphertext and wrong for a stranger's paste.
  // Look at the paste; create nothing.
  //
  // Importing used to happen on the first press, so a pasted link produced a
  // workflow sight unseen -- while the very same link opened in a browser
  // showed a page saying what it was and what it would need. Same link, two
  // completely different amounts of respect for the person holding it.
  const resolve = async () => {
    if (!input.trim() || resolving) return;
    setResolving(true);
    setError(null);

    const parsed = classifyShareInput(input);
    if (!parsed) {
      setError("paste a link or a code first");
      setResolving(false);
      return;
    }

    try {
      if (parsed.kind === "code") {
        setResolved({
          kind: "code",
          data: await decodeWorkflowShare(parsed.code),
        });
        return;
      }
      try {
        const res = await sharesApi.read(parsed.token);
        setResolved({
          kind: "link",
          token: parsed.token,
          share: res.share,
          req: res.requirements,
        });
      } catch (tokenErr) {
        // A bare base64url string is shaped like a token AND like a short
        // legacy code -- see classifyShareInput. The lookup failing is what
        // settles it, so the paste gets its second reading rather than an
        // error about a link that was never a link. If the code reading fails
        // too, the token error is the honest one to show: it is far and away
        // the likelier thing somebody pasted.
        if (input.trim() !== parsed.token) throw tokenErr;
        try {
          setResolved({
            kind: "code",
            data: await decodeWorkflowShare(parsed.token),
          });
        } catch {
          throw tokenErr;
        }
      }
    } catch (e) {
      setError(
        e instanceof Error
          ? e.message
          : "could not read that -- check the link or code and try again",
      );
    } finally {
      setResolving(false);
    }
  };

  const confirmImport = async () => {
    if (!resolved || importing) return;
    setImporting(true);
    setError(null);
    try {
      if (resolved.kind === "link") {
        const wf = await sharesApi.importInto(resolved.token);
        onImported(wf.id);
        return;
      }
      const { data } = resolved;
      const wf = await workflowsApi.importGraph({
        name: data.name?.trim() || "Imported workflow",
        description: data.description?.trim() || undefined,
        nodes: data.nodes,
        edges: data.edges,
      });
      onImported(wf.id);
    } catch (e) {
      setError(
        e instanceof Error ? e.message : "could not import this workflow",
      );
      setImporting(false);
    }
  };

  return (
    <div
      role="presentation"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
      style={{
        position: "fixed",
        inset: 0,
        zIndex: 1000,
        background: "rgba(8,7,12,0.72)",
        backdropFilter: "blur(4px)",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        padding: 24,
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Import workflow"
        style={{
          width: "100%",
          maxWidth: 480,
          border: "1px solid var(--border-strong)",
          borderRadius: "var(--r-4)",
          background: "var(--bg-elev-1)",
          color: "var(--fg)",
          boxShadow: "0 24px 64px rgba(0,0,0,0.55)",
          padding: 24,
        }}
      >
        <div
          style={{
            display: "flex",
            alignItems: "flex-start",
            justifyContent: "space-between",
            marginBottom: 16,
          }}
        >
          <div>
            <h2
              style={{
                fontSize: 17,
                fontWeight: 700,
                margin: 0,
                letterSpacing: "-0.01em",
              }}
            >
              Import workflow
            </h2>
            <p
              style={{
                margin: "3px 0 0",
                fontSize: 12.5,
                color: "var(--fg-muted)",
                maxWidth: "60ch",
              }}
            >
              Paste a share link or a code
            </p>
          </div>
          <button
            type="button"
            aria-label="Close"
            onClick={onClose}
            className="share-icon-btn"
          >
            <IconClose size={13} />
          </button>
        </div>

        {resolved ? (
          <ResolvedCard resolved={resolved} />
        ) : (
          <textarea
            autoFocus
            value={input}
            onChange={(e) => {
              setInput(e.target.value);
              setError(null);
            }}
            // Enter has to stay a newline -- a code that arrived wrapped is
            // pasted as several lines, and swallowing Enter would make fixing
            // one by hand impossible. Ctrl/Cmd+Enter is the usual way out of
            // that, and without it the only route forward was the mouse.
            onKeyDown={(e) => {
              if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                e.preventDefault();
                void resolve();
              }
            }}
            placeholder="https://www.agent-mesh.app/s/… or am1.…"
            style={{
              width: "100%",
              height: 100,
              resize: "none",
              fontFamily: "var(--font-mono)",
              fontSize: 11,
              lineHeight: 1.5,
              padding: 10,
              background: "var(--bg-elev-2)",
              border: "1px solid var(--border)",
              borderRadius: "var(--r-2)",
              color: "var(--fg)",
              marginBottom: 10,
              wordBreak: "break-all",
            }}
          />
        )}

        {!resolved && (
          <button
            type="button"
            onClick={handlePasteFromClipboard}
            className="share-ghost-btn"
            style={{ width: "100%", marginBottom: 14 }}
          >
            <IconPaste /> Paste from clipboard
          </button>
        )}

        {error && (
          <div
            style={{
              fontSize: 12,
              color: "var(--danger)",
              marginBottom: 12,
              maxWidth: "60ch",
            }}
          >
            {error}
          </div>
        )}

        <div style={{ display: "flex", gap: 8 }}>
          <button
            type="button"
            onClick={() => {
              // Back to the paste box rather than out of the dialog: having
              // looked at one link, the likeliest next move is trying a
              // different one.
              if (resolved) {
                setResolved(null);
                setError(null);
                return;
              }
              onClose();
            }}
            className="share-ghost-btn"
          >
            {resolved ? "Back" : "Cancel"}
          </button>
          <button
            type="button"
            disabled={
              resolved ? importing : !input.trim() || resolving || importing
            }
            onClick={resolved ? confirmImport : resolve}
            className="share-primary-btn"
            style={{ flex: 1, width: "auto" }}
          >
            {resolved
              ? importing
                ? "Importing…"
                : "Import to my workspace"
              : resolving
                ? "Looking…"
                : "Continue"}
          </button>
        </div>
      </div>
    </div>
  );
}

// What the paste turned out to be, shown before anything is created.
//
// Deliberately the same three things the public /s/ page leads with -- what
// it is called, how big it is, and what the importer will have to supply --
// so the two ways into the same workflow do not describe it differently.
function ResolvedCard({ resolved }: { resolved: Resolved }) {
  const name =
    resolved.kind === "link"
      ? resolved.share.name
      : resolved.data.name || "Untitled workflow";
  const description =
    resolved.kind === "link"
      ? resolved.share.description
      : resolved.data.description;
  const nodes =
    resolved.kind === "link"
      ? resolved.share.nodeCount
      : resolved.data.nodes.length;
  const edges =
    resolved.kind === "link"
      ? resolved.share.edgeCount
      : resolved.data.edges.length;
  // Only a link has requirements: they are derived server-side from a stored
  // snapshot, and a code has no row anywhere until it is imported.
  const lines = resolved.kind === "link" ? requirementLines(resolved.req) : [];

  return (
    <div
      className="reveal"
      style={{
        border: "1px solid var(--border-soft)",
        borderRadius: "var(--r-2)",
        background: "var(--bg-elev-2)",
        padding: "13px 15px",
        marginBottom: 14,
      }}
    >
      <p
        style={{
          margin: "0 0 4px",
          fontSize: 15,
          fontWeight: 700,
          letterSpacing: "-0.01em",
        }}
      >
        {name}
      </p>
      {description && (
        <p
          style={{
            margin: "0 0 8px",
            fontSize: 12.5,
            lineHeight: 1.6,
            color: "var(--fg-muted)",
            maxWidth: "60ch",
          }}
        >
          {description}
        </p>
      )}
      <p
        style={{
          margin: "0 0 10px",
          fontFamily: "var(--font-mono)",
          fontVariantNumeric: "tabular-nums",
          fontSize: 11.5,
          color: "var(--fg-dim)",
        }}
      >
        {nodes} nodes · {edges} connections
      </p>

      <p style={{ margin: "0 0 5px", fontSize: 12, fontWeight: 600 }}>
        {lines.length > 0 ? "You'll need to add" : "Ready to run as-is"}
      </p>
      {lines.length > 0 ? (
        <ul
          style={{
            margin: 0,
            paddingLeft: 17,
            fontSize: 12,
            lineHeight: 1.7,
            color: "var(--fg-muted)",
            maxWidth: "60ch",
          }}
        >
          {lines.map((line) => (
            <li key={line}>{line}</li>
          ))}
        </ul>
      ) : (
        <p
          style={{
            margin: 0,
            fontSize: 12,
            lineHeight: 1.6,
            color: "var(--fg-muted)",
            maxWidth: "60ch",
          }}
        >
          {resolved.kind === "link"
            ? "Nothing to configure — this one runs on AgentMesh's own model keys."
            : "The sender's keys, secrets and uploaded files were never part of this code."}
        </p>
      )}
    </div>
  );
}
