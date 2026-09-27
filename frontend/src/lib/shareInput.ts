// Working out what somebody just pasted into Import.
//
// Three things arrive through one box, because asking a person to classify
// their own clipboard before pasting it is the kind of friction that makes a
// feature go unused:
//
//   a full link   https://www.agent-mesh.app/s/k3Jm9x...
//   a bare token  k3Jm9x...
//   a code        am1.H4sIAAAA... (or a legacy bare-base64 one)
//
// Kept out of the modal so the rules are testable without a DOM, and so the
// awkward case below is written down once rather than re-derived.

export type ShareInput =
  { kind: "token"; token: string } | { kind: "code"; code: string };

// A token is what randURLSafe(16) produces: 22 base64url characters. The range
// is wider than that on purpose -- the backend's CHECK allows 16 to 64, and a
// client hard-coding 22 would start rejecting valid links the day that
// changes.
const TOKEN_SHAPE = /^[A-Za-z0-9_-]{16,64}$/;

const CODE_PREFIX = "am1.";

export function classifyShareInput(raw: string): ShareInput | null {
  const text = raw.trim();
  if (!text) return null;

  // A link, however it was pasted -- with or without a scheme, from any
  // origin, and with whatever query or fragment a chat client stapled on.
  // The path is the only part that matters.
  const fromUrl = tokenFromUrl(text);
  if (fromUrl) return { kind: "token", token: fromUrl };

  // A code is self-identifying since am1., so it never has to be guessed at.
  if (text.startsWith(CODE_PREFIX)) return { kind: "code", code: text };

  // The one genuinely ambiguous case: a bare string of base64url characters
  // is shaped like a token AND like a short legacy code. Tokens are short and
  // legacy codes are not -- a gzipped one-node graph is already a couple of
  // hundred characters -- so length is the tie-breaker, and the caller falls
  // back to trying it as a code if the lookup comes back empty. Guessing
  // wrong costs one failed request; refusing to guess costs the paste.
  if (TOKEN_SHAPE.test(text)) return { kind: "token", token: text };

  return { kind: "code", code: text };
}

// tokenFromUrl pulls the token out of any URL whose path ends /s/<token>.
//
// Deliberately origin-agnostic: a link copied from a preview deployment, from
// staging or from localhost is still that person's link, and refusing it
// because the host is not production would be pointless -- the token is
// looked up against THIS backend regardless of what host the string names.
function tokenFromUrl(text: string): string | null {
  if (!text.includes("/s/")) return null;
  for (const candidate of [text, `https://${text}`]) {
    try {
      const url = new URL(candidate, "https://agent-mesh.app");
      const match = /\/s\/([^/?#]+)\/?$/.exec(url.pathname);
      if (match) return decodeURIComponent(match[1]);
    } catch {
      // Not parseable as a URL under this candidate; try the next.
    }
  }
  return null;
}
