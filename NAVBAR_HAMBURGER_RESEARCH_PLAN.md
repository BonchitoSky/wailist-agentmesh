# Navbar research → hamburger architecture

**Goal:** replace the current "hide the nav, dump the routes in the account menu"
stopgap with a real navigation surface that scales to many pages.
**Method:** measured apple.com's `#globalnav` directly in a browser — computed
styles and the actual CSSOM rules, not blog posts — then checked the interaction
model against the W3C/ARIA guidance for navigation disclosures.

---

## 1. What apple.com actually does

All numbers below were read out of Apple's live stylesheets and computed styles
on 2026-08-03. Nothing here is inferred.

### 1.1 The shell

| Property            | Desktop                                     | ≤833px             |
| ------------------- | ------------------------------------------- | ------------------ |
| `#globalnav` height | 44px                                        | 48px               |
| Position            | `fixed`, `z-index: 9999`                    | same               |
| Background          | `rgba(255,255,255,0.8)`                     | opens to `#fafafc` |
| Backdrop            | `backdrop-filter: saturate(1.8) blur(20px)` | same               |

The frosted bar is one rule: a translucent background plus
`saturate(1.8) blur(20px)`. The saturation boost is what stops content behind
the blur from going grey — blur alone desaturates.

### 1.2 The breakpoint

`@media (max-width: 833px)` — that is the hamburger threshold, and it is a
single one. There is no intermediate "medium" nav state. Above it, full inline
links; below it, trigger + full-screen sheet.

### 1.3 The open animation — height, not transform

This is the central finding, and it is not what most implementations do.

```css
/* ≤833px */
#globalnav .globalnav-content {
  position: absolute;
  top: 0;
  width: 100%;
  height: 100%;
}
#globalnav.globalnav-animating .globalnav-content {
  transition:
    height var(--r-globalnav-flyout-rate) cubic-bezier(0.4, 0, 0.6, 1) 80ms,
    background var(--r-globalnav-flyout-rate) cubic-bezier(0.4, 0, 0.6, 1) 80ms;
}
#globalnav.globalnav-with-flyout-open .globalnav-content {
  height: calc(100vh - var(--globalnav-preceding-element-height, 0px));
  background: var(--r-globalnav-background-opened);
  overflow: hidden scroll;
  transition-delay: 0ms; /* ← opens immediately, closes after 80ms */
}
```

`--r-globalnav-flyout-rate` computes to **0.406s**.

Three things worth copying:

1. **The bar itself grows into the sheet.** It is not a separate panel that
   slides over the page — the nav's own container animates its height from 48px
   to full viewport. There is no seam between "bar" and "menu", which is most of
   why it reads as one continuous object rather than two.
2. **`transition-delay` is asymmetric.** Opening runs at 0ms delay; closing
   inherits the 80ms. Opening feels instant, closing feels unhurried. Same
   duration, opposite perceived weight.
3. **The height target subtracts a variable**, `--globalnav-preceding-element-height`,
   so a banner above the nav doesn't push the sheet off-screen.

### 1.4 The cross-fade

The top-level links do not slide away — they fade, and the sheet content fades
in behind them:

```css
/* resting */
.globalnav-list > .globalnav-item:not(.globalnav-menu) .globalnav-link {
  opacity: 1;
  visibility: visible;
  transition:
    opacity 0.24s cubic-bezier(0.4, 0, 0.6, 1) 80ms,
    visibility 0.24s step-start 80ms;
}
/* open */
.globalnav-with-flyout-open ... .globalnav-link {
  opacity: 0;
  visibility: hidden;
  transition:
    opacity 0.24s cubic-bezier(0.4, 0, 0.6, 1),
    visibility 0.24s step-end;
}
```

**`visibility` is transitioned with `step-start` / `step-end`.** This is the
technique that makes the whole thing feel clean: `visibility` is a discrete
property, so stepping it flips it at one end of the timeline instead of
interpolating. Opening → `step-start` makes it visible on frame 1 so it is
interactive for the entire fade. Closing → `step-end` keeps it hittable until
the fade finishes, then removes it from hit-testing _and the accessibility tree_.
`opacity: 0` alone would leave an invisible, still-clickable, still-screen-readable
element behind.

### 1.5 The scrim

```css
.globalnav-curtain {
  position: fixed;
  inset: 0;
  z-index: 9998;
  background: rgba(232, 232, 237, 0.4);
  backdrop-filter: blur(20px);
  opacity: 0;
  visibility: hidden;
}
.globalnav-with-flyout-open ~ .globalnav-curtain {
  opacity: 1;
  visibility: visible;
  transition:
    opacity 0.32s cubic-bezier(0.4, 0, 0.6, 1) 80ms,
    visibility 0.32s step-start 80ms;
}
```

Sits one z-index below the nav (9998 vs 9999) and is a **sibling**, targeted with
`~`. No JS toggles it; it reacts to the state class on the nav.

### 1.6 The timing vocabulary

Four durations and exactly one easing curve across the entire component:

| Duration   | Used for                            |
| ---------- | ----------------------------------- |
| **0.24s**  | link opacity cross-fade             |
| **0.32s**  | scrim opacity, link colour on hover |
| **0.406s** | the height expansion                |
| **80ms**   | the stagger delay between layers    |

Easing is **`cubic-bezier(0.4, 0, 0.6, 1)`** everywhere — a symmetric ease-in-out.
Not a spring, not an overshoot. The smoothness comes from _consistency and
layering_, not from an expressive curve.

### 1.7 The hamburger icon

Two SVG `<polyline>` elements (`#globalnav-menutrigger-bread-top` / `-bottom`),
`stroke-width: 1.2`, `stroke-linecap: round`, points `2 12, 16 12`. The morph to
an X animates the **polyline geometry**, not a CSS rotation of two divs — which
is why there are no `transform` transitions on them. The button is a 48×48 hit
target with `outline-offset: -7px` so the focus ring sits inside the box.

### 1.8 Escape hatches they built in

- `.globalnav-block-transitions` sets `transition: none !important` — used to
  suppress animation during resize and first paint, so the nav never animates
  just because the viewport changed.
- `#globalnav.touch .globalnav-menutrigger-button { transition: none }` — touch
  pointers get no hover transition at all.

### 1.9 Scroll lock

`overflow: hidden` on **both** `<html>` and `<body>`, plus a `globalnav-scrim`
class on `<body>`.

### 1.10 Where we should not copy them

- **Reduced motion.** `<html>` carries a `no-reduced-motion` class from their JS
  feature detection, but of the 10 readable stylesheets on the homepage there are
  **zero `prefers-reduced-motion` media blocks and zero rules consuming that
  class**. The nav animation is not gated on it. Our app already respects
  `prefers-reduced-motion` in `globals.css`; we should keep doing so and be
  better than the reference here.
- **`aria-expanded` is absent** on `#globalnav-menutrigger-button` (it has
  `aria-controls="globalnav-list"` and `aria-label="Menu"` only). State is carried
  in classes. That fails the disclosure pattern — see §2.
- **`100vh`**, not `100dvh`, for the open sheet height. We just removed `100vh`
  across our app for exactly the mobile-chrome reason; don't reintroduce it.
- **Visual identity.** Copy the mechanics — height-growth, stepped visibility,
  layered timing. Not the frosted-white look, type, or icon shapes. Our dark token
  set stays the source of truth.

---

## 2. The accessibility model we should use

Apple's markup is not the pattern to follow here. Per the W3C ARIA Authoring
Practices, site navigation should use the **disclosure** pattern, not `role="menu"`:

- `role="menu"` / `role="menuitem"` describe an _application_ menu with roving
  focus and arrow-key traversal. Screen readers announce and navigate them
  accordingly. For a list of links this is the classic "bad ARIA is worse than no
  ARIA" case.
- Correct shape: a `<button>` with `aria-expanded` and `aria-controls`, disclosing
  a plain `<nav>` containing a `<ul>` of links.
- `aria-expanded` belongs on a **button, never a link**.
- Escape closes and returns focus to the trigger; moving focus out of the region
  closes it.

**This is a live defect in our code, not a hypothetical.** `Topbar.tsx` renders
`<div className="profile-menu__panel" role="menu">` with `role="menuitem"`
buttons — and the mobile nav routes I added in the last PR inherited that, so our
three primary routes are currently announced as application menu items. The
landing sheet (`.lp-sheet`) has the opposite problem: correct-ish semantics but
no `aria-controls`, no focus management, and no focus return.

---

## 3. What this means for AgentMesh

Current state after the mobile-fix branch: below 768px the routes hide and
reappear inside the **account menu**. That was the right minimal fix for three
routes. It does not survive growth:

- Account menu ≠ site navigation. Mixing "Workflows / Usage / Credits" with
  "Settings / Sign out" conflates identity with wayfinding, and gets worse with
  every page added.
- One flat list has no room for grouping, section labels, or an active-section
  indicator.
- Two competing overflow surfaces already exist — `.profile-menu__panel` (authed
  pages) and `.lp-sheet` (landing) — with different markup, different CSS, and
  different close behaviour. A third page type would make three.

**Target:** one `<AppNav>` primitive. Above the breakpoint it renders inline
links; below it, a trigger plus a full-height sheet that grows out of the bar.
Both the landing page and the authed pages consume it; the account menu goes
back to being only account actions.

---

## 4. Workflow

Ten commits. Commits 1–3 are the foundation, 4–6 the component, 7–9 adoption,
10 verification.

| #   | Commit                                                                   | Files                                                   |
| --- | ------------------------------------------------------------------------ | ------------------------------------------------------- |
| 1   | `motion: add the shared timing and easing tokens`                        | `app/globals.css`                                       |
| 2   | `nav: add the route manifest as the single source of truth`              | `lib/nav.ts` (new)                                      |
| 3   | `nav: add the scrim and body scroll-lock primitives`                     | `app/globals.css`, `hooks/useScrollLock.ts` (new)       |
| 4   | `nav: add AppNav — inline links above the breakpoint, sheet below`       | `components/nav/AppNav.tsx` (new), `app/globals.css`    |
| 5   | `nav: animate the sheet by growing the bar's own height`                 | `components/nav/AppNav.tsx`, `app/globals.css`          |
| 6   | `nav: make the trigger a real disclosure and manage focus`               | `components/nav/AppNav.tsx`                             |
| 7   | `topbar: adopt AppNav and give the account menu back to account actions` | `components/Topbar.tsx`, `app/globals.css`              |
| 8   | `landing: adopt AppNav and drop the bespoke sheet`                       | `components/landing/LandingPage.tsx`, `app/globals.css` |
| 9   | `nav: drop role=menu from the account panel for the disclosure pattern`  | `components/Topbar.tsx`                                 |
| 10  | `nav: suppress transitions on resize and honour reduced motion`          | `app/globals.css`, `components/nav/AppNav.tsx`          |

### Commit 1 — motion tokens

Adopt the layered vocabulary, adapted to our existing scale. `globals.css`
already defines `--ease: cubic-bezier(0.2, 0.8, 0.2, 1)`, which is a _decelerating_
curve — right for things entering, wrong for a symmetric open/close. Add a second
curve rather than changing the existing one (it is used across the app):

```css
:root {
  --ease-nav: cubic-bezier(0.4, 0, 0.6, 1);
  --nav-rate-fade: 0.24s;
  --nav-rate-scrim: 0.32s;
  --nav-rate-sheet: 0.4s;
  --nav-stagger: 80ms;
  --nav-h: 56px; /* our bar is 56px, not Apple's 44/48 */
}
```

### Commit 2 — route manifest

`NAV_ITEMS` currently lives in `Topbar.tsx` and `NAV_SECTIONS` in
`LandingPage.tsx`. With more pages coming, both move to `lib/nav.ts` with room to
grow:

```ts
export type NavItem = {
  label: string;
  href: string;
  group?: string; // section heading in the sheet
  match?: (path: string) => boolean; // active-state override
};
```

Grouping is the thing the current flat list cannot express and the reason this
matters before the page count grows.

### Commit 3 — scrim + scroll lock

Scrim as a sibling of the nav, driven by a state class, per §1.5. Scroll lock
sets `overflow: hidden` on both `<html>` and `<body>` and **restores the previous
values on close** rather than blindly clearing them. Must also preserve scroll
position — the naive version jumps the page to the top on close.

### Commit 4 — the component

```
<AppNav>
  bar:      [brand] [inline links ≥bp]        [actions] [trigger <bp]
  sheet:    grouped links, only rendered/mounted below bp
  scrim:    sibling, class-driven
```

Breakpoint: **768px**, matching the `md` token established in
`MOBILE_UI_FIX_PLAN.md` §2. Not Apple's 833px, and not the 720px in
`NAVBAR_UI_POLISH_PLAN.md:391` — one number, app-wide.

CSS-driven visibility (`hide-md` / `show-md` utilities already exist), so there
is no `useMediaQuery`, no hydration mismatch, and no post-load flash.

### Commit 5 — the growth animation

The bar's container animates `height` from `var(--nav-h)` to
`calc(100dvh - var(--nav-offset, 0px))` — `dvh`, per §1.10. Asymmetric delay:
0ms opening, `var(--nav-stagger)` closing. Inline links cross-fade out with
stepped `visibility`, exactly as §1.4.

> **Known cost, stated up front:** animating `height` is not compositor-only —
> it triggers layout on every frame. Apple ships it, and for a bar-to-sheet
> expansion the alternative (`transform: scaleY`) distorts the children. Accept
> it, but keep the sheet's _contents_ opacity-only, and verify frame rate in
> commit 10 rather than assuming.

### Commit 6 — disclosure semantics

`<button aria-expanded aria-controls>` disclosing a `<nav><ul>`. Escape closes
and returns focus to the trigger. Focus moves into the sheet on open. Focus
leaving the region closes it. No `role="menu"`.

### Commits 7–8 — adoption

Topbar and landing both consume `AppNav`; `.lp-sheet` and the mobile branch of
`.profile-menu__panel` are deleted. Net CSS should come out roughly flat despite
adding a component — two bespoke surfaces collapse into one.

### Commit 9 — fix the ARIA on the account panel

Per §2. Separate commit because it changes announced semantics on a shipped
surface and should be revertable on its own.

### Commit 10 — resize and reduced motion

- `.nav-block-transitions` (per §1.8) applied during resize so the sheet never
  animates because the viewport changed. Add it on `resize`, remove on the next
  frame.
- `@media (prefers-reduced-motion: reduce)`: height and opacity transitions →
  `none`; the sheet still opens and closes, instantly. Being better than the
  reference here is deliberate (§1.10).

---

## 5. Verification

Reuse the scripted harness from the mobile branch — real navigations, not
`history.pushState` (that failure is documented in `MOBILE_UI_FIX_PLAN.md` §7).

1. **Semantics:** trigger is a `<button>`; `aria-expanded` flips true/false;
   `aria-controls` resolves to the sheet's id; zero `role="menu"` in the nav
   subtree.
2. **Focus:** Tab enters the sheet on open; Escape closes _and_ returns focus to
   the trigger; focus leaving the region closes it.
3. **Stepped visibility:** after close completes, no link inside the sheet is
   hit-testable (`elementFromPoint` misses it) and none is in the a11y tree.
4. **Scroll lock:** `scrollY` is identical before open and after close.
5. **Breakpoint:** 768 collapsed / 769 inline, both directions; resizing across
   it never plays the open animation.
6. **Motion:** with `prefers-reduced-motion: reduce` emulated, computed
   `transition-duration` on the sheet is `0s`.
7. **Frame rate:** record the open with `performance` / long-task observation at
   375px; investigate if the height animation drops frames on a mid-tier profile.
8. **Desktop regression:** 1440px bar pixel-identical to `master`.

---

## 6. Open questions for the maintainers

1. **Which pages are coming?** The manifest's grouping (commit 2) should be
   designed against the real list, not invented. This is the one input that would
   change the shape of the work.
2. **Does the sheet need nested sections?** Apple's flyout has a second level.
   The plan above builds one flat-but-grouped level; second-level disclosure is a
   meaningful addition and should be scoped separately if wanted.
3. **Keep the frosted bar?** `backdrop-filter: saturate(1.8) blur(20px)` over our
   dark tokens is cheap to add and reads well, but it is a visual change to a
   shipped surface, so it is not assumed here.

---

## 7. Sources

Primary evidence is direct measurement of `https://www.apple.com`'s live
stylesheets and computed styles (2026-08-03); the CSS in §1 is quoted from the
CSSOM as served. Interaction-model guidance:

- [Disclosure Navigation Menu — W3C ARIA APG](https://www.w3.org/WAI/ARIA/apg/patterns/disclosure/examples/disclosure-navigation/)
- [Disclosure Navigation Menu with Top-Level Links — W3C ARIA APG](https://www.w3.org/WAI/ARIA/apg/patterns/disclosure/examples/disclosure-navigation-hybrid/)
- [ARIA: menu role — MDN](https://developer.mozilla.org/en-US/docs/Web/Accessibility/ARIA/Reference/Roles/menu_role)
- [aria-expanded — MDN](https://developer.mozilla.org/en-US/docs/Web/Accessibility/ARIA/Reference/Attributes/aria-expanded)
- [Building Accessible Menu Systems — Smashing Magazine](https://www.smashingmagazine.com/2017/11/building-accessible-menu-systems/)
- [Link + Disclosure Widget Navigation — Adrian Roselli](http://adrianroselli.com/2019/06/link-disclosure-widget-navigation.html)

Related internal docs: `MOBILE_UI_FIX_PLAN.md` (breakpoint tokens, verification
harness), `NAVBAR_UI_POLISH_PLAN.md` (visual polish of the current bar; its
720px collapse is superseded by §4 commit 4).

---

## 8. Implementation status — BLOCKED

Branch `feature/appnav-hamburger`, stacked on `fix/mobile-responsive-ui` (it
depends on that branch's `hide-md`/`show-md` utilities and 768px token).

### Working and verified

- Route manifest (`lib/nav.ts`) with grouping support; `Topbar` and the sheet
  read from it, so the two surfaces cannot drift.
- `useScrollLock` — locks `<html>` and `<body>`, restores the previous inline
  values, and pins/restores scroll position. **Verified:** `htmlOvf` flips
  `visible → hidden → visible`, `scrollY` identical before open and after close.
- Disclosure semantics. **Verified:** trigger is a `<button>`, `aria-expanded`
  flips `false ↔ true`, `aria-label` swaps Menu/Close menu, `aria-controls`
  resolves to the sheet element.
- **`role="menu"` / `role="menuitem"` count is now 0** across the authed shell —
  the ARIA defect in §2 is fixed.
- Sheet geometry: `position: fixed` under the bar, correctly measured at
  `top: 56px, height: 756px` in a 812px viewport.

### Blocked

**The sheet never becomes visible.** `.appnav--open .appnav__sheet` (and
`.appnav--open ~ .appnav__scrim`) do not take effect, so opacity stays 0 and
visibility stays hidden while open.

What was ruled out, in order:

| Hypothesis                 | Test                                                     | Result                                                |
| -------------------------- | -------------------------------------------------------- | ----------------------------------------------------- |
| Stale HMR CSS              | hard reload                                              | unchanged                                             |
| Invalid `calc`/`dvh` value | same value on a probe element                            | resolves to 812px correctly                           |
| Selector does not match    | `el.matches()` for both class and attribute forms        | `true`                                                |
| A competing rule wins      | enumerated every matching rule in the CSSOM              | only 2, the open rule wins on specificity _and_ order |
| Author-origin override     | runtime-injected `!important` rule that provably matches | **still ignored**                                     |

The last row is the anomaly: an injected `opacity: 1 !important; visibility:
visible !important` rule, confirmed matching via `matches()`, leaves the
computed values at `0` / `hidden`. In an earlier variant that grew the bar's own
absolutely-positioned container, the same shape of failure appeared on `height` —
ignored at `!important` and inline — while `outline` in the _same rule_ applied.

That combination is not explainable by ordinary cascade rules, which is why the
investigation was stopped rather than continued by guesswork.

### Next step

Inspect the element's actual matched-rules list in real DevTools (or via the
Chrome DevTools MCP) instead of inferring from `getComputedStyle`. That shows
directly which declarations are struck through and why — the one thing the
computed-style probes could not reveal. Verify in a real browser too, in case
this is specific to the automation context.

Until then this branch must not merge: the trigger renders and toggles state but
opens nothing, which is worse than the shipped behaviour on
`fix/mobile-responsive-ui`.
