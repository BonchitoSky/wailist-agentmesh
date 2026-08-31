"use client";
import { useEffect } from "react";
// Concrete import, not the @native alias: Turbopack would not resolve that
// alias in any form tried (see the note in the PR). The native shell is
// wired in by a different mechanism -- this keeps the web build honest and
// green in the meantime, since boot() here correctly answers null.
import { boot } from "@/lib/nativeShell";
import { IS_NATIVE, setAuthToken } from "@/lib/nativeAuth";

// Restores the session the shell persisted, once, on first mount.
//
// This is the join between two halves that are deliberately ignorant of each
// other. The shell owns durable storage and the OS; the web bundle owns the
// API client. Neither imports the other -- the token is handed across here, so
// the same bundle still runs in a browser, where @native is a no-op and boot()
// simply answers null.
//
// Renders nothing. Mounted in the root layout because the session has to be
// restored before any page makes its first authenticated call, not when some
// particular screen happens to appear.
export function NativeBoot() {
  useEffect(() => {
    if (!IS_NATIVE) return;
    let cancelled = false;
    void boot().then((token) => {
      if (!cancelled && token) setAuthToken(token);
    });
    return () => {
      cancelled = true;
    };
  }, []);
  return null;
}
