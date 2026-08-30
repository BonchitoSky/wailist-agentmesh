// Session storage for the native shell.
//
// The web app authenticates with an HttpOnly cookie and this module is inert
// there -- deliberately, because a token page JavaScript can read is strictly
// weaker than a cookie it cannot. But a Capacitor WebView runs on the
// https://localhost app origin, so the cookie the API sets on its own domain
// is third-party and Android declines to send it. The native build therefore
// uses the bearer path the API has always supported for non-browser clients.
//
// The token lives in memory here and is persisted by the shell (mobile/) into
// Keystore-backed storage, not localStorage: a WebView's localStorage is
// readable by anything that achieves script execution inside it, and survives
// on disk unencrypted.

export const IS_NATIVE = process.env.NEXT_PUBLIC_NATIVE_CLIENT === "1";

let token: string | null = null;

export function setAuthToken(next: string | null): void {
  token = next;
}

export function getAuthToken(): string | null {
  return token;
}

// The header the API reads to decide whether to hand back a bearer token at
// sign-in. Mirrors nativeClientHeader in backend/internal/api/handlers/auth.go.
export const CLIENT_HEADER = "X-AgentMesh-Client";

// authHeaders returns what this client must add to every API call.
//
// Empty on the web, so the browser keeps sending nothing but its cookie and
// the request is byte-identical to what it was before any of this existed.
export function authHeaders(): Record<string, string> {
  if (!IS_NATIVE) return {};
  const h: Record<string, string> = { [CLIENT_HEADER]: "android" };
  if (token) h.Authorization = `Bearer ${token}`;
  return h;
}
