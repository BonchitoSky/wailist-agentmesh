# Mobile responsive UI — fix plan

> **Status: implemented.** All nine commits are on `fix/mobile-responsive-ui`.
> Deviations from the plan as written, and the verification actually run, are
> recorded in §7 at the bottom.

**Branch:** `fix/mobile-responsive-ui` (cut from `master` @ `2788a44`, which includes the merged navbar PR #15)
**Scope:** frontend only — `wailist-agentmesh/frontend/src`
**Reported on:** mobile Chrome / Android (~375–412 CSS px), screenshots of `/`, `/signin`, `/workflows`

---

## 1. Diagnosis — why it looks like that

The app was built desktop-first with **inline styles**, which cannot express media
queries. The result is measurable: the entire `src` tree contains **11 media
queries**, and only three are layout rules —
[`globals.css:327`](frontend/src/app/globals.css:327) (`usage-split`),
[`billing/page.tsx:29`](frontend/src/app/billing/page.tsx:29) (`bill-grid`), and
two in the checkout modal. Landing, auth, workflows, usage and the topbar have
**zero** responsive handling.

Everything in the screenshots follows from four root causes.

### RC-1 — Absolutely-centred landing nav collides with its neighbours

[`LandingPage.tsx:134-137`](frontend/src/components/landing/LandingPage.tsx:134)

```
nav { display:flex; position:absolute; left:50%; transform:translateX(-50%) }
```

The nav is taken **out of flow**, so the flex row holding `Logo` and the
`Sign Up` / `Open Studio` cluster reserves no space for it. On a phone the three
links are painted straight over both. This is exactly screenshot 1: `AgentMesh`
overprinted by `Overview`, `Waitlist` overprinted by `Open Studio`.

### RC-2 — Fixed-height buttons + wrapping labels = text escaping its box

[`ui/index.tsx:134-147`](frontend/src/components/ui/index.tsx:134) sets
`height: 28` with **no `whiteSpace: nowrap` and no `flexShrink: 0`**. Squeezed
into a narrow flex row the label wraps to 2–3 lines inside a 28 px-tall box and
spills out. Screenshot 3's `Load demo workflow` / `Load canix402 workflow`, and
screenshot 1's `How it works`, are this bug.

The same style block is **duplicated in four places** —
[`ui/index.tsx:134`](frontend/src/components/ui/index.tsx:134),
[`WorkflowsPage.tsx:775,789`](frontend/src/components/workflows/WorkflowsPage.tsx:775),
[`AuthPage.tsx:580`](frontend/src/components/auth/AuthPage.tsx:580),
[`CanvasPage.tsx:832,862`](frontend/src/components/canvas/CanvasPage.tsx:832) —
so the fix must land in all four (consolidation follow-up in §6).

### RC-3 — Hard-coded column counts that never collapse

| Location                                                                               | Rule                             | Effect at 375 px                                               |
| -------------------------------------------------------------------------------------- | -------------------------------- | -------------------------------------------------------------- |
| [`AuthPage.tsx:109`](frontend/src/components/auth/AuthPage.tsx:109)                    | `gridTemplateColumns: "1fr 1fr"` | each column ≈ 187 px; form crushed, marketing panel off-screen |
| [`WorkflowsPage.tsx:169`](frontend/src/components/workflows/WorkflowsPage.tsx:169)     | `repeat(4, 1fr)`                 | four ≈ 80 px KPI cards, every label wrapped                    |
| [`billing/page.tsx:254`](frontend/src/app/billing/page.tsx:254)                        | `repeat(4, 1fr)`                 | same                                                           |
| [`WorkflowsPage.tsx:492,519`](frontend/src/components/workflows/WorkflowsPage.tsx:492) | 7 columns, ~862 px min           | horizontal scroll inside the card                              |

`AuthPage`'s grid is also wrapped in `height: 100vh; overflow: hidden`
([`:110-111`](frontend/src/components/auth/AuthPage.tsx:110)), so the overflow is
**clipped rather than scrollable** — screenshot 2's "accountable" is cut off with
no way to reach it.

### RC-4 — `100vh` on mobile browsers

Used at [`AuthPage.tsx:110`](frontend/src/components/auth/AuthPage.tsx:110),
[`LandingPage.tsx:80,96,117,129`](frontend/src/components/landing/LandingPage.tsx:80),
[`UsagePage.tsx:118`](frontend/src/components/usage/UsagePage.tsx:118),
[`billing/page.tsx:91`](frontend/src/app/billing/page.tsx:91),
[`CanvasPage.tsx:342,360`](frontend/src/components/canvas/CanvasPage.tsx:342).

On mobile Chrome/Safari `100vh` excludes browser chrome, so a `100vh` box is
taller than the visible area and its bottom sits permanently under the URL bar.
`100dvh` is the correct unit.

### Not a cause — viewport meta

There is no `export const viewport` anywhere in `src`, but Next 16 App Router
injects `width=device-width, initial-scale=1` by default, so pages _are_
rendering at device width (confirmed by the crushing, rather than a zoomed-out
desktop render). We add the explicit export anyway — one line, question closed.

---

## 2. Approach — CSS classes, not a JS breakpoint hook

Inline styles can't hold media queries, so each fix needs a `className` backed by
a rule in `globals.css`. The alternative — a `useMediaQuery` hook feeding inline
styles — is **rejected**: it renders the desktop layout on the server then snaps
to mobile after hydration (layout flash, plus hydration-mismatch risk on every
page).

Rule: **CSS-first.** Add a small utility layer to `globals.css` and attach classes
to the existing inline-styled elements. Inline styles stay for everything
non-responsive, so diffs stay small and reviewable.

**Breakpoints** (standardised, documented in `globals.css`):

| Token | Width     | Target                                   |
| ----- | --------- | ---------------------------------------- |
| `lg`  | ≤ 1024 px | small laptop / landscape tablet          |
| `md`  | ≤ 768 px  | tablet portrait — **nav collapses here** |
| `sm`  | ≤ 520 px  | phone                                    |

Two queries already exist nearby (1024 px `usage-split`, 900 px `bill-grid`).
Migrating `bill-grid` 900 → 768 is a behaviour change and gets its own commit
(§4, commit 8) so it never rides along with a bug fix.

---

## 3. What each screen becomes

### Topbar — the "only half visible" navbar

Today it is one 56 px non-wrapping flex row holding logo + hairline +
`Acme Capital ▾` + network pill + 3 nav links + avatar
([`Topbar.tsx:85-137`](frontend/src/components/Topbar.tsx:85)). That needs
~560 px; at 375 px the right end is simply off-screen.

> **Aligned with an existing plan.** `NAVBAR_UI_POLISH_PLAN.md:382-395` already
> specs this collapse — hide the nav and the org chip, surface Workflows / Usage /
> Credits as items at the top of the **existing profile menu**, "one overflow
> surface, no second menu component." That design is adopted here rather than
> competing with it: it reuses the panel's markup, CSS and a11y work, and avoids a
> second open/close state machine alongside the hover/pin one at
> [`Topbar.tsx:32-77`](frontend/src/components/Topbar.tsx:32).
>
> **One deviation to confirm:** that plan uses **720 px**; this plan uses **768 px**
> everywhere else. Recommend 768 for a single consistent `md` breakpoint — it is a
> one-number change in that doc. Flagging rather than silently overriding.

- **> 768 px** — unchanged.
- **≤ 768 px** — bar keeps `Logo`, the network `Pill` and the avatar. `.tb-nav`
  and the `Acme Capital ▾` chip are `display: none`; the three routes appear as
  `.profile-menu__item`s above the existing `Settings` / `Sign out` items, with a
  `__divider` between the groups.
- The nav elements need class hooks first — `.tb-nav`, `.tb-brand__org` do not
  exist in `Topbar.tsx` yet (that plan is written but unimplemented). Commit 3
  adds them.
- Active route keeps `aria-current="page"` in the collapsed form.
- Close the menu on route change (new) as well as outside press and Escape
  (already handled).

### Landing (`/`)

- Nav becomes `position: static; transform: none` at ≤ 768 px — **kills RC-1**.
- ≤ 520 px: the three inline links collapse into a hamburger sheet; the bar keeps
  `Logo` + `Open Studio`, and `Sign Up` moves into the sheet. (The landing page
  has no profile menu to fold into, so here a sheet is the only option.)
- Hero `fontSize: clamp(80px, 16vw, 220px)`
  ([`:188`](frontend/src/components/landing/LandingPage.tsx:188)) →
  `clamp(44px, 13vw, 220px)`. The **80 px floor** is what makes `AgentMesh` span
  edge-to-edge at 375 px — `16vw` is only 60 px there, so the floor wins.
- Drop the hard `<br />` in the subtitle
  ([`:198`](frontend/src/components/landing/LandingPage.tsx:198)) at ≤ 520 px; let
  it wrap naturally.
- `LogoMarquee` ([`:231`](frontend/src/components/landing/LandingPage.tsx:231))
  stacks its caption above the track at ≤ 640 px.
- Section padding `128px 32px` → `72px 20px` at ≤ 520 px.

### Auth (`/signin`, `/signup`)

- ≤ 900 px: `gridTemplateColumns: 1fr`, and the right-hand marketing panel
  ([`:433-500`](frontend/src/components/auth/AuthPage.tsx:433)) is `display: none`.
  It is decoration; stacking it below would bury the sign-in button under a
  screen of copy.
- `height: 100vh; overflow: hidden` → `min-height: 100dvh; overflow-y: auto` —
  **fixes the clipped content in screenshot 2**.
- Left column padding `40px 56px` → `24px 20px` at ≤ 520 px.
- `v0.4 · testnet` gets `whiteSpace: nowrap` + `flexShrink: 0` so it stops
  colliding with the logo.

### Workflows (`/workflows`)

- Header row ([`:113-119`](frontend/src/components/workflows/WorkflowsPage.tsx:113)):
  `flexWrap: wrap` + `alignItems: flex-start` at ≤ 768 px; the 4-button cluster
  drops to its own full-width line with `flex: 1 1 auto` buttons.
- `h1` 36 px → 28 px at ≤ 520 px.
- KPI grid `repeat(4,1fr)` → `repeat(2,1fr)` at ≤ 768 px. Deliberately 2-up, not
  1-up — the labels are short and four stacked cards is needless scrolling.
- Controls row ([`:194-201`](frontend/src/components/workflows/WorkflowsPage.tsx:194))
  wraps: search full-width on its own line, filter pills + view toggle below.
- **Table → cards.** At ≤ 768 px the 7-column grid
  ([`:492,519`](frontend/src/components/workflows/WorkflowsPage.tsx:492)) becomes a
  stacked card per workflow — name + status on line 1, the
  agents / runs / spend / updated metrics as a labelled 2-up grid below; header
  row hidden. Horizontal scroll inside a card technically works today but is poor
  on touch.

### Usage (`/usage`)

- `usage-split` already collapses at 1024 px — leave it.
- `100vh` → `100dvh` ([`:118`](frontend/src/components/usage/UsagePage.tsx:118)).
- `SETTLE_GRID` ([`:695`](frontend/src/components/usage/UsagePage.tsx:695)) and
  `EP_GRID` ([`:954`](frontend/src/components/usage/UsagePage.tsx:954)) get the
  same card treatment as workflows at ≤ 768 px.
- Fixed `width: 240` legend
  ([`:935`](frontend/src/components/usage/UsagePage.tsx:935)) →
  `maxWidth: 240; width: 100%`.

### Billing (`/billing`)

- `repeat(4, 1fr)` ([`:254`](frontend/src/app/billing/page.tsx:254)) → 2-up at ≤ 768 px.
- `bill-grid` breakpoint 900 → 768 px for consistency.
- `100vh` → `100dvh`.

### Canvas (`/workflows/[id]`) — explicit scope decision

`CanvasPage` is a drag-and-drop node editor with two mouse-resizable panels
([`CanvasPage.tsx:62-110`](frontend/src/components/canvas/CanvasPage.tsx:62),
plus `PalettePanel`, `Inspector`, `ResizeHandle`). Making it genuinely
touch-usable means pointer-event rework, pinch-zoom and a touch node picker —
a separate project, not a responsive-CSS pass.

**This branch does not make the canvas mobile-usable.** It adds an honest
interstitial at ≤ 768 px — "the workflow editor needs a wider screen", with a link
back to `/workflows` — instead of today's silently broken layout. Calling this
out now rather than half-fixing it.

---

## 4. Commit breakdown

Atomic, in dependency order; each independently reviewable and revertable.

| #   | Commit message                                                                | Files                                                                            |
| --- | ----------------------------------------------------------------------------- | -------------------------------------------------------------------------------- |
| 1   | `layout: declare viewport and add responsive breakpoint utilities`            | `app/layout.tsx`, `app/globals.css`                                              |
| 2   | `ui: stop buttons spilling their labels when squeezed`                        | `components/ui/index.tsx`, `WorkflowsPage.tsx`, `AuthPage.tsx`, `CanvasPage.tsx` |
| 3   | `topbar: add class hooks and fold nav into the account menu on small screens` | `components/Topbar.tsx`, `app/globals.css`                                       |
| 4   | `landing: take the nav out of absolute centering so it stops overlapping`     | `components/landing/LandingPage.tsx`, `app/globals.css`                          |
| 5   | `landing: scale hero type and section padding down on phones`                 | `components/landing/LandingPage.tsx`, `app/globals.css`                          |
| 6   | `auth: stack the split layout and let it scroll on mobile`                    | `components/auth/AuthPage.tsx`, `app/globals.css`                                |
| 7   | `workflows: wrap the header, halve the KPI grid, card up the table`           | `components/workflows/WorkflowsPage.tsx`, `app/globals.css`                      |
| 8   | `usage, billing: stack tables and grids on small screens`                     | `components/usage/UsagePage.tsx`, `app/billing/page.tsx`, `app/globals.css`      |
| 9   | `canvas: tell small screens the editor needs a wider viewport`                | `components/canvas/CanvasPage.tsx`                                               |
| 10  | `layout: swap 100vh for 100dvh so mobile chrome stops clipping`               | all files listed under RC-4                                                      |

Repo conventions: Prettier over every touched file before each commit, no AI
attribution trailers, no force push. PR opens against `upstream/master`.

---

## 5. Verification

**Device matrix** — via `resize_window` against the local dev server:

| Width | Represents                                | Must hold                         |
| ----- | ----------------------------------------- | --------------------------------- |
| 320   | iPhone SE (1st gen)                       | no horizontal page scroll         |
| 375   | iPhone SE / mini — **the reported width** | all three screenshots correct     |
| 390   | iPhone 14/15                              | —                                 |
| 412   | Pixel — the reported Android              | —                                 |
| 768   | tablet portrait                           | nav-collapse boundary, both sides |
| 1024  | small laptop                              | **desktop layout unchanged**      |
| 1440  | desktop                                   | **unchanged**                     |

**Per-width assertions:**

1. `document.documentElement.scrollWidth <= window.innerWidth` on `/`, `/signin`,
   `/signup`, `/workflows`, `/usage`, `/billing` — the strongest single signal,
   and scriptable via `javascript_tool`.
2. No overlapping bounding boxes in the landing nav and topbar
   (`getBoundingClientRect()` intersection check across the nav children) — the
   direct regression test for RC-1.
3. Every button satisfies `scrollHeight <= clientHeight` — proves no label is
   escaping its fixed-height box (RC-2).
4. Account menu opens on tap, all three routes navigate, menu closes on route
   change / outside tap / Escape.
5. `/signin` scrolls to the bottom of the form; the `Sign in` button is reachable.
6. Console clean — no hydration warnings.

**Regression guard:** screenshots at 1440 px on all six routes before commit 1 and
after commit 10 — desktop must be pixel-identical. This is a mobile branch; any
desktop change is a bug.

**Automated:** `npm run lint`, `npx tsc --noEmit`, `npx vitest run` per commit.

> Note — `npm ci` on `master` is currently broken (missing `@emnapi`, a
> pre-existing lockfile desync, not introduced here). Use `npm install` locally;
> if CI fails at install for this reason it is not this branch's regression.

---

## 6. Explicitly out of scope

- **Touch-usable canvas editor** (§3) — needs its own branch.
- **Consolidating the four duplicated button-style blocks** (RC-2) into
  `components/ui` — a real cleanup, but a four-file refactor does not belong in a
  bug-fix branch. Worth a follow-up.
- Migrating inline styles to a styling system wholesale.
- Landscape-phone tuning (< 500 px viewport height).
- Pre-existing and untouched: the `Acme Capital` workspace switcher is hardcoded,
  and `Settings` in the account menu is a no-op
  ([`Topbar.tsx:208-214`](frontend/src/components/Topbar.tsx:208)).

---

## 7. What actually shipped

### Deviations from the plan above

- **Nine commits, not ten.** Planned commits 4 and 5 (landing nav / landing type)
  landed together as `landing: take the nav out of absolute centering and scale
the hero for phones` — same files, same screen, and splitting them after the
  fact would have needed interactive hunk staging.
- **`bill-grid` was left at 900px.** The plan migrated it to 768px "for
  consistency"; on reflection that is a behaviour change with no defect behind
  it. 900px is now the deliberate breakpoint for _two-column content splits_
  (billing and auth), distinct from 768px for _navigation collapse_.
- **The topbar route-change effect was removed.** It tripped
  `react-hooks/set-state-in-effect`, and it was redundant: the menu items already
  close the panel before navigating, and the existing outside-pointerdown handler
  covers navigation triggered elsewhere.
- **The usage endpoints table keeps horizontal scroll.** It is nine columns
  inside an existing `overflowX: auto` container with `minWidth: 984` — already
  self-contained, so it does not break the page. Only the settlements grid, which
  had no scroll container and did overflow, was converted to cards. Carding the
  endpoints table remains open.
- **Implementation note not in the plan:** several fixes had to move a property
  out of its inline style before a breakpoint could reach it, because inline
  styles beat external CSS at any specificity. Where the element's `display` was
  inline (the workflows table header), the `hide-md` utility with `!important` is
  used instead. Grid column counts are driven by custom properties redefined at
  `:root` inside media queries — that is why `--wf-row-cols` and friends exist.

### Verification actually run

Scripted via `javascript_tool` against the dev server, with **real navigations**
(an early sweep using `history.pushState` was discarded — it never re-rendered
the routes and audited one page repeatedly).

| Check                                                     | Result                                                                                     |
| --------------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| Unclipped overflow, `/` and `/workflows` @ 320px          | 0 elements, `scrollWidth == innerWidth`                                                    |
| Landing nav child overlap @ 320 and 375px                 | `[]` — **RC-1 fixed**                                                                      |
| Label spill (`scrollHeight > clientHeight`) @ 320/375/768 | `0` — **RC-2 fixed**                                                                       |
| `/signin` @ 375px                                         | one column, aside dropped, `Sign in` reachable at y=434                                    |
| `/workflows` @ 375px                                      | KPI 2-up, header row hidden, metrics captioned from `data-label`                           |
| `/usage`, `/billing` @ 375px                              | no unclipped overflow, no spill                                                            |
| Account menu @ 375px                                      | Workflows/Usage/Credits + Settings/Sign out; `aria-current` correct; panel inside viewport |
| Collapse boundary                                         | 768px collapsed / 769px desktop — clean on both sides                                      |
| Canvas @ 375px                                            | notice covers viewport (375×812); @1440px hidden, editor renders                           |
| Desktop @ 1440px                                          | KPI 4×296px, 7-column row, h1 36px, padding unchanged, no captions, 0 overflow             |

`tsc --noEmit` and `eslint` are clean (one pre-existing unused-var warning in
`canvas/Inspector.tsx`, untouched here).

**Two pre-existing failures, not caused by this branch** — verified by stashing
all changes and re-running: `src/lib/credits/store.test.ts` fails 2 of 33 tests
(`accumulates balanceUSD across purchases`, `uses creditsUSDOverride instead of
the mock-FX amount`). Credits/FX logic, unrelated to layout.

### Still open

- Touch-usable canvas editor.
- Carding the nine-column usage endpoints table.
- Consolidating the four duplicated button-style blocks into `components/ui`.
- Real-device pass — everything above is a desktop browser at emulated widths.
