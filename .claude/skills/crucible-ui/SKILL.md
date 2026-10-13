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
| Theme tokens, eight themes | `web/src/theme/tokens.css` (dark: `forge`, `quench`, `damascus`, `verdigris`, `starmetal`; light: `anvil`, `parchment`; `contrast`) |
| All component styles | `web/src/theme/app.css` (plain CSS, no framework; the last section, *The forge finish*, is the global polish layer) |
| Fonts, self-hosted | `web/src/theme/fonts.css`, `web/src/theme/fonts/` (Cinzel, Inter, JetBrains Mono) |
| Theme list for Settings | `web/src/theme/theme.ts` (`THEMES`; `light: true` marks a light theme for the editor). A new theme is a token block with every token, an entry here and in Go's `config.Themes` (a test checks they match); the contrast test and the editor pick it up from `THEMES`. |
| Shared components | `web/src/components/` (Loader, ErrorBox, Conflict, MoltenBar, Embers, SparkBurst, Avatar, UserMenu, HeatMap, Toaster, Timer …) |
| Small UI logic, testable | `web/src/lib/` (pure functions with `*.test.ts`) |

## Hard rules

1. **Colours come from tokens only.** `--bg --surface --surface-2 --text --muted --border --accent --accent-2 --ok
   --danger --on-accent --glow --spark`, plus terminal and diff tokens. Never write a hex, `rgb()` or `hsl()` in a
   `color:` or `background:` declaration in `app.css`: `web/src/lib/contrast.test.ts` fails the build. Derive shades with
   `color-mix(in srgb, var(--accent) 30%, var(--border))`. A new colour need means a new token in every theme.
2. **Every theme, every time.** Check at least one dark theme (Forge), both light ones (Anvil, Parchment) and High Contrast.
   Text pairs are contrast-checked for every theme in `THEMES` in `contrast.test.ts`; High Contrast must meet WCAG AAA. Add a pair there when you introduce a new text/background combination.
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
- A grid column that holds a terminal, an editor or anything that sizes itself is `minmax(0, 1fr)`, never a bare `1fr`
  (which can't shrink below its content and spills off the page). Something with `hidden` leaves the grid, so change
  the column template with it (the lab's Hide tasks).
- Breakpoints in use: 900px, 760px, 640px. Nothing may scroll horizontally at phone width; wide tables go in `.table-wrap`;
  a `p.warn` (a lab's blocked reason, which may quote a terraform error to editors) wraps anywhere.

## Components and patterns

| Pattern | How |
|---|---|
| Surface | `.panel`, `.card`, `.module`, `.question`, `.stats li`, `.toolbar`, `.result`, `.request`, `.rank-card`, `.journey-row` share the **heated edge**: a transparent border painted by `--edge` (warm at two corners). States set a solid `--edge` (`.module.complete`, `.question.right/.wrong`, `.panel.danger-zone`). New surfaces join that selector list rather than inventing a border. |
| Card grid | `.cards` of `.card`; hover lifts and heats the edge. A whole-card link: `.card.card-link` with the `h2` link stretched over it; inner links stay clickable. An "add" tile: `button.card.new-card` with `<span aria-hidden>+</span> Start a team`, which opens its form below. |
| Buttons | `button.primary` is the one main action: molten churn plus a hammer strike with sparks (`lib/sparks.ts`). One per form or panel. `button.ghost` for secondary, `button.danger` for destructive fills, `.danger-text` for outline-only destructive actions in a `.panel.danger-zone`. `a.button-link` for navigation that looks like a button. `.small` for compact row actions. |
| Forms | `.stack` (grid of `label`s wrapping their control). Filters: `.toolbar` with uppercase captions (`label.check` for a checkbox inside it); `.toolbar.fit` to shrink to its controls. Checkboxes take `accent-color`. Focus is an accent ring. |
| Decision | Approve/reject on one item (an edit's review): `form.stack.panel` with an `h2` from the user's side (*Your review*), a muted sentence saying what each choice does, the note `textarea`, then `.row` with `button.primary` (submits) and `button.ghost` for the other choices. A decided note reads as `.hint` naming who wrote it. |
| Pick and set | One change to one thing from a known set (a team member's tag, a role holder): `form.row.retag` (or `.row.enroll` inside a role row) with a caption `span.role-name`, an `input` whose `list` is a `datalist` of the candidates (each `option` labelled with who it is: tag, team, or what a group means), a `select` for the value, then `button.primary`. Check the pick in a pure `lib/` function first (`retag`, `checkRef`) and show its reason in `[role=alert].error`. |
| People and groups | Role holders are emails or groups (`all_seniors`, `platform`): chips, with a group as `.chip.group` (accent edge, a ⚑ mark, `title` saying who it is via `describeGroup`). Say how many people the groups are now beside the list. |
| Choose in place | A value someone may change right where it shows (a person's tag): a `button.tag-pill` with `aria-haspopup="menu"`, `aria-expanded` and a label that says what it is and that it changes it; it opens `.tag-menu[role=menu]` of `menuitemradio`s (`aria-checked`, a ✓), as the menu pattern: outside `mousedown` and Escape close it, focus returns to the button, arrow keys move, the current value takes focus. Saves at once with a toast. Read-only viewers get a plain `span.tag-pill`. Tag colours are edge and tint only (`.tag-leader/-senior/-junior`); the word carries the meaning. |
| Filterable list | A list people search and slice (the team's people): `.toolbar` with **Find** (`type=search`), a value filter, **Sort by**, and a `.segmented` List / By-group switch, all kept in the URL (`lib/people.ts`: `readQuery`/`writeQuery`, a pure `peopleView` with tests). Show "N of M" when filtered and a `.muted.empty` line when nothing matches. Grouped: a `section` per group with an `h3` and its count. |
| Segmented choice | `.segmented` with `button[aria-pressed]` (Journey's By person / By training). Keep state in the URL with `useSearchParams` when a view should be shareable. |
| Lists of people | `.chips` of `.chip` (with an × button to remove); `.role-row` for label + chips rows. |
| Pick before start | A choice a lab needs before it starts (a cluster lab's context): a `select` labelled from the user's side (*Run it on*), each option naming what it is and where (`kind-dev · 127.0.0.1:52011`), the last pick remembered per browser and preselected only if still offered; a risky variant asks with a `label.check` consent box, and the start button stays disabled until both are given. With nothing to pick, a muted line says how to get something. The picker is `.stack.cluster-pick` (centred, `min(100%, 32rem)`, single `minmax(0, 1fr)` column so a long name never widens a phone). |
| Live status list | Several named things and their state (a lab's Helm releases): `section.panel` with an `h5`, one `li` each with the name in `strong` and a `.badge` in words (*Ready*, *Starting*, *Failing*), then what is wrong in `code`, never colour alone. The badge class comes from `releaseBadge` (`.complete`, `.in_progress`, `.failing` in `--danger`); the words from `releaseWords`. Elsewhere (a Journey row) the same thing is one line: `ul.cluster-labs`, *Lab running in Module on `context`* · name + badge per release. |
| Facts | `dl.facts` for label/value pairs; `.badge` for status and small tags (`.complete`, `.in_progress`, `.warn`, `.failing`, edit statuses). |
| Tables | `table.grid` (quiet uppercase headers, rows light up on hover), `td.actions` right-aligned. When the same table repeats once per group (the team's people By tag), give it `table-layout: fixed` with a width per column: left to itself each table sizes to its own rows and a column lands somewhere else in each one. Pair it with a `min-width` so narrow screens scroll the `.table-wrap` instead of breaking emails mid-word. |
| Cost breakdown table | How a price was reached, line by line (an aws lab's estimate on its lobby): `details.lab-settings.cost-breakdown` with a summary from the user's side (*How this estimate was worked out*), one muted sentence on where the prices come from, then `.table-wrap` > `table.grid` — the thing in `code` (wraps anywhere), its type, per-hour prices via `usdRate` (four decimals under a dollar), `—` for nothing, the server's note as plain words — and a `tfoot` total row. On phones the Type column is hidden, because the address always contains the type. Left-aligned and capped at 44rem even in the centred lobby; numbers use tabular figures. Spend columns say *Estimate*, *Metered*, *Billed* (never "actual"). A freshness line for the data behind the numbers (*Price data from <date>.*) is a `p[role=status]`, `.muted` when fresh, `.error` plus the words *Out of date: …* when stale; the failure detail only reaches admins. |
| Progress | `MoltenBar` (flowing metal; respects calm), `meter` for budgets (molten → `--accent-2` past `low` → `--danger` at `high`). |
| Empty state | `<p className="muted empty">No labs yet.</p>`: a dashed hearth with the ⚒ mark. Say what will appear and, if possible, how to get it. |
| Disclosure | `details.lab-settings` with a `summary` in the accent colour (secondary settings, roster editing). |
| Menus and popovers | Follow `AdminMenu` / `UserMenu` (at phone width the user card spans the page gutter, `left/right: 1rem`, anchored to the nav): button with `aria-expanded`, close on outside `mousedown` and on Escape (focus returns to the button), `role="menu"`/`"dialog"`, absolutely positioned under the button, `card-cast` entrance. |
| Feedback | Saves: `toast('…')` from `lib/alerts`; a 409 shows `<Conflict onReload … />` via `reportSaveError`. Loading: `<Loader label="Gathering the smiths…" />` (forge loader with quotes). Errors: `<ErrorBox error={…} />`. Destructive actions use `window.confirm` with a sentence that names what goes and what stays. |
| Identity | `Avatar` (an icon from `lib/avatars.ts` drawn as poured metal, or initials) in a turning crucible ring. New icons: add to `AVATARS` and to `auth.Avatars` in Go (a test checks they match). |
| Smith mark | `SmithMark` beside a name: a ⚒ glyph tinted by level from tokens (`.smith-mark.l0`–`.l5`) plus the title in text; never ordered, numbered or compared. On the Hearth's craft card it is the headline (`.rank-name .smith-mark`). |
| Experience meter | `ExperienceMeter`: a `MoltenBar` captioned "N / M to <next rank>" (static under calm); in `CraftCard` on the Hearth and under the name in the user card. Swords: `SwordList`, one line per training (`.sword-list`); a training's page shows `.sword-line`. Rank-ups (`RankUp`, kind smith or sword) are one non-blocking banner at a time inside `RankUpSlot`. |
| Heat | Journey heat cells: glyph + text carry the meaning (`·` cold, `◐` glowing, `●` forged); colour only reinforces it. |
| Ambient | `Embers` and `SparkBurst` for moments (Hearth, a passed check); never as constant decoration on working pages. |
| Sign-in gate | `ForgeGate` (full screen, no nav) is shown when `whoAmI()` finds nobody signed in; logout lands on it. The Crucible title holds the centre of the screen (`.gate-inner` is a `1fr auto 1fr` grid, the title group in the middle row), the way in sits under it, and `Embers` drift behind (none under calm motion). It wears the look last used in this browser (`rememberLook`/`lastLook` in `theme/theme.ts`; the account stays the source of truth), else the instance default. |

## Motion

- One orchestrated moment beats scattered effects. Durations: 0.15–0.25s for hover/focus, 0.22–0.4s for entrances,
  6–9s for ambient loops (the molten churn, `bar-flow`, motes).
- Loops must be seamless: a gradient that rolls must repeat at the distance it moves (see `bar-flow`, `molten-flow`).
- To animate a custom property (an angle, a radius), register it with `@property` (see `--pour`, `--strike-r`).
- CSS transforms on SVG elements scale and rotate around the drawing's top-left corner unless told otherwise: set
  `transform-box: fill-box; transform-origin: center` (or `view-box` with an explicit origin for a pivot).
- A gleam or sheen is light on a surface, never a shape on top of it: clip a soft gradient band to the surface's own
  path and move the band, then mark the end with a small accent (a twinkle).
- Fast motion leaves a smear on its own path: faint copies of the moving part run the same animation, each delayed a
  little more, visible only while it moves fast. Never a separate shape that fades in and out.
- A multi-stage scene puts its stage on the drawing as a class (`.scene.s3`): plain rules say where everything rests in
  that stage, so calm motion still shows the right picture, and `.sN` entrance animations carry each part there.
- Prefer `transform`/`opacity`. Don't leave a `transform` filled on a container that may hold `position: fixed`
  descendants (modals): use `backwards` fill or none.
- Things that move independently (the user card's motes) get their own elements and timings; a shared layer moves together.
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
   `localhost:8080` in the same browser context, then open `localhost:5199/…`. Check the themes
   (`document.documentElement.setAttribute('data-theme', t)`), phone width, and Calm forge.
4. Run `npm test`, `npx tsc -b`, `npm run lint`, `npm run build` in `web/`; for capability changes also
   `go test ./internal/docs` and the relevant e2e journey.
5. If you changed or added a pattern, update this skill.
