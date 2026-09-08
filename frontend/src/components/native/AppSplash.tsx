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

// Long enough for one full sweep, so a fast boot does not flash the wordmark
// and vanish. Under this the animation reads as a glitch rather than a load.
const MIN_VISIBLE_MS = 700;

// The ceiling. Android's own guidance is that a splash should last as long as
// loading genuinely takes and no longer -- artificial timers are called out by
// name -- so this is a cap, not a duration: boot almost always wins the race.
// It exists so a stalled restore cannot hold someone on a logo forever;
// NativeBoot has its own 10s timeout, which is far too long to stare at this.
const MAX_VISIBLE_MS = 2000;

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
    // Tell the OS it can drop the launch window: this component is painted, so
    // there is something behind it. Deliberately not awaited -- if the plugin
    // is missing the splash simply auto-hides as it used to, and the app must
    // not wait on a teardown it does not depend on.
    void import("@capacitor/splash-screen")
      .then(({ SplashScreen }) => SplashScreen.hide())
      .catch(() => {});

    const started = Date.now();
    const leave = () => {
      if (done) return;
      done = true;
      setPhase("leaving");
      window.setTimeout(() => setPhase("gone"), FADE_MS);
    };

    // Whichever comes first: the shell is ready (having waited out the floor),
    // or the cap. authReady resolves even when boot fails -- NativeBoot calls
    // markAuthReady in a finally -- so this cannot hang on a broken restore.
    void authReady.then(() => {
      const remaining = MIN_VISIBLE_MS - (Date.now() - started);
      if (remaining > 0) window.setTimeout(leave, remaining);
      else leave();
    });
    const cap = window.setTimeout(leave, MAX_VISIBLE_MS);

    return () => {
      done = true;
      window.clearTimeout(cap);
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
