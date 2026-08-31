// The background geofence client.
//
// Android's own GeofencingClient watches the boundary and wakes the app on a
// crossing. We never poll GPS: polling is why location apps get uninstalled,
// and the platform batches geofence work across every app on the device.
//
// Note where the work actually happens. When the app is alive, the flush below
// sends queued fixes. When it is NOT -- the common case for a real crossing --
// the native receiver (GeofenceReceiver.java) appends straight to the same
// queue, in the same storage, and this code drains it on next launch without
// knowing or caring that native wrote it.
import { Network } from "@capacitor/network";
import { Geofence } from "./nativeGeofence";
import { pushFix, Unauthorized } from "./api";
import { pending, remove, type Fix } from "./queue";

let flushing = false;

// Sends whatever is queued, oldest first, stopping at the first failure.
//
// Stopping rather than skipping is deliberate: fixes are ordered, and the
// server uses that order to decide what is a crossing. Pushing a newer fix
// past one that failed would advance the server's idea of "last seen" and make
// the skipped one permanently stale.
export async function flush(): Promise<void> {
  if (flushing) return;
  flushing = true;
  try {
    const sent: Fix[] = [];
    for (const fix of await pending()) {
      try {
        await pushFix(fix);
        sent.push(fix);
      } catch (err) {
        // Unauthorized means nothing will succeed until the user signs in
        // again; everything else is worth retrying later. Either way the
        // queue is kept -- the crossings happened, and they still matter.
        if (err instanceof Unauthorized) break;
        break;
      }
    }
    if (sent.length) await remove(sent);
  } finally {
    flushing = false;
  }
}

export interface StartOptions {
  workflowId: string;
  lat: number;
  lng: number;
  radiusM: number;
}

/**
 * Registers the zone with the OS.
 *
 * Returns false when background location has not been granted. That is a
 * normal outcome rather than an error: the app is a perfectly good
 * viewer/controller without it, and permissions.ts owns explaining the trade
 * before Android's dialog appears.
 */
export async function start(opts: StartOptions): Promise<boolean> {
  const { granted } = await Geofence.hasPermission();
  if (!granted) return false;

  await Geofence.addGeofence({
    id: opts.workflowId,
    lat: opts.lat,
    lng: opts.lng,
    radiusM: opts.radiusM,
  });

  // The other half of the offline queue: without this a backlog sits there
  // until the next crossing happens to occur.
  await Network.addListener("networkStatusChange", (status) => {
    if (status.connected) void flush();
  });

  // Anything the native receiver queued while the app was closed.
  await flush();
  return true;
}

export async function stop(workflowId: string): Promise<void> {
  await Geofence.removeGeofence({ id: workflowId });
}
