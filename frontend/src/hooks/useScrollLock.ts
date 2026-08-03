"use client";
import { useEffect } from "react";

/**
 * Freezes page scroll while `locked` is true.
 *
 * Two details the naive version gets wrong:
 *
 * 1. It restores the *previous* inline overflow rather than clearing it, so a
 *    page that deliberately sets its own overflow is not trampled by the menu
 *    closing.
 * 2. It pins the scroll position. Setting `overflow: hidden` on the document
 *    discards the scroll offset in some engines, so the page jumps to the top
 *    when the lock lifts; capturing `scrollY` and restoring it afterwards keeps
 *    the reader where they were.
 *
 * Applied to both <html> and <body> — locking only one leaves iOS Safari able
 * to scroll the other.
 */
export function useScrollLock(locked: boolean) {
  useEffect(() => {
    if (!locked) return;

    const html = document.documentElement;
    const body = document.body;
    const prevHtml = html.style.overflow;
    const prevBody = body.style.overflow;
    const scrollY = window.scrollY;

    html.style.overflow = "hidden";
    body.style.overflow = "hidden";

    return () => {
      html.style.overflow = prevHtml;
      body.style.overflow = prevBody;
      window.scrollTo(0, scrollY);
    };
  }, [locked]);
}
