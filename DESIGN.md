# Quartermaster design system

The visual system of the web UI in `ui-svelte/`. The source of truth is `ui-svelte/src/index.css`
(tokens and shared classes). This file says what those tokens are FOR and which rules are
deliberate, so a change can be judged against intent instead of against generic taste.
Implementation gotchas (zoom, `h-screen`, popups) live in `ui-svelte/CLAUDE.md`.

## What the product is

A local inference engine with two faces, one bundle:

- **Operator dashboard**: a dense instrument panel for one person running models on their own GPU.
  Model catalog, load/unload, per-model tuning, live VRAM / throughput / logs. Read at a glance,
  often while something else is happening.
- **Playground**: a per-user studio (chat, image, video, 3D, speech, transcription). Calmer, more
  space around the content, same tokens and components.

Both run in a browser tab and in a frameless native window (WebView2), at a user-chosen interface
scale, in dark and light. Often on a LAN box with no internet: nothing may fetch at runtime (fonts
are bundled).

## Principles

1. **Density over air, on the dashboard.** This is an operator tool, not a landing page. Small type,
   tight gaps, many readouts per screen. Do not "open up" a dashboard view by adding padding or
   splitting it into more cards.
2. **Data is mono, everything else is sans.** The type split carries meaning (see Typography).
3. **Colour means state, never decoration.** The accent means "active / your attention". Status
   colours mean status. Category hues identify model kinds. Nothing gets colour just to look lively.
4. **Stable layout.** Nothing pops in, fades out or leaves a hole. Readouts render from the first
   frame with a `-` placeholder and their label; widths are reserved (`min-w-[Nch]`). Signal state
   with colour on the value, not opacity on the group. Panels that switch content keep a fixed
   height so the page never jumps.
5. **Labels stay.** A number without its label is unreadable. Never shed labels at narrow widths or
   hide them behind hover.
6. **Flat.** Hairline borders separate things; resting shadows do not. Shadows are only for things
   that float (popups, menus, modals).
7. **One component per job.** A shared class exists for buttons, segmented controls, chips, tiles,
   cards, popups. Use it; do not hand-roll a lookalike with utilities.

## Themes

Two themes, switched by `data-theme` on the root. Neither is a derived copy of the other.

| | Dark (default feel) | Light ("paper") |
|---|---|---|
| Ground | warm near-black `#22201e` | parchment `rgb(240 230 214)` |
| Accent (`--color-primary`) | flame orange `rgb(255 106 43)` | roasted brown `rgb(120 72 40)` |
| Text on accent | near-white | near-white |

Both are **warm**. No cool greys, no pure `#000` / `#fff` surfaces. The flame/helm identity comes from
the brand mark (orange helm on dark) and the fire-field activity strip.

## Colour tokens

Always use tokens (Tailwind `bg-surface`, `text-txtsecondary`, `border-card-border`, ...), never
Tailwind palette colours (`red-500`, `gray-900`): tokens are re-tuned per theme, the palette is not.

**Surfaces, darkest to lightest (dark theme):**

| Token | Use |
|---|---|
| `background` | the page |
| `rail` | the status rail: one step BELOW surface, a readout strip, not a card |
| `surface` / `chrome` | cards, inputs, the title bar and side rails (same tone: chrome reads as one raised plane with the page recessed into it) |
| `surface-2` | a well nested INSIDE a card (`.well`, a `<pre>` in a card). Always raised above its parent, never sunk below it |

**Text:** `txtmain` for content, `txtsecondary` for labels, units, secondary lines.

**Interactive:** `primary` / `-hover` / `-active` (filled accent); `secondary` / `-hover` / `-active`
(neutral tinted fill for hover and pressed states); `focus-ring`.

**Borders:** `card-border` (large panels, very faint), `card-border-inner` (rules inside a card),
`border` (hover / emphasis), `btn-border`, `control-border` (16px ticks and radios, which a panel
border would lose), `composer-border` / `composer-ring`.

**Status:** `success` (ready / green), `warning` (starting, stopping, queued / amber), `error`
(stopped, destructive / red), `info`.

**Model categories:** `--color-cat-llm` (gold), `-image` (violet), `-video` (teal), `-3d` (blue),
`-other` (grey). Five hues is the ceiling. Chat models are gold, deliberately NOT the orange accent:
LLMs are most of any catalog, and orange there would paint the whole page as a call to action.

**Special:** `speak` / `speak-bg` (read-aloud position), `cite-bg` / `cite-text` (citation chips),
`dot` (dot-grid texture).

## Typography

- **Sans (Inter)**: chrome and prose. Headings, labels, buttons, nav, body copy. The body default.
- **Mono (JetBrains Mono)**: DATA. Model ids, quants, GB / token counts, flags, log lines, launch
  commands: anything the operator reads as a value. Opt in with `font-mono` at the call site. Numbers
  that update live also get `tabular-nums`.

Scale, dense-tool sized:

| Token | Size | Use |
|---|---|---|
| `text-micro` | 11px | the floor. Uppercase labels, badges, segmented controls, tile sub-lines. Nothing smaller |
| `text-label` | 12px | section labels (`h6`), nav links |
| `text-xs` / `text-sm` | 12 / 14px | the working sizes for most UI |
| `text-lg` | 18px | a tile's big value |
| `h1`...`h4` | 2xl / xl / lg / base, semibold, tight tracking | page and panel titles; rare |

`h6` IS the section label: small, uppercase, wide tracking, secondary colour. Headings carry no
built-in margin; pages space them with flex `gap`. Uppercase + `tracking-wide` is reserved for
labels, badges and small command buttons, never body text.

## Spacing and shape

- Spacing is Tailwind's scale, used tightly: `gap-1` / `gap-2` everywhere, `gap-1.5` / `gap-3` for
  groups, `p-3` / `p-4` for card bodies.
- **`h-10` (40px) is the row unit.** A sidebar item, the status rail and each page's first toolbar
  row share it, so chrome and content sit on one horizontal grid. Toolbars that can wrap use
  `min-h-10`.
- Radius: `rounded-md` is the default (cards, buttons, inputs, popups). `rounded-lg` for modals and
  the chrome's inner corner. `rounded-full` for chips, pills, dots, sliders. The composer is the one
  large-radius element (`rounded-3xl`).
- **One hairline L separates chrome from page.** The rule sits on the elements below-right of the
  chrome (`rounded-tl-lg border-t border-l`), never on the title bar or the rail itself, so it curves
  into the corner. Tone-only separation was tried and rejected.

## Components (shared classes in `index.css`)

**Buttons: `.btn` plus modifiers.** Never hand-rolled hover or case utilities.

| Class | When |
|---|---|
| `.btn` | the plain button. Hover lifts border and label to the accent |
| `.btn--primary` | the one main action in a view (filled) |
| `.btn--primary-outline` | the one affirmative verb in a row of plain buttons, where filled would be too loud |
| `.btn--ghost` | low emphasis in a busy row; borderless until hovered |
| `.btn--quiet` | per-row actions in a long list: muted, neutral hover, so N rows do not light up N accents |
| `.btn--sm` | small command button; always uppercase |
| `.btn--icon` | glyph-only, keeps the border to align with text buttons |
| `.btn--danger` | filled red: ONLY the confirm half of an irreversible pair |
| `.btn--danger-outline` | a destructive action reachable from everywhere (Unload all) |
| `.btn--danger-hover` | plain until hovered: removing one row's item |

Destructive weight scales with how irreversible the action is. Variant hovers are written
`:hover:not(:disabled)`.

**Other controls:**

- `.seg`: segmented control, mutually exclusive options, selected child carries `aria-pressed`.
- `.chip-toggle`: independent multi-select pill; mono, normal case (its labels are flag names).
- `.icon-btn`: icon-only toolbar affordance, borderless.
- `Toggle.svelte`: every boolean setting. Checkboxes only for pick-N-of-M lists.
- `Select.svelte` / `Combobox.svelte`: the only dropdowns. They share the `.qm-popup*` chrome, so a
  closed list and a free-text-with-suggestions field cannot drift into looking like two widgets.
  Never a native `<datalist>` (ignores theme and zoom).
- Ticks, radios and range sliders are fully restyled in `index.css`; native `<select>` chrome is
  stripped.

**Containers and readouts:**

- `.card`: flat surface, hairline border, no resting shadow; border strengthens on hover.
- `.well`: raised nested block inside a card.
- `.tile`: one KPI. Uppercase micro label, big mono value, optional mono sub-line.
- `.status--{ready,starting,stopping,queued,stopped}`: tinted status badge.
- `table` (gridded) vs `.data-table` (grid-free, for long scannable catalogues: rows told apart by
  opt-in hairlines `tr.rule` or alternating bands, because a border on every cell is the
  spreadsheet tell and competes with the vertical rules that carry meaning).

**Icons:** `lucide-svelte`, stroke icons, sized to the text beside them.

## Motion

- Short and functional: colour transitions ~120-200ms, nothing bouncy.
- **Live-state feedback** (reasoning, generating, loading, searching): `.reason-shimmer` sweeps a
  brighter band across the label; `.reason-glow` pulses the leading icon; `.thinking-dots` grows
  `.` `..` `...` in a fixed-width box so the word never shifts.
- The **fire field** (dashboard activity strip) is the one expressive animation: its intensity is
  driven by real load / prefill progress and tok/s, not decoration.
- Every animation honours `prefers-reduced-motion` (stopped or slowed).

## Accessibility and platform

- Visible `:focus-visible` ring on every control (`--color-focus-ring`), hidden for pointer users.
- `text-micro` (11px) is the minimum size.
- Interface scale is CSS `zoom` on `:root`. Viewport units are always divided by `--qm-scale`, and
  anything positioned from a `getBoundingClientRect()` goes through `lib/uiZoom.ts`.
- No `-webkit-font-smoothing` override (it thins every glyph on macOS).

## Writing (UI copy and docs)

- No em dashes. Use a colon, comma or parentheses.
- Labels are nouns, buttons are verbs. Small command buttons are uppercase via `.btn--sm`, not typed
  in caps.
- Say what the number is and its unit; prefer `-` over an empty cell or a spinner for "no data yet".

## Anti-patterns (reject these)

- Palette colours instead of tokens (`bg-gray-900`, `text-red-500`).
- Resting drop shadows on cards; glassmorphism; gradients as decoration.
- Cool grey or pure black/white surfaces; any non-warm neutral.
- Using the orange accent for categories, emphasis or decoration: it means "active".
- Cards nested in cards to group things; use a `.well` or a rule.
- Status-chip soup: one badge per row at most, and only for real state.
- Labels that disappear at small widths, readouts that mount late, `invisible` placeholders
  leaving holes.
- A custom class named after a Tailwind utility (`collapse`, `hidden`, `grid`, ...).
- A new dropdown, button or toggle style when a shared class exists.
- Mono for prose, or sans for values.

## Known deviations (debt, not precedent)

- `MetadataTooltip.svelte` uses `bg-gray-900 text-white`, a palette colour, in both themes.
- `.chat-prose code` names its own mono stack instead of `--font-mono`.
- Some modals and menus mix `rounded-lg` / `rounded-xl`; `rounded-md` / `rounded-lg` is the intent.
