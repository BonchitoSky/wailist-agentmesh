// The shell's own backend client.
//
// Separate from the web bundle's lib/api.ts on purpose: this code runs in the
// background, outside any page, when the OS wakes us for a geofence crossing.
// There may be no WebView document alive at all at that moment, so it cannot
// rely on anything the page set up.
import { clearToken, loadToken } from "./auth";
import { isWriteBlocked, isReadOnlyNow } from "../lib/readonly";
import type { Fix } from "./queue";

// Same defence-in-depth as lib/api.ts's assertWritable, reimplemented rather
// than imported: this module is deliberately kept independent of lib/api.ts
// (see the file header), and the two write calls below are the only ones
// this file makes, so a private inline check is simpler than exporting
// lib/api.ts's version for one other caller.
function assertWritable(method: string, path: string): void {
  if (isWriteBlocked(method, path, isReadOnlyNow())) {
    throw new Error("Workflows can only be edited in the AgentMesh desktop app.");
  }
}

// Baked in at build time, same value the web bundle is given. There is no
// Next server in front of the shell, so this is the backend's absolute URL.
const API_BASE = process.env.NEXT_PUBLIC_API_URL ?? "";

export class Unauthorized extends Error {}

async function call(path: string, init: RequestInit = {}): Promise<Response> {
  const token = await loadToken();
  const res = await fetch(`${API_BASE}${path}`, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      "X-AgentMesh-Client": "android",
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(init.headers as Record<string, string> | undefined),
    },
  });
  if (res.status === 401) {
    // Drop the dead token rather than retrying with it forever.
    await clearToken();
    throw new Unauthorized("session expired");
  }
  return res;
}

export interface PingResult {
  inside: boolean;
  triggered: boolean;
  direction?: "enter" | "leave";
  runId?: string;
  stale?: boolean;
}

// Pushes one fix. Throws on anything the client should retry later; returns
// normally for every answer the server considers final, INCLUDING "that was
// not an event" and "that was a replay" -- both mean the fix is handled and
// must come off the queue.
export async function pushFix(fix: Fix): Promise<PingResult> {
  const res = await call(`/workflows/${fix.workflowId}/trigger/location`, {
    method: "POST",
    body: JSON.stringify({
      lat: fix.lat,
      lng: fix.lng,
      accuracyM: fix.accuracyM,
      recordedAt: fix.recordedAt,
    }),
  });

  // 429 is the server asking us to slow down, not to give up: keep the fix.
  if (res.status === 429) throw new Error("rate limited");
  // 5xx is ours to retry. 4xx other than 429 is not -- a malformed or
  // unconfigured fix will fail identically forever, and keeping it would
  // block the queue behind it.
  if (res.status >= 500) throw new Error(`server error ${res.status}`);
  if (!res.ok) return { inside: false, triggered: false };

  return (await res.json()) as PingResult;
}

export interface Geofence {
  lat: number;
  lng: number;
  radiusM: number;
}

export async function setGeofence(
  workflowId: string,
  fence: Geofence,
): Promise<void> {
  assertWritable("PUT", `/workflows/${workflowId}/geofence`);
  const res = await call(`/workflows/${workflowId}/geofence`, {
    method: "PUT",
    body: JSON.stringify(fence),
  });
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(body.error ?? "could not save the geofence");
  }
}

export async function clearGeofence(workflowId: string): Promise<void> {
  assertWritable("DELETE", `/workflows/${workflowId}/geofence`);
  await call(`/workflows/${workflowId}/geofence`, { method: "DELETE" });
}
