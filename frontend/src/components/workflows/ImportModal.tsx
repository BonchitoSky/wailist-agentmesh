"use client";
import { useState } from "react";
import { IconClose } from "@/components/ui";
import { useModalDismissal } from "@/hooks/useModalDismissal";
import { shares as sharesApi, workflows as workflowsApi } from "@/lib/api";
import { classifyShareInput } from "@/lib/shareInput";
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
export function ImportModal({
  onClose,
  onImported,
}: {
  onClose: () => void;
  onImported: (id: string) => void;
}) {
  const [input, setInput] = useState("");
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
  const importCode = async (code: string): Promise<string> => {
    const data = await decodeWorkflowShare(code);
    const wf = await workflowsApi.importGraph({
      name: data.name?.trim() || "Imported workflow",
      nodes: data.nodes,
      edges: data.edges,
    });
    return wf.id;
  };

  const handleImport = async () => {
    if (!input.trim() || importing) return;
    setImporting(true);
    setError(null);

    const parsed = classifyShareInput(input);
    if (!parsed) {
      setError("paste a link or a code first");
      setImporting(false);
      return;
    }

    try {
      if (parsed.kind === "code") {
        onImported(await importCode(parsed.code));
        return;
      }
      try {
        const wf = await sharesApi.importInto(parsed.token);
        onImported(wf.id);
      } catch (tokenErr) {
        // A bare base64url string is shaped like a token AND like a short
        // legacy code -- see classifyShareInput. The lookup failing is what
        // settles it, so the paste gets its second reading rather than an
        // error about a link that was never a link. If the code reading fails
        // too, the token error is the honest one to show: it is far and away
        // the likelier thing somebody pasted.
        if (input.trim() !== parsed.token) throw tokenErr;
        try {
          onImported(await importCode(parsed.token));
        } catch {
          throw tokenErr;
        }
      }
    } catch (e) {
      setError(
        e instanceof Error
          ? e.message
          : "could not import that -- check the link or code and try again",
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

        <textarea
          autoFocus
          value={input}
          onChange={(e) => {
            setInput(e.target.value);
            setError(null);
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

        <button
          type="button"
          onClick={handlePasteFromClipboard}
          className="share-ghost-btn"
          style={{ width: "100%", marginBottom: 14 }}
        >
          <IconPaste /> Paste from clipboard
        </button>

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
          <button type="button" onClick={onClose} className="share-ghost-btn">
            Cancel
          </button>
          <button
            type="button"
            disabled={!input.trim() || importing}
            onClick={handleImport}
            className="share-primary-btn"
            style={{ flex: 1, width: "auto" }}
          >
            {importing ? "Importing…" : "Import"}
          </button>
        </div>
      </div>
    </div>
  );
}
