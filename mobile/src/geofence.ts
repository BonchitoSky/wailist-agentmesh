// The background geofence client.
//
// The OS watches the boundary and wakes the app on a crossing. We do not poll
// GPS: polling is why location apps get uninstalled, and Android's own
// geofence API is built to be cheap in exactly the way a loop is not.
//
// Every fix is queued before it is sent (queue.ts) and flushed when the
// network returns, because a crossing happens precisely where signal is worst.
// The server decides what is actually an EVENT -- it ignores repeats and
// replays -- so this side can afford to be simple and send everything it saw.
import BackgroundGeolocation from "@transistorsoft/capacitor-background-geolocation";
import { Network } from "@capacitor/network";
import { pushFix, Unauthorized } from "./api";
import { enqueue, pending, remove, type Fix } from "./queue";

let flushing = false;

// Sends whatever is queued, oldest first, stopping at the first failure.
//
// Stopping rather than skipping is deliberate: the fixes are ordered, and the
// server uses that order to decide what is a crossing. Pushing a newer fix
// past one that failed would advance the server's idea of "last seen" and make
// the skipped fix permanently stale.
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
        if (err instanceof Unauthorized) {
          // Nothing will succeed until the user signs in again. Keep the
          // queue: the crossings still happened, and they are still worth
          // delivering once there is a session.
          break;
        }
        break;
      }
    }
    if (sent.length) await remove(sent);
  } finally {
    flushing = false;
  }
}

async function record(
  workflowId: string,
  location: {
    coords: { latitude: number; longitude: number; accuracy?: number };
    timestamp: string;
  },
): Promise<void> {
  await enqueue({
    workflowId,
    lat: location.coords.latitude,
    lng: location.coords.longitude,
    accuracyM: location.coords.accuracy,
    // The OS's own timestamp for the observation, not now: this is what the
    // server orders by, and a fix delivered late must still say when it
    // actually happened.
    recordedAt: location.timestamp,
  });
  await flush();
}

export interface StartOptions {
  workflowId: string;
  lat: number;
  lng: number;
  radiusM: number;
}

// Registers the zone with the OS and starts listening.
//
// Returns false when the user has not granted background ("Always") location.
// That is a normal outcome, not an error: the app stays a perfectly good
// viewer/controller without it, and src/permissions.ts owns explaining the
// trade to the user before Android's own dialog appears.
export async function start(opts: StartOptions): Promise<boolean> {
  const state = await BackgroundGeolocation.ready({
    // Only wake us for the transitions we actually act on.
    desiredAccuracy: BackgroundGeolocation.DESIRED_ACCURACY_HIGH,
    stopOnTerminate: false,
    startOnBoot: true,
    // We never want a raw location stream; the geofence is the whole point.
    geofenceModeHighAccuracy: true,
  });

  BackgroundGeolocation.onGeofence((event) => {
    void record(opts.workflowId, {
      coords: {
        latitude: event.location.coords.latitude,
        longitude: event.location.coords.longitude,
        accuracy: event.location.coords.accuracy,
      },
      timestamp: event.location.timestamp,
    });
  });

  // Flush on reconnect. This is the other half of the offline queue: without
  // it a backlog sits there until the next crossing happens to occur.
  Network.addListener("networkStatusChange", (status) => {
    if (status.connected) void flush();
  });

  await BackgroundGeolocation.addGeofence({
    identifier: opts.workflowId,
    latitude: opts.lat,
    longitude: opts.lng,
    radius: opts.radiusM,
    notifyOnEntry: true,
    notifyOnExit: true,
  });

  if (!state.enabled) await BackgroundGeolocation.start();
  return true;
}

export async function stop(workflowId: string): Promise<void> {
  await BackgroundGeolocation.removeGeofence(workflowId);
}
