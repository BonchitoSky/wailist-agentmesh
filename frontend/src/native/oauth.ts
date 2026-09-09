import type { PluginListenerHandle } from "@capacitor/core";
import { auth } from "@/lib/api";

// OAuth sign-in from inside the app.
//
// The buttons on the sign-in screen used to do `window.location.href = <api
// origin>/auth/oauth/google`, which navigates the WebView off its own
// https://localhost origin. The native CSP's `default-src 'self'` blocks that;
// when it does not, the app bundle is gone and there is no way back but killing
// the app. Either way no session arrives, because the backend finishes by
// setting a cookie the WebView will not accept.
//
// So the provider is opened OUTSIDE the WebView and the answer comes back as an
// Android intent:
//
//   start()  -> Custom Tab at <api>/auth/oauth/<provider>?client=android&challenge=
//               ... provider, consent, backend callback ...
//   intent   -> ai.agentmesh.app://auth?code=<one-time>
//   handle() -> POST /auth/oauth/exchange {code, verifier} -> session token
//
// Outside the WebView is not a preference. Google refuses OAuth in an embedded
// WebView outright (`disallowed_useragent`), so a Custom Tab or the system
// browser is the only thing that works -- and a Custom Tab is the better of the
// two, because it shares Chrome's cookie jar and the user is usually signed in
// there already.

// The scheme is the app's own applicationId. It must match, exactly, three
// other places: capacitor.config.ts's appId, custom_url_scheme in
// mobile/android/app/src/main/res/values/strings.xml, and nativeAppScheme in
// backend/internal/api/handlers/oauth_native.go. A mismatch fails silently --
// Android simply does not match the intent-filter, the Custom Tab sits open,
// and nothing anywhere says why.
const APP_SCHEME = "ai.agentmesh.app";
const CALLBACK_PREFIX = `${APP_SCHEME}://auth`;

// Where the verifier waits between the two halves of the flow.
//
// sessionStorage, not a module variable: the Custom Tab is a separate task and
// Android is free to kill the app behind it on a low-memory device. A verifier
// held in memory would be gone exactly when the user came back, and the flow
// would fail at the last step having already asked them for consent. Not the
// secure store either -- this is a value with a 60-second life that is
// worthless once used, and the secure store is an async native call on the path
// where the app is being resumed.
const VERIFIER_KEY = "agentmesh.oauth.verifier";

// 32 bytes, hex. Long enough that guessing it is not a strategy, and printable
// so it survives a URL and sessionStorage with no encoding questions.
function newVerifier(): string {
  const bytes = new Uint8Array(32);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

// PKCE S256: base64url, unpadded. The same transform the backend applies to the
// verifier when it checks, so the two must not drift.
async function challengeOf(verifier: string): Promise<string> {
  const digest = await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(verifier),
  );
  return btoa(String.fromCharCode(...new Uint8Array(digest)))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

// start opens the provider's consent screen in a Custom Tab.
//
// Throws when the app has no backend configured, because there is nothing
// useful to open and a Custom Tab showing an error page is worse than a
// sentence on the sign-in screen.
export async function start(provider: "github" | "google"): Promise<void> {
  const base = auth.oauthURL(provider);
  if (!base) throw new Error("Social sign in is not configured.");

  const verifier = newVerifier();
  sessionStorage.setItem(VERIFIER_KEY, verifier);

  const url = `${base}?client=android&challenge=${encodeURIComponent(
    await challengeOf(verifier),
  )}`;

  const { Browser } = await import("@capacitor/browser");
  await Browser.open({ url });
}

// The outcome of one callback, so the caller decides what to show. A thrown
// error would be indistinguishable from a bug in the listener, and this runs
// where nobody is awaiting a promise.
export type OAuthResult =
  { ok: true; token: string } | { ok: false; reason: string };

// handleCallbackUrl turns a deep link into a session, or into a reason.
//
// Exported, and takes the URL rather than reading an event, so every decision
// in it can be tested without a Capacitor bridge.
export async function handleCallbackUrl(url: string): Promise<OAuthResult> {
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return { ok: false, reason: "bad_callback" };
  }

  const error = parsed.searchParams.get("error");
  if (error) return { ok: false, reason: error };

  const code = parsed.searchParams.get("code");
  if (!code) return { ok: false, reason: "no_code" };

  // Read and clear together. A verifier left behind would be reused by the next
  // attempt, and a second attempt is exactly when the first one went wrong.
  const verifier = sessionStorage.getItem(VERIFIER_KEY);
  sessionStorage.removeItem(VERIFIER_KEY);
  if (!verifier) return { ok: false, reason: "no_verifier" };

  try {
    const token = await auth.oauthExchange(code, verifier);
    return token
      ? { ok: true, token }
      : { ok: false, reason: "exchange_failed" };
  } catch {
    return { ok: false, reason: "exchange_failed" };
  }
}

let urlListener: PluginListenerHandle | null = null;

// listenForCallback attaches the deep-link handler.
//
// Idempotent for the same reason listenForTaps is: a second attachment would
// run one callback twice, and the second run finds the verifier already cleared
// and reports a failure over a sign-in that actually worked.
//
// It belongs in boot() rather than on the sign-in screen. The intent can arrive
// at a cold start -- Android may have killed the app while the Custom Tab was
// in front -- and a listener registered when some component mounts has already
// missed it.
export async function listenForCallback(
  onResult: (result: OAuthResult) => void | Promise<void>,
): Promise<void> {
  if (urlListener) return;

  const { App } = await import("@capacitor/app");
  urlListener = await App.addListener("appUrlOpen", (event) => {
    if (!event.url?.startsWith(CALLBACK_PREFIX)) return;
    void (async () => {
      const result = await handleCallbackUrl(event.url);
      // Close the Custom Tab whichever way it went. Left open, the user returns
      // to the app and finds the consent screen still sitting behind it.
      try {
        const { Browser } = await import("@capacitor/browser");
        await Browser.close();
      } catch {
        // Nothing to close, or no plugin. Not a reason to drop the result.
      }
      await onResult(result);
    })();
  });
}
