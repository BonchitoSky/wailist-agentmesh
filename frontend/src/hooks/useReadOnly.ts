"use client";
import { useSyncExternalStore } from "react";
import {
  HOVER_NONE_QUERY,
  POINTER_COARSE_QUERY,
  isHandheldNow,
} from "@/lib/device";

// Whether this client is a viewer rather than an editor.
//
// Keyed on the DEVICE, not the window. A laptop dragged narrow is still a
// laptop and keeps the full editor; a phone or tablet is a viewer at any size.
// See lib/device.ts for the decision table and why width plays no part in it.
//
// Layout is a separate question with a separate answer -- useIsCompact still
// measures width, because how something stacks genuinely does depend on how
// much room there is.

function subscribe(onChange: () => void): () => void {
  if (typeof window === "undefined" || !window.matchMedia) return () => {};
  // Both queries are live: pairing a Bluetooth mouse to a tablet, or
  // undocking a convertible, changes the primary pointer mid-session.
  const queries = [POINTER_COARSE_QUERY, HOVER_NONE_QUERY].map((q) =>
    window.matchMedia(q),
  );
  queries.forEach((mql) => mql.addEventListener("change", onChange));
  return () =>
    queries.forEach((mql) => mql.removeEventListener("change", onChange));
}

// The server has no device to inspect, so it renders the editor. That is both
// the safe default and the common case, and readDeviceSignals() returns the
// same desktop-shaped answer when `window` is missing -- so the SSR markup and
// the first client render agree, and React reconciles to the viewer afterwards
// on the devices that need it.
function getServerSnapshot(): boolean {
  return false;
}

export function useReadOnly(): boolean {
  return useSyncExternalStore(subscribe, isHandheldNow, getServerSnapshot);
}
