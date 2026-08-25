"use client";
import { useIsCompact } from "./useIsCompact";

// Whether this client is a viewer rather than an editor.
//
// It is the same measurement as useIsCompact, and that is the point rather
// than an accident: the studio stops being able to lay out its editor at the
// same width the editor stops being offered. Kept under its own name because
// the two answer different questions -- one is "how should this lay out", the
// other is "what may this client do" -- and a future rule (a licence, an
// account role) would change this one without touching the layout.
export function useReadOnly(): boolean {
  return useIsCompact();
}
