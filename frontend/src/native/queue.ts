// The offline queue for location fixes.
//
// A geofence crossing happens exactly where signal tends to be worst: a
// basement car park, a tunnel, a trail. Fire-and-forget would silently drop
// the one event the whole feature exists to deliver, so every fix is persisted
// before it is sent and only removed once the server has accepted it.
//
// The server is the other half of this: it orders fixes by the DEVICE's
// timestamp and ignores any older than the last one it acted on, so flushing a
// backlog cannot re-fire a crossing that was already handled. That is why
// recordedAt is captured here, at observation time, and never at send time.
import { Preferences } from "@capacitor/preferences";

const QUEUE_KEY = "agentmesh.geofence.queue";

// GeofenceReceiver.java writes here, never to QUEUE_KEY above. It runs with no
// WebView and cannot call into this module, so a crossing landing while the
// app is dead has to go through SharedPreferences instead -- and if it used
// QUEUE_KEY, its read-modify-write could race the one below and silently
// clobber whichever side wrote second. A separate key sidesteps needing a
// cross-process lock: Java only ever appends here, and drainNative() below is
// written to be safe against that append landing mid-drain.
const NATIVE_QUEUE_KEY = "agentmesh.geofence.native_queue";

// Bounded so a phone that is offline for a week cannot grow this without
// limit. The oldest fixes are the least interesting -- the server ignores
// anything older than the last crossing it handled anyway -- so the cap drops
// from the front.
const MAX_QUEUED = 200;

// Past this, a fix describes a journey nobody is waiting on any more. Sending
// it would at best be a no-op and at worst fire a workflow about somewhere the
// user was days ago.
const MAX_AGE_MS = 24 * 60 * 60 * 1000;

export interface Fix {
  workflowId: string;
  lat: number;
  lng: number;
  accuracyM?: number;
  /** ISO 8601, captured when the OS reported the fix, never when it is sent. */
  recordedAt: string;
}

// A corrupt queue must not brick the trigger. Losing the backlog is
// recoverable; refusing to record anything ever again is not.
function parseFixes(value: string | null): Fix[] {
  if (!value) return [];
  try {
    const parsed: unknown = JSON.parse(value);
    return Array.isArray(parsed) ? (parsed as Fix[]) : [];
  } catch {
    return [];
  }
}

async function read(): Promise<Fix[]> {
  const { value } = await Preferences.get({ key: QUEUE_KEY });
  return parseFixes(value);
}

async function write(fixes: Fix[]): Promise<void> {
  await Preferences.set({ key: QUEUE_KEY, value: JSON.stringify(fixes) });
}

export async function enqueue(fix: Fix): Promise<void> {
  const all = [...(await read()), fix];
  await write(all.slice(-MAX_QUEUED));
}

// Migrates whatever GeofenceReceiver.java queued while the app was dead into
// the main queue above, then re-reads NATIVE_QUEUE_KEY and writes back only
// the tail that arrived after the snapshot was taken -- not an empty array.
// Java only ever appends (JSONArray.put), so the live value is always the
// snapshot plus zero or more new entries at the end; truncating to that tail
// instead of clearing outright means a crossing delivered in the exact window
// between the snapshot read and this write is kept, not lost.
async function drainNative(): Promise<void> {
  const { value } = await Preferences.get({ key: NATIVE_QUEUE_KEY });
  const snapshot = parseFixes(value);
  if (snapshot.length === 0) return;

  const all = [...(await read()), ...snapshot];
  await write(all.slice(-MAX_QUEUED));

  const { value: current } = await Preferences.get({ key: NATIVE_QUEUE_KEY });
  const remaining = parseFixes(current).slice(snapshot.length);
  if (remaining.length === 0) {
    await Preferences.remove({ key: NATIVE_QUEUE_KEY });
  } else {
    await Preferences.set({
      key: NATIVE_QUEUE_KEY,
      value: JSON.stringify(remaining),
    });
  }
}

export async function pending(now = Date.now()): Promise<Fix[]> {
  await drainNative();
  const fresh = (await read()).filter(
    (f) => now - Date.parse(f.recordedAt) < MAX_AGE_MS,
  );
  return fresh.sort(
    (a, b) => Date.parse(a.recordedAt) - Date.parse(b.recordedAt),
  );
}

// Removes exactly the fixes that were accepted, matched on their timestamp and
// workflow. Anything enqueued while a flush was in flight survives.
export async function remove(sent: Fix[]): Promise<void> {
  const gone = new Set(sent.map((f) => `${f.workflowId}@${f.recordedAt}`));
  await write(
    (await read()).filter((f) => !gone.has(`${f.workflowId}@${f.recordedAt}`)),
  );
}

export async function clear(): Promise<void> {
  await Preferences.remove({ key: QUEUE_KEY });
}
