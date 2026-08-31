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

async function read(): Promise<Fix[]> {
  const { value } = await Preferences.get({ key: QUEUE_KEY });
  if (!value) return [];
  try {
    const parsed: unknown = JSON.parse(value);
    return Array.isArray(parsed) ? (parsed as Fix[]) : [];
  } catch {
    // A corrupt queue must not brick the trigger. Losing the backlog is
    // recoverable; refusing to record anything ever again is not.
    return [];
  }
}

async function write(fixes: Fix[]): Promise<void> {
  await Preferences.set({ key: QUEUE_KEY, value: JSON.stringify(fixes) });
}

export async function enqueue(fix: Fix): Promise<void> {
  const all = [...(await read()), fix];
  await write(all.slice(-MAX_QUEUED));
}

export async function pending(now = Date.now()): Promise<Fix[]> {
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
