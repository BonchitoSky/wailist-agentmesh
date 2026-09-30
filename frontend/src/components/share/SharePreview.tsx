"use client";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { Logo, Tag } from "@/components/ui";
import { useAuth } from "@/hooks/useAuth";
import { useReadOnly } from "@/hooks/useReadOnly";
import { can } from "@/lib/readonly";
import { shares } from "@/lib/api";
import { requirementLines } from "@/lib/shareRequirements";
import { shareHref, workflowHref } from "@/lib/routes";
import type { ShareImportRequirements, WorkflowShare } from "@/lib/types";

// What somebody sees when they open a link they were sent.
//
// The page is deliberately readable signed OUT -- see the route comment. The
// shape of it follows from that: show enough that a stranger can decide
// whether they want this before being asked to make an account, and be honest
// about what they will have to supply themselves, because a workflow that
// silently cannot run is worse than one that says what it needs.

// How a node type reads to somebody who has never used the app. The catalogue
// in lib/data.ts is keyed for the canvas palette and carries geometry we have
// no use for here, so this is a small separate mapping rather than a reach
// into that.
const NODE_LABELS: Record<string, string> = {
  trigger: "Trigger",
  agent: "Agent",
  provider: "Model",
  tool: "Tool",
  tool402: "Paid tool",
  action: "Action",
  state: "State",
  end: "End",
  tendril: "Compute",
  google: "Google",
};

function expiryNote(expiresAt?: string): string | null {
  if (!expiresAt) return null;
  const ms = new Date(expiresAt).getTime() - Date.now();
  if (Number.isNaN(ms)) return null;
  const days = Math.ceil(ms / 86_400_000);
  if (days <= 0) return "Expires today";
  return days === 1 ? "Expires tomorrow" : `Expires in ${days} days`;
}

export function SharePreview({ token }: { token: string }) {
  const router = useRouter();
  const { signedIn, loading: authLoading } = useAuth();
  // Importing creates a workflow, so it is authoring, and this app authors on
  // a computer only -- the same policy that withholds New workflow and the
  // canvas from a phone. Preview stays open to everyone, because reading what
  // somebody sent you is not authoring and a link is mostly opened on a phone.
  //
  // Without this the page offered an Import button on a phone that could not
  // work: assertWritable rejects POST /shares/{token}/import for any handheld
  // or native client, so pressing it produced an error instead of a workflow.
  // Saying so up front is the honest version of the same rule.
  const readOnly = useReadOnly();
  const canImport = can("workflow.create", readOnly);

  const [share, setShare] = useState<WorkflowShare | null>(null);
  const [requirements, setRequirements] =
    useState<ShareImportRequirements | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [importing, setImporting] = useState(false);
  const loading = !share && !error;

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await shares.read(token);
        if (!cancelled) {
          setShare(res.share);
          setRequirements(res.requirements);
        }
      } catch (e) {
        if (!cancelled) {
          setError(
            e instanceof Error
              ? e.message
              : "this share link is no longer available",
          );
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [token]);

  // Importing is always a press, never something that happens on arrival.
  //
  // An earlier version carried ?import=1 through sign-in and fired this from
  // an effect, to save the returning visitor a click. Two things were wrong
  // with that: a write triggered by navigation runs again on Back, quietly
  // giving somebody two copies of the same workflow, and it trips this repo's
  // react-hooks/set-state-in-effect rule. A signed-in visitor lands back here
  // with the button already saying "Import to my workspace", which is one
  // press and no ambiguity about when the copy was made.
  const handleImport = async () => {
    if (!signedIn) {
      // safeNextPath validates this on the other side; shareHref is already an
      // app-relative path, so it survives that check.
      router.push(`/signin?next=${encodeURIComponent(shareHref(token))}`);
      return;
    }
    setImporting(true);
    setError(null);
    try {
      const wf = await shares.importInto(token);
      router.push(workflowHref(wf.id));
    } catch (e) {
      setError(
        e instanceof Error ? e.message : "could not import this workflow",
      );
      setImporting(false);
    }
  };

  const lines = requirements ? requirementLines(requirements) : [];
  const expires = expiryNote(share?.expiresAt);

  return (
    <div
      style={{
        minHeight: "100dvh",
        background: "var(--bg)",
        color: "var(--fg)",
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        padding: "48px 20px 64px",
      }}
    >
      <header style={{ marginBottom: 32 }}>
        <Link
          href="/"
          aria-label="AgentMesh"
          style={{
            display: "inline-flex",
            alignItems: "center",
            gap: 9,
            color: "var(--fg)",
            textDecoration: "none",
          }}
        >
          {/* Logo draws the mark AND the wordmark, which is how Topbar and the
              sign-in screen use it. This page used to add a second
              "AgentMesh" beside it, so the header read the name twice. */}
          <Logo size={20} />
        </Link>
      </header>

      <main
        className="reveal"
        style={{
          width: "100%",
          maxWidth: 560,
          border: "1px solid var(--border)",
          borderRadius: "var(--r-3)",
          background: "var(--bg-elev-1)",
          padding: 28,
        }}
      >
        {loading && (
          <p style={{ margin: 0, fontSize: 13, color: "var(--fg-muted)" }}>
            Opening this link…
          </p>
        )}

        {error && !share && (
          <>
            <h1 style={{ fontSize: 18, fontWeight: 700, margin: "0 0 8px" }}>
              This link isn&apos;t available
            </h1>
            <p
              style={{
                margin: 0,
                fontSize: 13,
                lineHeight: 1.6,
                color: "var(--fg-muted)",
                maxWidth: "60ch",
              }}
            >
              It may have been revoked by whoever shared it, or it may have
              expired. Ask them for a fresh one.
            </p>
          </>
        )}

        {share && (
          <>
            <p
              style={{
                margin: "0 0 6px",
                fontSize: 11,
                letterSpacing: "0.08em",
                textTransform: "uppercase",
                color: "var(--fg-dim)",
              }}
            >
              Shared workflow
            </p>
            <h1
              style={{
                fontSize: 24,
                fontWeight: 700,
                margin: "0 0 10px",
                letterSpacing: "-0.02em",
              }}
            >
              {share.name}
            </h1>
            {share.description && (
              <p
                style={{
                  margin: "0 0 14px",
                  fontSize: 13.5,
                  lineHeight: 1.6,
                  color: "var(--fg-muted)",
                  maxWidth: "60ch",
                }}
              >
                {share.description}
              </p>
            )}

            <div
              style={{
                display: "flex",
                alignItems: "center",
                gap: 8,
                flexWrap: "wrap",
                marginBottom: 20,
                fontFamily: "var(--font-mono)",
                fontVariantNumeric: "tabular-nums",
                fontSize: 12,
                color: "var(--fg-muted)",
              }}
            >
              <span>{share.nodeCount} nodes</span>
              <span style={{ color: "var(--fg-dim)" }}>·</span>
              <span>{share.edgeCount} connections</span>
              {expires && (
                <>
                  <span style={{ color: "var(--fg-dim)" }}>·</span>
                  <span style={{ color: "var(--warm)" }}>{expires}</span>
                </>
              )}
            </div>

            {share.graph.nodes.length > 0 && (
              <div
                style={{
                  display: "flex",
                  flexWrap: "wrap",
                  gap: 6,
                  marginBottom: 22,
                }}
              >
                {share.graph.nodes.slice(0, 12).map((n) => (
                  <Tag key={n.id}>
                    {n.label || n.name || NODE_LABELS[n.type] || n.type}
                  </Tag>
                ))}
                {share.graph.nodes.length > 12 && (
                  <Tag>+{share.graph.nodes.length - 12} more</Tag>
                )}
              </div>
            )}

            <div
              style={{
                border: "1px solid var(--border-soft)",
                borderRadius: "var(--r-2)",
                background: "var(--bg-elev-2)",
                padding: "13px 15px",
                marginBottom: 20,
              }}
            >
              <p style={{ margin: "0 0 6px", fontSize: 12.5, fontWeight: 600 }}>
                {lines.length > 0 ? "You'll need to add" : "Ready to run as-is"}
              </p>
              {lines.length > 0 ? (
                <ul
                  style={{
                    margin: 0,
                    paddingLeft: 17,
                    fontSize: 12.5,
                    lineHeight: 1.75,
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
                    fontSize: 12.5,
                    lineHeight: 1.6,
                    color: "var(--fg-muted)",
                    maxWidth: "60ch",
                  }}
                >
                  Nothing to configure — this one runs on AgentMesh&apos;s own
                  model keys.
                </p>
              )}
              <p
                style={{
                  margin: "9px 0 0",
                  fontSize: 11.5,
                  lineHeight: 1.6,
                  color: "var(--fg-dim)",
                  maxWidth: "60ch",
                }}
              >
                The sender&apos;s API keys, secrets and uploaded files were
                never part of this link.
              </p>
            </div>

            {error && (
              <p
                style={{
                  margin: "0 0 12px",
                  fontSize: 12.5,
                  color: "var(--danger)",
                }}
              >
                {error}
              </p>
            )}

            {canImport ? (
              <>
                <button
                  type="button"
                  className="share-import-btn"
                  onClick={handleImport}
                  disabled={importing}
                >
                  {importing
                    ? "Importing…"
                    : signedIn
                      ? "Import to my workspace"
                      : "Sign in to import"}
                </button>
                {!signedIn && !authLoading && (
                  <p
                    style={{
                      margin: "10px 0 0",
                      fontSize: 11.5,
                      textAlign: "center",
                      color: "var(--fg-dim)",
                    }}
                  >
                    You&apos;ll come straight back here.
                  </p>
                )}
              </>
            ) : (
              <div
                style={{
                  border: "1px solid var(--border)",
                  borderRadius: "var(--r-2)",
                  padding: "13px 15px",
                  textAlign: "center",
                  fontSize: 12.5,
                  lineHeight: 1.6,
                  color: "var(--fg-muted)",
                }}
              >
                Open this link on a computer to import it.
                <br />
                <span style={{ color: "var(--fg-dim)", fontSize: 11.5 }}>
                  Workflows are built and imported in the desktop app.
                </span>
              </div>
            )}
          </>
        )}
      </main>
    </div>
  );
}
