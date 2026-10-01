"use client";
import { useEffect, useRef, useState } from "react";
import { IconClose } from "@/components/ui";
import { useModalDismissal } from "@/hooks/useModalDismissal";
import { shares as sharesApi } from "@/lib/api";
import { shareUrl } from "@/lib/routes";
import type { ShareRedactions, WorkflowShare } from "@/lib/types";
import { encodeWorkflowShare } from "@/lib/workflowShare";

// Handing a workflow to somebody else.
//
// A link first, a code second. The previous version of this dialog produced
// only a code, and its own X and Email buttons then refused to carry it --
// they sent a caption and told the recipient to ask for the clipboard
// separately, because a code is routinely longer than a mailto: survives. A
// token is 22 characters, so the link goes wherever text goes.
//
// The code is still here for a channel that mangles URLs, and is built from
// the graph the SERVER returned rather than from the workflow's own nodes.
// One sanitiser, on the server; the client is never trusted to reproduce it.

const EXPIRY_CHOICES = [
  { label: "No expiry", days: 0 },
  { label: "7 days", days: 7 },
  { label: "30 days", days: 30 },
] as const;

const IconCopy = ({ size = 14 }: { size?: number }) => (
  <svg width={size} height={size} viewBox="0 0 16 16" fill="none">
    <rect
      x="5.5"
      y="5.5"
      width="8"
      height="8"
      rx="1.5"
      stroke="currentColor"
      strokeWidth="1.3"
    />
    <path
      d="M2.5 10.5v-7A1 1 0 0 1 3.5 2.5h7"
      stroke="currentColor"
      strokeWidth="1.3"
      strokeLinecap="round"
    />
  </svg>
);

const IconX = ({ size = 14 }: { size?: number }) => (
  <svg width={size} height={size} viewBox="0 0 16 16" fill="currentColor">
    <path d="M9.3 6.9 14 2h-1.6L8.6 5.9 5.4 2H1l5 6.7L1 14h1.6l4-4.3L9.9 14H14L9.3 6.9Zm-1.4 1.6-.5-.6L3.2 3h1.4l3 4 .5.6 3.9 5.3H10.6l-2.7-3.4Z" />
  </svg>
);

const IconMail = ({ size = 14 }: { size?: number }) => (
  <svg width={size} height={size} viewBox="0 0 16 16" fill="none">
    <rect
      x="1.5"
      y="3.5"
      width="13"
      height="9"
      rx="1.5"
      stroke="currentColor"
      strokeWidth="1.3"
    />
    <path
      d="M2 4.5 8 9l6-4.5"
      stroke="currentColor"
      strokeWidth="1.3"
      strokeLinecap="round"
      strokeLinejoin="round"
    />
  </svg>
);

const isLive = (s: WorkflowShare) =>
  !s.revokedAt &&
  (!s.expiresAt || new Date(s.expiresAt).getTime() > Date.now());

// What a link's life says about it, in the words the sharer thinks in.
//
// A revoked or expired link stays on screen rather than disappearing. The
// listing returns them deliberately, and "I revoked that one" is the answer
// to "why did my friend say the link was dead" -- which a row that quietly
// vanished cannot give.
function lifeOf(s: WorkflowShare): { label: string; dead: boolean } {
  if (s.revokedAt) return { label: "Revoked", dead: true };
  if (!s.expiresAt) return { label: "Never expires", dead: false };
  const ms = new Date(s.expiresAt).getTime() - Date.now();
  if (Number.isNaN(ms)) return { label: "Never expires", dead: false };
  if (ms <= 0) return { label: "Expired", dead: true };
  const days = Math.ceil(ms / 86_400_000);
  if (days === 1) return { label: "Expires tomorrow", dead: false };
  return { label: `Expires in ${days} days`, dead: false };
}

function importsOf(s: WorkflowShare): string {
  const n = s.importCount ?? 0;
  if (n === 0) return "no imports yet";
  return n === 1 ? "1 import" : `${n} imports`;
}

// Sentences, not a count table. "your API key" is what a person needs to hear;
// "apiKeys: 1" is what the API happens to return.
function redactionLines(r: ShareRedactions): string[] {
  const lines: string[] = [];
  const plural = (n: number, one: string, many: string) =>
    n === 1 ? one : `${n} ${many}`;
  if (r.apiKeys > 0) lines.push(plural(r.apiKeys, "your API key", "API keys"));
  if (r.secrets > 0)
    lines.push(plural(r.secrets, "a connector secret", "connector secrets"));
  if (r.webhookSecrets > 0) lines.push("your webhook secret");
  if (r.connectedAccounts > 0) lines.push("your connected accounts");
  if (r.uploadedFiles > 0)
    lines.push(plural(r.uploadedFiles, "an uploaded file", "uploaded files"));
  if (r.agentWallets > 0) lines.push("your agent wallet addresses");
  if (r.emailAddresses > 0) lines.push("the addresses it sends email to");
  if (r.leasedMachines > 0) lines.push("your leased machine");
  return lines;
}

// Mounted only while open (the parent renders it conditionally on
// shareWorkflowId, the same pattern AddToWorkflowDialog uses) -- so a fresh
// mount per share is the reset, and no effect has to clear anything.
export function ShareModal({
  workflowId,
  onClose,
}: {
  workflowId: string;
  onClose: () => void;
}) {
  const [share, setShare] = useState<WorkflowShare | null>(null);
  const [redactions, setRedactions] = useState<ShareRedactions | null>(null);
  const [others, setOthers] = useState<WorkflowShare[]>([]);
  const [expiryDays, setExpiryDays] = useState<number>(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState<"link" | "code" | null>(null);
  // The token whose Revoke button is currently asking "Revoke?". One at a
  // time: arming a second row disarms the first, which is what a person
  // expects and saves a per-row state.
  const [confirming, setConfirming] = useState<string | null>(null);
  const loading = !share && !error;

  const dialogRef = useModalDismissal(onClose);

  // One call, and the server decides whether this is a new link or one the
  // workflow already has. Opening the dialog four times should not leave four
  // links behind for somebody to wonder about later.
  //
  // This was list-then-maybe-reuse, decided here, and it was wrong twice over.
  // It reused the newest live link without asking whether the workflow had
  // changed since, so editing and reopening handed back a link to the
  // PREVIOUS version with nothing on screen saying so. And that branch had no
  // redaction counts to set, so the "not included ..." summary disappeared at
  // exactly the moment somebody was about to hand the link over.
  //
  // Both follow from the same mistake: only the server holds the sanitised
  // graph, so only the server can tell whether a link still describes this
  // workflow. Asked properly, it answers with the counts either way.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const created = await sharesApi.create(workflowId, 0, true);
        if (cancelled) return;
        setShare(created.share);
        setRedactions(created.redactions);
        // The listing feeds the revoke list below and nothing else, so it is
        // fetched after, with the link just obtained filtered out of it.
        const existing = await sharesApi.listFor(workflowId);
        if (cancelled) return;
        setOthers(existing.filter((s) => s.token !== created.share.token));
      } catch (e) {
        if (!cancelled) {
          setError(
            e instanceof Error ? e.message : "could not prepare a share link",
          );
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [workflowId]);

  // Cleared on unmount. The dialog can be closed inside the 1.5s window --
  // copying a link and immediately pressing Escape is the normal way to use
  // this -- and the timer would then fire against a component that is gone.
  const flashTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (flashTimer.current !== null) clearTimeout(flashTimer.current);
    },
    [],
  );

  const flash = (what: "link" | "code") => {
    setCopied(what);
    if (flashTimer.current !== null) clearTimeout(flashTimer.current);
    flashTimer.current = setTimeout(() => setCopied(null), 1500);
  };

  const handleCopyLink = async () => {
    if (!share) return;
    try {
      await navigator.clipboard.writeText(shareUrl(share.token));
      flash("link");
    } catch {
      setError("clipboard write was blocked -- select the link and copy it");
    }
  };

  // The graph is fetched here rather than kept around: the listing deliberately
  // does not carry graphs, and most people copy the link and never touch this.
  const handleCopyCode = async () => {
    if (!share || busy) return;
    setBusy(true);
    setError(null);
    try {
      const { share: full } = await sharesApi.read(share.token);
      const code = await encodeWorkflowShare({
        name: full.name,
        description: full.description,
        nodes: full.graph.nodes,
        edges: full.graph.edges,
      });
      await navigator.clipboard.writeText(code);
      flash("code");
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not prepare a code");
    } finally {
      setBusy(false);
    }
  };

  const handleNewLink = async () => {
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      const created = await sharesApi.create(workflowId, expiryDays);
      setOthers((prev) => (share ? [share, ...prev] : prev));
      setShare(created.share);
      setRedactions(created.redactions);
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not create a link");
    } finally {
      setBusy(false);
    }
  };

  // Revoking asks first, inline, by turning the button into "Revoke?".
  //
  // It is instant and irreversible, and the link may already be in somebody
  // else's inbox -- so a stray click on a 26px button should not be the whole
  // interaction. Inline rather than a nested confirm dialog: a second modal
  // over this one is heavier than the decision warrants, and the row itself
  // is the thing being talked about.
  const handleRevoke = async (token: string) => {
    if (confirming !== token) {
      setConfirming(token);
      return;
    }
    setConfirming(null);
    setError(null);
    try {
      await sharesApi.revoke(token);
      // Marked revoked in place rather than dropped, so the sharer can see
      // what they just did. lifeOf() greys it and the Revoke button goes.
      const stamp = new Date().toISOString();
      setOthers((prev) =>
        prev.map((s) => (s.token === token ? { ...s, revokedAt: stamp } : s)),
      );
      setShare((cur) =>
        cur && cur.token === token ? { ...cur, revokedAt: stamp } : cur,
      );
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not revoke that link");
    }
  };

  const url = share ? shareUrl(share.token) : "";
  const caption = share
    ? `Check out "${share.name}" -- a workflow I built with AgentMesh`
    : "";
  // Both carry the LINK now. The old version could not: a code is routinely
  // longer than a mailto: body or a tweet survives, so it sent a caption and
  // an apology instead.
  const tweetUrl = `https://twitter.com/intent/tweet?text=${encodeURIComponent(caption)}&url=${encodeURIComponent(url)}`;
  const mailUrl = `mailto:?subject=${encodeURIComponent(caption)}&body=${encodeURIComponent(`${caption}\n\n${url}`)}`;

  const lines = redactions ? redactionLines(redactions) : [];
  const currentLife = share
    ? lifeOf(share)
    : { label: "", dead: false as boolean };
  // Every other link, dead ones included. They used to be filtered out, which
  // threw away the only record of what had been handed out and then pulled
  // back -- and the backend returns them precisely so this view can show them.
  const otherLinks = others;

  return (
    <div
      role="presentation"
      className="share-scrim"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-label="Share workflow"
        className="share-panel"
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
              Share workflow
            </h2>
            <p
              style={{
                margin: "3px 0 0",
                fontSize: 12.5,
                color: "var(--fg-muted)",
              }}
            >
              {share?.name || "Loading…"}
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

        {/* "Copied!" is a label swap on a button, which a screen reader does
            not announce -- so the one piece of feedback the whole dialog
            gives was silent. This says it out loud without showing twice. */}
        <span
          aria-live="polite"
          style={{
            position: "absolute",
            width: 1,
            height: 1,
            overflow: "hidden",
            clip: "rect(0 0 0 0)",
            whiteSpace: "nowrap",
          }}
        >
          {copied === "link"
            ? "Link copied to clipboard"
            : copied === "code"
              ? "Code copied to clipboard"
              : ""}
        </span>

        {loading && (
          <div
            style={{
              // Roughly what the loaded state occupies, so the dialog does
              // not snap to a new height the moment the link arrives.
              minHeight: 220,
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
              fontSize: 12.5,
              color: "var(--fg-dim)",
            }}
          >
            Preparing a link…
          </div>
        )}

        {error && (
          <div
            style={{
              fontSize: 12.5,
              color: "var(--danger)",
              padding: "8px 0",
              maxWidth: "60ch",
            }}
          >
            {error}
          </div>
        )}

        {share && (
          <>
            <div
              style={{
                fontSize: 11.5,
                color: "var(--fg-dim)",
                marginBottom: 6,
              }}
            >
              Anyone with this link can see the workflow and import a copy.
            </div>
            <input
              readOnly
              value={url}
              onFocus={(e) => e.currentTarget.select()}
              aria-label="Share link"
              style={{
                width: "100%",
                height: 38,
                fontFamily: "var(--font-mono)",
                fontSize: 11.5,
                padding: "0 10px",
                background: "var(--bg-elev-2)",
                border: "1px solid var(--border)",
                borderRadius: "var(--r-2)",
                color: "var(--fg-muted)",
                marginBottom: 10,
              }}
            />
            {/* What this particular link's life looks like, and how much use
                it has had. Both were previously shown for every OTHER link
                but never for the one on screen -- so the most interesting
                question a sharer has ("has anyone actually used it?") was the
                one the dialog would not answer. */}
            <div
              style={{
                display: "flex",
                alignItems: "center",
                justifyContent: "space-between",
                gap: 8,
                marginBottom: 10,
                fontSize: 11.5,
                color: currentLife.dead ? "var(--warm)" : "var(--fg-dim)",
              }}
            >
              <span style={{ fontVariantNumeric: "tabular-nums" }}>
                {currentLife.label} · {importsOf(share)}
              </span>
              {isLive(share) && (
                <button
                  type="button"
                  onClick={() => void handleRevoke(share.token)}
                  className="share-revoke-btn"
                >
                  {confirming === share.token ? "Revoke?" : "Revoke"}
                </button>
              )}
            </div>

            <button
              type="button"
              onClick={handleCopyLink}
              disabled={currentLife.dead}
              className="share-primary-btn"
            >
              <IconCopy size={13} />{" "}
              {currentLife.dead
                ? "This link is dead"
                : copied === "link"
                  ? "Copied!"
                  : "Copy link"}
            </button>

            <div style={{ display: "flex", gap: 8, margin: "10px 0 14px" }}>
              <a
                href={tweetUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="share-ghost-btn"
              >
                <IconX size={13} /> Post
              </a>
              <a href={mailUrl} className="share-ghost-btn">
                <IconMail size={13} /> Email
              </a>
              <button
                type="button"
                onClick={handleCopyCode}
                disabled={busy || currentLife.dead}
                className="share-ghost-btn"
                title="A code works without a link, but cannot be revoked"
              >
                <IconCopy size={13} /> {copied === "code" ? "Copied!" : "Code"}
              </button>
            </div>

            {/* Said out loud, because the rest of this dialog implies the
                opposite. Everything else here can be pulled back -- there is
                a Revoke on every link and an expiry select -- and a code sits
                among them looking like one more way to send the same thing.
                It is not: a code is the graph itself, so once it is out there
                is nothing left to revoke. */}
            {copied === "code" && (
              <div
                style={{
                  margin: "0 0 12px",
                  fontSize: 11.5,
                  lineHeight: 1.6,
                  color: "var(--warm)",
                  maxWidth: "60ch",
                }}
              >
                A code carries the workflow itself, so it keeps working even
                after you revoke the link. Send a link if you might change your
                mind.
              </div>
            )}

            <div
              style={{
                border: "1px solid var(--border-soft)",
                borderRadius: "var(--r-2)",
                background: "var(--bg-elev-2)",
                padding: "11px 13px",
                marginBottom: 14,
                fontSize: 12,
                lineHeight: 1.65,
                color: "var(--fg-muted)",
                maxWidth: "60ch",
              }}
            >
              <strong style={{ color: "var(--fg)", fontWeight: 600 }}>
                Not included:
              </strong>{" "}
              {lines.length > 0
                ? `${lines.join(", ")}. Whoever imports it adds their own.`
                : "any API keys, secrets or uploaded files. Whoever imports it adds their own."}
            </div>

            <div
              style={{
                height: 1,
                background: "var(--border)",
                margin: "0 0 14px",
              }}
            />

            <div
              style={{
                display: "flex",
                alignItems: "center",
                gap: 8,
                marginBottom: otherLinks.length > 0 ? 12 : 0,
              }}
            >
              {/* Worded so it cannot be read as applying to the link above.
                  It sets the expiry of the link the button beside it MAKES --
                  labelled "New link expires", people reasonably read it as
                  putting an expiry on the one they had just copied. */}
              <label
                htmlFor="share-expiry"
                style={{ fontSize: 11.5, color: "var(--fg-dim)" }}
              >
                Make another, expiring in
              </label>
              <select
                id="share-expiry"
                value={expiryDays}
                onChange={(e) => setExpiryDays(Number(e.target.value))}
                style={{
                  height: 30,
                  padding: "0 8px",
                  background: "var(--bg-elev-2)",
                  border: "1px solid var(--border)",
                  borderRadius: "var(--r-2)",
                  color: "var(--fg)",
                  fontSize: 12,
                }}
              >
                {EXPIRY_CHOICES.map((c) => (
                  <option key={c.days} value={c.days}>
                    {c.label}
                  </option>
                ))}
              </select>
              <button
                type="button"
                onClick={handleNewLink}
                disabled={busy}
                className="share-ghost-btn"
                style={{ flex: "0 0 auto", padding: "0 12px" }}
              >
                Make it
              </button>
            </div>

            {otherLinks.length > 0 && (
              <div>
                <div
                  style={{
                    fontSize: 11.5,
                    color: "var(--fg-dim)",
                    marginBottom: 6,
                  }}
                >
                  Other links to this workflow
                </div>
                {otherLinks.map((s) => (
                  <div
                    key={s.token}
                    style={{
                      display: "flex",
                      alignItems: "center",
                      justifyContent: "space-between",
                      gap: 8,
                      padding: "6px 0",
                      fontSize: 11.5,
                      color: "var(--fg-muted)",
                    }}
                  >
                    <span
                      style={{
                        fontFamily: "var(--font-mono)",
                        fontVariantNumeric: "tabular-nums",
                        overflow: "hidden",
                        textOverflow: "ellipsis",
                        whiteSpace: "nowrap",
                        // A dead link is kept, and shown as spent rather than
                        // as an option.
                        opacity: lifeOf(s).dead ? 0.55 : 1,
                        textDecoration: lifeOf(s).dead
                          ? "line-through"
                          : undefined,
                      }}
                    >
                      …{s.token.slice(-8)} · {importsOf(s)} · {lifeOf(s).label}
                    </span>
                    {isLive(s) && (
                      <button
                        type="button"
                        onClick={() => void handleRevoke(s.token)}
                        className="share-revoke-btn"
                      >
                        {confirming === s.token ? "Revoke?" : "Revoke"}
                      </button>
                    )}
                  </div>
                ))}
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}
