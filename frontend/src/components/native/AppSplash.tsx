"use client";
import { useEffect, useState } from "react";
import { IS_NATIVE, authReady } from "@/lib/nativeAuth";

// The app's own loading screen: the wordmark on black, its colours sweeping
// across, held while the shell actually boots.
//
// Why this exists in the WebView rather than natively. From Android 12 the
// system draws the launch window itself and it cannot be fully customised --
// an icon on a colour, animation capped at 1000ms, and no way to put a wordmark
// there at all. values/styles.xml makes that first frame black with the mark on
// it; this picks up immediately after, and the two are the same colour so the
// join is invisible.
//
// It is also what closes the white flash. Android removes a launch window "as
// soon as the first frame is drawn" -- and the first frame is the empty WebView,
// not the mounted app. With launchAutoHide off in capacitor.config.ts, the
// native splash stays up until this component has painted and calls hide(), so
// there is never a moment with nothing on screen.

// How long the wordmark is held at minimum, and this one is a product decision
// rather than a technical floor.
//
// 700ms was the technical answer: one full sweep, so a fast boot could not flash
// the wordmark and vanish, which reads as a glitch rather than a load. 1700 is
// the deliberate one. Seen on real hardware the screen is worth looking at, and
// a launch that is over before it registers gives that away for nothing.
//
// It also buys the shell a second. NativeBoot's work runs underneath this, so a
// longer hold means more of it is finished before anything is revealed -- fewer
// screens that arrive empty and populate a moment later. That is a side effect
// of holding the screen, not a wait ON the backend: nothing here blocks on a
// request, and a slow server still resolves through authReady exactly as before.
//
// Android's guidance is against artificial timers, and this is one, so it is
// worth being honest about the trade: one second of brand against one second of
// waiting. It is defensible while the screen carries something worth seeing. If
// the wordmark ever goes, this number should go back to 700 with it.
//
// Measured from when the wordmark is VISIBLE, not from mount. Those are not the
// same moment: the native splash sits in its own window on top of the WebView
// until hide() completes, so a floor started at mount can elapse entirely
// behind it.
const MIN_VISIBLE_MS = 1700;

// The backstop, and nothing else. It must be LONGER than the boot it is
// covering or it becomes the normal way out -- which is what went wrong: at
// 2000ms against NativeBoot's 10s timeout, any boot slower than two seconds hit
// the cap, and the cap tore the wordmark away while the app behind it had
// still drawn nothing. What the user got was the splash replaced by an empty
// screen for as long as the app took to render.
//
// authReady always settles inside BOOT_TIMEOUT_MS because NativeBoot resolves
// it in a finally, so this only fires if that contract is broken. Sitting on a
// branded screen for a moment longer is strictly better than sitting on a blank
// one: Android's guidance is that a splash lasts as long as loading takes, and
// loading is not over while the screen is still empty.
const MAX_VISIBLE_MS = 12_000;

// Matches the fade in globals.css. Kept as a constant because the unmount has
// to outlast the transition, and two numbers drifting apart would clip it.
const FADE_MS = 220;

export function AppSplash() {
  // Starts visible, and deliberately not behind a mounted check: the shell is a
  // static export, so this renders into index.html and is on screen in the very
  // first frame the WebView paints. Anything gated on an effect would show the
  // app first and the splash after, which is worse than no splash.
  const [phase, setPhase] = useState<"visible" | "leaving" | "gone">("visible");

  useEffect(() => {
    if (!IS_NATIVE) return;

    let done = false;
    let capTimer: number | undefined;
    let floorTimer: number | undefined;
    let frame: number | undefined;

    const leave = () => {
      if (done) return;
      done = true;
      setPhase("leaving");
      window.setTimeout(() => setPhase("gone"), FADE_MS);
    };

    // Is there anything behind this to reveal?
    //
    // authReady says the SHELL has booted. It does not say React has rendered a
    // screen, and on a slow device those are seconds apart -- long enough that
    // leaving on authReady alone uncovered an empty page and held it there.
    // Deliberately generic: any child of <body> that is not the splash and
    // occupies real space counts, so this never has to know which route the app
    // opened on or what that route calls its root element.
    const appHasPainted = () =>
      Array.from(document.body.children).some(
        (el) =>
          !el.classList.contains("splash") &&
          (el as HTMLElement).getBoundingClientRect().height > 0,
      );

    // Poll on frames rather than a timer: this is a question about what has been
    // drawn, so the moment after a paint is exactly when the answer changes. The
    // splash is animating throughout, so frames are being served -- rAF stalling
    // on an unpainted page is not a risk while this component is the page.
    const leaveOncePainted = () => {
      if (done) return;
      if (appHasPainted()) return leave();
      frame = requestAnimationFrame(leaveOncePainted);
    };

    // The clock starts when the wordmark can actually be SEEN.
    //
    // hide() takes the native splash down, and until it resolves this component
    // is painted but covered. Starting the floor and the cap here rather than at
    // mount is the whole fix: previously both ran while the native window was
    // still on top, so the wordmark's entire life could expire before anyone
    // could see it -- which is what happened, every launch.
    const begin = () => {
      if (done) return;
      const shownAt = Date.now();
      capTimer = window.setTimeout(leave, MAX_VISIBLE_MS);
      void authReady.then(() => {
        const remaining = MIN_VISIBLE_MS - (Date.now() - shownAt);
        if (remaining > 0)
          floorTimer = window.setTimeout(leaveOncePainted, remaining);
        else leaveOncePainted();
      });
    };

    // Tell the OS it can drop the launch window: this component is painted, so
    // there is something behind it. The result is not awaited for correctness --
    // a missing plugin must not stall boot -- but begin() hangs off it either
    // way, so a rejection still starts the clock rather than stranding it.
    void import("@capacitor/splash-screen")
      .then(({ SplashScreen }) => SplashScreen.hide())
      .catch(() => {})
      .finally(begin);

    return () => {
      done = true;
      window.clearTimeout(capTimer);
      window.clearTimeout(floorTimer);
      if (frame !== undefined) cancelAnimationFrame(frame);
    };
  }, []);

  // Web builds never render this: IS_NATIVE is false, so it returns null before
  // any markup exists and the browser's prerendered HTML contains no splash.
  //
  // The JSX below does still ship in the client chunk -- checked, rather than
  // assumed, because the same claim about the landing page in #166 was true and
  // it is tempting to reuse it. It is not true here: nothing removes an
  // unreached return, so a few hundred bytes of inert markup ride along. That
  // is the honest cost, and it is small enough to accept rather than paying for
  // a dynamic import on the one component that must be in the first frame.
  if (!IS_NATIVE || phase === "gone") return null;

  return (
    <div
      className="splash"
      data-leaving={phase === "leaving" ? "" : undefined}
      // Not a live region and not a progress bar: this is the app opening, and
      // a screen reader announcing "loading" over a 700ms logo is noise. It is
      // hidden from the tree entirely, and the screen behind it is what gets
      // announced once it goes.
      aria-hidden="true"
    >
      <span className="splash__mark">
        Agent<span className="splash__sweep">Mesh</span>
      </span>
    </div>
  );
}
