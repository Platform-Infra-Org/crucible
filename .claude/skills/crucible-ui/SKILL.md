---
name: crucible-ui
description: Crucible's UI design system and patterns (the forge look). Use for any UI design or styling task in web/ — a new page, a component, a restyle, a form, an empty state, motion, a theme or token change — before writing the JSX or CSS. Keep this file current whenever a pattern changes.
---

# Crucible UI design

Crucible's interface is a forge: dark charcoal and molten metal by default, steel and iron in the light theme. The look is
quiet and consistent, with heat used sparingly: edges that run warm, a molten rule under titles, metal that flows in
progress bars, sparks when a primary button is struck. **Tasteful, never loud.** Follow this file for every UI task, and
read the existing page closest to what you are building before you start.

**Keep this file current.** When a change adds, alters or removes a pattern below (a token, a component class, a motion
rule, a layout convention), update this skill in the same commit. A stale skill is a bug.

## Where things live

| What | Where |
|---|---|
| Theme tokens, four themes | `web/src/theme/tokens.css` (`forge`, `anvil`, `quench`, `contrast`) |
| All component styles | `web/src/theme/app.css` (plain CSS, no framework; the last section, *The forge finish*, is the global polish layer) |
| Fonts, self-hosted | `web/src/theme/fonts.css`, `web/src/theme/fonts/` (Cinzel, Inter, JetBrains Mono) |
| Theme list for Settings | `web/src/theme/theme.ts` (`THEMES`) |
| Shared components | `web/src/components/` (Loader, ErrorBox, Conflict, MoltenBar, Embers, SparkBurst, Avatar, UserMenu, HeatMap, Toaster, Timer …) |
| Small UI logic, testable | `web/src/lib/` (pure functions with `*.test.ts`) |

## Hard rules

1. **Colours come from tokens only.** `--bg --surface --surface-2 --text --muted --border --accent --accent-2 --ok
   --danger --on-accent --glow --spark`, plus terminal and diff tokens. Never write a hex, `rgb()` or `hsl()` in a
   `color:` or `background:` declaration in `app.css`: `web/src/lib/contrast.test.ts` fails the build. Derive shades with
   `color-mix(in srgb, var(--accent) 30%, var(--border))`. A new colour need means a new token in all four themes.
2. **Every theme, every time.** Check Forge, Anvil (light), Quench and High Contrast. Text pairs are contrast-checked per
   theme in `contrast.test.ts`; High Contrast must meet WCAG AAA. Add a pair there when you introduce a new text/background combination.
3. **Motion is optional.** Every animation must stop under the global rules `[data-calm='true']` (the Calm forge setting)
   and `@media (prefers-reduced-motion: reduce)`. Those rules already kill `animation` and `transition`; don't defeat
   them with `!important` or JS-driven motion that ignores `useCalm()`.
4. **Strict CSP.** No inline `<script>`, no external hosts (fonts, images, scripts). Dynamic values go through React's
   `style` prop (CSSOM), never an HTML `style` attribute string.
5. **Copy is plain and from the user's side**, in the forge voice where it fits (*Ignite the forge*, *The forge has
   cooled*, *Gathering the smiths…*). Buttons say exactly what happens; errors say what went wrong and how to fix it.
6. **Docs follow capability.** A UI change that changes what someone can do updates `docs/user` in the same commit
   (`go test ./internal/docs` checks routes and pages).

## Tokens and type

- Display: **Cinzel** for `h1`–`h3` (set globally). Body: **Inter**, 15px/1.6. Code and ids: **JetBrains Mono**.
- Section labels (panel `h5`, toolbar captions, table headers, card eyebrows): ~0.72–0.85rem, uppercase,
  letter-spacing 0.06–0.1em, `var(--muted)`, weight 600.
- `--glow` is the theme's soft halo (none in High Contrast). `--spark` is white-hot (motes, icon cores, the avatar ring).
  `--anvil` is dark iron that stays dark in every theme (the gate's anvil), for drawings that must read on light backgrounds too.
  `--rune-1`…`--rune-6` (ember to violet) and `--arc`/`--arc-2` (blue-violet) belong to the gate's rune sword; keep them for magic, not for UI state.
- Radii: 6px inputs, 8px buttons, 12px panels, 14px cards, 999px pills and chips.

## Page anatomy

- A page is `<section className="page">` (max 960px, centred). Its direct children rise in one after another (`page-in`,
  weightless via `:where(.page) > *`, `backwards` fill so no transform lingers). Don't add page-level entrance animations of your own.
- `h1` gets a short molten rule automatically (`.page h1::after`). Follow it with `<p className="lede">` when the page
  needs a sentence of context.
- A header with an action: `<div className="row team-head">` with the title block, `<span className="spacer" />`, then
  an `a.button-link` (see Team and Journey).
- Numbers at a glance: `<ul className="stats">` of `<li><strong>N</strong> label</li>` (each tile has a molten top line).
- Group each concern in a **panel**. Plain content: `<section className="panel" aria-labelledby="…">` with an `h2`.
  A form: `<form className="stack panel">` (fields get a readable max width; `h2 + p` is tightened).
- List beside detail: `.manage-grid` with `.manage-list` (links, `.active` gets the molten inset edge) and `.manage-detail`.
- Breakpoints in use: 900px, 760px, 640px. Nothing may scroll horizontally at phone width; wide tables go in `.table-wrap`.

## Components and patterns

| Pattern | How |
|---|---|
| Surface | `.panel`, `.card`, `.module`, `.question`, `.stats li`, `.toolbar`, `.result`, `.request`, `.rank-card`, `.journey-row` share the **heated edge**: a transparent border painted by `--edge` (warm at two corners). States set a solid `--edge` (`.module.complete`, `.question.right/.wrong`, `.panel.danger-zone`). New surfaces join that selector list rather than inventing a border. |
| Card grid | `.cards` of `.card`; hover lifts and heats the edge. A whole-card link: `.card.card-link` with the `h2` link stretched over it; inner links stay clickable. An "add" tile: `button.card.new-card` with `<span aria-hidden>+</span> Start a team`, which opens its form below. |
| Buttons | `button.primary` is the one main action: molten churn plus a hammer strike with sparks (`lib/sparks.ts`). One per form or panel. `button.ghost` for secondary, `button.danger` for destructive fills, `.danger-text` for outline-only destructive actions in a `.panel.danger-zone`. `a.button-link` for navigation that looks like a button. `.small` for compact row actions. |
| Forms | `.stack` (grid of `label`s wrapping their control). Filters: `.toolbar` with uppercase captions (`label.check` for a checkbox inside it); `.toolbar.fit` to shrink to its controls. Checkboxes take `accent-color`. Focus is an accent ring. |
| Segmented choice | `.segmented` with `button[aria-pressed]` (Journey's By person / By training). Keep state in the URL with `useSearchParams` when a view should be shareable. |
| Lists of people | `.chips` of `.chip` (with an × button to remove); `.role-row` for label + chips rows. |
| Facts | `dl.facts` for label/value pairs; `.badge` for status and small tags (`.complete`, `.in_progress`, `.warn`, edit statuses). |
| Tables | `table.grid` (quiet uppercase headers, rows light up on hover), `td.actions` right-aligned. |
| Progress | `MoltenBar` (flowing metal; respects calm), `meter` for budgets (molten → `--accent-2` past `low` → `--danger` at `high`). |
| Empty state | `<p className="muted empty">No labs yet.</p>`: a dashed hearth with the ⚒ mark. Say what will appear and, if possible, how to get it. |
| Disclosure | `details.lab-settings` with a `summary` in the accent colour (secondary settings, roster editing). |
| Menus and popovers | Follow `AdminMenu` / `UserMenu`: button with `aria-expanded`, close on outside `mousedown` and on Escape (focus returns to the button), `role="menu"`/`"dialog"`, absolutely positioned under the button, `card-cast` entrance. |
| Feedback | Saves: `toast('…')` from `lib/alerts`; a 409 shows `<Conflict onReload … />` via `reportSaveError`. Loading: `<Loader label="Gathering the smiths…" />` (forge loader with quotes). Errors: `<ErrorBox error={…} />`. Destructive actions use `window.confirm` with a sentence that names what goes and what stays. |
| Identity | `Avatar` (an icon from `lib/avatars.ts` drawn as poured metal, or initials) in a turning crucible ring. New icons: add to `AVATARS` and to `auth.Avatars` in Go (a test checks they match). |
| Heat | Journey heat cells: glyph + text carry the meaning (`·` cold, `◐` glowing, `●` forged); colour only reinforces it. |
| Ambient | `Embers` and `SparkBurst` for moments (Hearth, a passed check); never as constant decoration on working pages. |
| Sign-in gate | `ForgeGate` (full screen, no nav) is shown when `whoAmI()` finds nobody signed in; logout lands on it. Its one orchestrated moment is one of two animations of the six ranks, or none, picked by the switch under the button (remembered in `localStorage`; None is `.gate.plain`, a `1fr auto 1fr` grid that holds the title group at the centre of the screen over the embers): `Forging`, an SVG hammer striking ore on an anvil (the hammer's keyframe impact at 70% must equal `IMPACT / CYCLE`), or `RuneSword`, a horizontal runic sword whose six engraved runes light one per rank, each in its own colour (`--rune-1`…`--rune-6`, passed to each rune as `--c`), and which stays enchanted in `--arc`/`--arc-2` once all six are lit (stage classes `s0`–`s5`, prefix `rs-`, durations `RUNE_STEP`; it stops on the masterwork rather than looping). Both share `Defs` and `RankTrack` (`HammerShape` is the anvil's hammer); stages and timings live in `lib/forging.ts`. Calm shows the masterwork at rest. |

## Motion

- One orchestrated moment beats scattered effects. Durations: 0.15–0.25s for hover/focus, 0.22–0.4s for entrances,
  6–9s for ambient loops (the molten churn, `bar-flow`, motes).
- Loops must be seamless: a gradient that rolls must repeat at the distance it moves (see `bar-flow`, `molten-flow`).
- To animate a custom property (an angle, a radius), register it with `@property` (see `--pour`, `--strike-r`).
- CSS transforms on SVG elements scale and rotate around the drawing's top-left corner unless told otherwise: set
  `transform-box: fill-box; transform-origin: center` (or `view-box` with an explicit origin, like the gate's hammer pivot).
- A gleam or sheen is light on a surface, never a shape on top of it: clip a soft gradient band to the surface's own
  path and move the band (the masterwork's sheen, `forge-blade-clip`), then mark the end with a small accent (its tip twinkle).
- Fast motion leaves a smear on its own path: faint copies of the moving part run the same animation, each delayed a
  little more, visible only while it moves fast (the gate hammer's `forge-echo`). Never a separate shape that fades in and out.
- A multi-stage scene puts its stage on the drawing as a class (`.rune-sword.s3`): plain rules say where everything rests in
  that stage, so calm motion still shows the right picture, and `.sN` entrance animations carry each part there.
- Prefer `transform`/`opacity`. Don't leave a `transform` filled on a container that may hold `position: fixed`
  descendants (modals): use `backwards` fill or none.
- Things that move independently (the user card's motes) get their own elements and timings; a shared layer moves together.
- An orbit (the rune sword's motes): an outer group swings `translateX` and the dot inside swings `translateY` (plus
  scale and opacity for depth), both `alternate` with the same duration, a quarter lap apart; spread the starting
  delays round the lap so they never bunch.
- Lightning that runs round something: a closed jagged path (`bolt(seed, cx, cy, rx, ry)` in `lib/forging.ts`) with `pathLength="100"`, a
  dash pattern that divides 100, and `stroke-dashoffset` animated by -100, so the loop has no seam; flicker opacity on top.
- A `both`-filled animation with a delay holds its first keyframe during the delay: start a flash or a ring at
  opacity 0, or it shows early.

## Accessibility

- Every control has a label (visible, or `aria-label`); icons and decoration are `aria-hidden`; status text uses
  `role="status"`, errors `role="alert"`; progress uses `role="progressbar"` with values.
- `:focus-visible` shows a 3px `--accent-2` outline; never remove it without an equal replacement.
- Never use colour alone for meaning (heat glyphs, badge text, strike-through for done).
- Keyboard: menus close on Escape and return focus; `Ctrl+Alt+↑` leaves a terminal.

## Process for a UI task

1. Read the closest existing page and this file. Reuse a pattern before adding CSS; add CSS to the matching section of
   `app.css` with a one-line comment that says what it is for.
2. Build with tokens; add tests for any logic (`web/src/lib/*.test.ts`) and a render test for the page
   (`renderToStaticMarkup`, mocking `../useFetch` and `../me` as the existing `*.test.tsx` files do).
3. Look at it. Run the dev server against the running stack and screenshot it with Playwright:
   `cd web && npx vite --port 5199 --strictPort` (it proxies `/api` and `/auth` to `localhost:8080`), sign in on
   `localhost:8080` in the same browser context, then open `localhost:5199/…`. Check all four themes
   (`document.documentElement.setAttribute('data-theme', t)`), phone width, and Calm forge.
4. Run `npm test`, `npx tsc -b`, `npm run lint`, `npm run build` in `web/`; for capability changes also
   `go test ./internal/docs` and the relevant e2e journey.
5. If you changed or added a pattern, update this skill.
