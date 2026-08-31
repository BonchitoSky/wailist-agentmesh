"use client";
import { useEffect } from "react";
import { IS_NATIVE, setAuthToken } from "@/lib/nativeAuth";

// Restores the session the native shell persisted, once, on first mount.
//
// The join between two halves that stay ignorant of each other: the shell owns
// durable storage and the OS, the web bundle owns the API client. The token is
// handed across here rather than either side importing the other.
//
// The import is dynamic and guarded by IS_NATIVE, a build-time constant. In a
// browser build that branch is dead, so the Capacitor chunk is never fetched;
// on device it loads once. A static import would pull @capacitor/* into every
// page of the web app to satisfy a branch that never runs there.
//
// Renders nothing. Mounted in the root layout because the session has to be
// restored before any page makes its first authenticated call, not when some
// particular screen happens to appear.
export function NativeBoot() {
  useEffect(() => {
    if (!IS_NATIVE) return;
    let cancelled = false;
    void import("@/native")
      .then(({ boot }) => boot())
      .then((token) => {
        if (!cancelled && token) setAuthToken(token);
      })
      .catch((err) => {
        // A shell that cannot boot must not take the app down with it. The
        // viewer still works signed out; the alternative is a blank screen
        // with the reason visible only in logcat.
        console.error("native shell failed to boot", err);
      });
    return () => {
      cancelled = true;
    };
  }, []);
  return null;
}
