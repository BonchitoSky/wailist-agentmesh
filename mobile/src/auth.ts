// Session persistence for the shell.
//
// The token comes from the backend's sign-in response, which hands one out
// only to a client identifying itself via X-AgentMesh-Client (see
// backend/internal/api/handlers/auth.go). The web app never gets one and
// keeps using its HttpOnly cookie.
//
// Stored through @capacitor/preferences, which on Android is backed by
// EncryptedSharedPreferences -- deliberately NOT the WebView's localStorage,
// which sits unencrypted on disk and is readable by anything that achieves
// script execution inside the WebView.
import { Preferences } from "@capacitor/preferences";

const TOKEN_KEY = "agentmesh.session.token";

export async function loadToken(): Promise<string | null> {
  const { value } = await Preferences.get({ key: TOKEN_KEY });
  return value ?? null;
}

export async function saveToken(token: string): Promise<void> {
  await Preferences.set({ key: TOKEN_KEY, value: token });
}

// Called on sign-out and on any 401. Clearing on 401 matters: a token that
// has expired or been revoked otherwise sits there failing every request
// forever, with the app unable to explain why.
export async function clearToken(): Promise<void> {
  await Preferences.remove({ key: TOKEN_KEY });
}
