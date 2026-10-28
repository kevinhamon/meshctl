# Mesh console design language

The console is one page, `index.html`, with plain CSS and JavaScript: no framework, no build step,
nothing loaded from the network. This file is the reference for how it looks. The stylesheet
opens with the tokens below; everything after them uses the tokens, and
`TestStylesheet_UsesTheDesignTokens` fails on a raw font size, spacing or radius outside the
token block (`0`, `auto`, `1px`, `2px` and `50%` are allowed).

## Tokens

| Group | Tokens | Use |
|---|---|---|
| Type | `--fs-xs` 11 · `--fs-sm` 12 · `--fs-md` 13 · `--fs-lg` 15 · `--fs-xl` 20 | xs badges and pills; sm meta, labels, ids; md body and lists (the base); lg names; xl page titles and stat numbers |
| Weight | 400, `--fw-b` 600 | bold for names, labels, numbers |
| Mono | `--mono` | ids, hashes, paths, raw text |
| Space | `--s1` 4 · `--s2` 8 · `--s3` 12 · `--s4` 16 · `--s5` 24 · `--s6` 32 | s2–s3 inside components, s3 between them, s4–s5 page gutters |
| Radius | `--r-sm` 6 · `--r-md` 10 · `--r-pill` | sm buttons, chips, menu rows; md cards, panels, dialogs; pill badges and counts |
| Surface | `--bg` · `--surface` · `--surface-2` · `--hover` · `--line` | page; cards and panels; sidebars and footer; hover; borders |
| Text | `--text` · `--muted` · `--faint` | content; secondary; hints and empty states |
| Accent | `--accent` · `--accent-bg` | links, say chips, focus, primary button |
| Status | `--red` `--amber` `--green` and their `-bg` | needs you / errors; waiting / warnings; clear / live |
| Depth | `--shadow` · `--shadow-pop` | raised surfaces; menus (none for surfaces in dark mode) |
| Focus | `--focus` | the one focus ring |
| Motion | `--t-fast` 120ms | hover and expand; 0 under `prefers-reduced-motion` |

Light and dark are the same tokens with different values (`prefers-color-scheme`).

## Components

- **Frame** (both layouts): a header (the Agent Mesh mark and name with the version beneath,
  linking Home; the live state; the gear for Settings), a breadcrumb bar under it (Home › pool ›
  agent › section, each part a link), the centre, and the footer. The footer belongs to the pool
  on screen: "Pool:" and the pool selector, its path, other pools that need attention, the pool's
  stats (each a filter for the agents shown), doctor, and the pool's actions (Re-check, Repair,
  Upgrade). Home has no footer. The window never scrolls; the sidebars and the centre do, each
  on its own.
- **Layouts**: Classic shows the pool as a grid of agent cards; Workspace adds the agent list on
  the left and shows the pool overview in the centre. Both share the frame and the agent page.
- **Sidebar** (`.ws-left`, `.sections`): a head row (uppercase name, and a « » icon button that
  collapses it from its own edge) over a scrolling list. The left collapses to a rail of
  initials; the right to a thin strip. Narrow windows: the left opens as an overlay from ☰ in
  the breadcrumb bar, the right becomes a scrolling row of cards above the content.
- **Agent card**: full in the Classic grid (stripe in the agent's badge colour, shown name, folder
  name, status badge, up to three pending rows, say chips and pills); compact in the Workspace
  list (`.acard`: shown name, status, one line of activity; the folder name on hover).
- **Agent names**: an agent is shown by its manifest title (`displayName`), with its folder name
  (`.slug`, monospace, small) beneath or on hover; an agent without a title shows its folder
  name. Rail initials come from the shown name.
- **Agent page**: a content area and the sections sidebar of **section cards** (`.scard`). A
  section card is compact but rich: an uppercase title, a row of inline stats, a line or two of
  detail (`sl()`). Clicking one renders that section in the content area; it never expands in
  place. The section is part of the address (`#/pool/<key>/agent/<name>/<section>`); without
  one, the Overview shows the agent's stat blocks and what needs the person now. A section is
  `{ key, title, card(a), body(a, into) }` in `sections`.
- **Home** (`#/`): totals across every pool, then a pool card per pool (state, agents, needs
  attention, waiting, sessions, doctor, and Needs upgrade with an Upgrade button).
- **Settings** (`#/settings`): rows of a label, a line of help and a control (segmented control
  or checkbox); saved in the pool repo of the pool on screen.
- **Panel** (`.panel`): a titled surface in the content area: a head (title and inline stats)
  and a body.
- **Inline stat** (`.st`, `st(n, label, cls)`): a bold number and a label; `red`, `amber`,
  `green` or `quiet` by state. Section summaries are rows of these.
- **Stat** (`.stat`): the block form, a large number over a label (Home, agent Overview).
- **Badge** (`.status`): a state word. **Pill** (`.pill`): a count. **Say chip** (`.say`): a
  phrase to say, or a schedule in plain words (the cron expression on hover).
- **List rows**: `.line` (a pending kind with its items), `.doc` (a document: kind tag, name,
  age), `.hist` (a commit or closed request), `.finding`, `.session`, `.job`. Rows are separated
  by a top border, never boxed.
- **Button**: default; `.primary` for the one action a view is for (Repair, Upgrade); `.quiet`
  for secondary tools; `.icon-btn` for an icon alone (always with an `aria-label` and a title).
  **Select** (`button.select`): a bordered button with a status dot, the value and a down
  chevron, opening a menu. **Segmented control** (`.seg`): a choice of two or three.
- **Icon** (`icon(name)`, `.icon`): drawn here as SVG paths on a 24-unit grid, stroked in the
  text colour, 18px (the gear 20px). The set: `menu`, `down`, `up`, `collapseLeft`,
  `collapseRight`, the gear and the Agent Mesh mark (both inline in the page). A new icon is
  added to `iconPaths` the same way; no icon fonts, libraries or images.
- **Notice** (`.notice`): a pool-level message, amber edge.
- **Dialog**: head (title, path, actions), body; the viewer, item, repair and upgrade dialogs.
  An action that changes a pool shows what it will do first and what it did after.

## Rules

- Every value from the pool is set as text, never as HTML.
- A number shows with its unit word ("3 intake"), coloured only when it asks for attention.
- Summaries show numbers; detail and controls are one click away, in the content area, not in
  a dialog. Dialogs are for reading a file or confirming an action.
- A control looks like what it does: a selector has a visible chevron, an icon button has a
  label for screen readers and a tooltip, and nothing relies on a glyph a few pixels wide.
- The page redraws on every update, so any state the person sets lives somewhere it survives:
  the address (pool, agent, section), the pool's `console.yaml` (layout, agent order, the
  attention filter), or the browser (open sidebars); the footer filter lives in a variable.
