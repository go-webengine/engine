# Fidelity Report

**Date: 2026-08-04**

Honest assessment of the renderer on five pages. Phase 0 shipped a static
HTML→CSS→block/inline→paint→PNG pipeline that **linearised every page to one
column**. Phase 1 adds the real positioning layer — box model completeness,
vertical margin collapsing, floats + clear, basic flexbox, basic tables, painted
borders, `@media` width queries, and selector combinators. This is still a
**static** renderer with **no JavaScript**; anything a page paints via scripting
is absent, and that is stated below, not hidden.

The committed PNGs under `testdata/renders/` back every claim here. Reproduce
them with the commands at the bottom.

## Phase 1.7 — flexbox completeness + CSS grid

The bench harness identified CSS **flex/grid layout** (not JavaScript) as the
dominant remaining fidelity gap on modern Tailwind-driven pages. Phase 1.7
closes most of it. All of the following land with **exact-geometry unit tests**
(`layout` and `paint` stay at **100%** statement coverage; `css` at 99.7%).

### Flexbox — now complete enough for real Tailwind layouts
- **`flex-wrap`** (`wrap` / `wrap-reverse`) breaking items into multiple flex
  lines; **`flex-flow`** shorthand.
- **`gap` / `row-gap` / `column-gap`** between items (main axis) and lines
  (cross axis), including the two-value `gap` shorthand.
- **`align-content`** (start / end / center / space-between / around / evenly)
  distributing lines across a definite cross size.
- **`align-self`** overriding `align-items` per item; **`order`** reordering
  items within the container.
- **`min/max-width`** and **`min/max-height`** resolved within flex (clamping
  grow/shrink on the main axis and stretch on the cross axis).
- Nested flex containers, percentage bases against the container, `flex:1` /
  `flex:0 0 200px` shorthands.

### CSS grid (`display:grid`) — new
- **`grid-template-columns` / `grid-template-rows`** with `px`, `%`, **`fr`**,
  `auto`, **`repeat(n, …)`** and **`minmax(min, max)`** (incl. `minmax(_, 1fr)`).
- **`gap` / `row-gap` / `column-gap`** between tracks.
- **Explicit placement**: `grid-column` / `grid-row` with line numbers
  (positive and negative), `/`-ranges and **`span N`**; `grid-area` line form.
- **Auto-placement**: row-major flow for items without explicit placement, plus
  fixed-column-auto-row and fixed-row-auto-column items.
- **`grid-template-areas`** with `grid-area: <name>` placement.
- **`justify-items` / `align-items` / `place-items`** and per-item
  **`justify-self` / `align-self` / `place-self`** (stretch fills the cell,
  otherwise the item keeps its size and is positioned within the cell);
  **`justify-content`** distributes/centres the whole track band.
- `grid-auto-rows` / `grid-auto-columns` for implicit tracks; a lone `auto`
  column stretches to fill the container (matching browsers).

### Deliberately out of scope this phase (→ Phase 1.8, not faked)
- `repeat(auto-fill / auto-fit, …)`, `fit-content()`, `subgrid`, named grid
  **lines** (named **areas** *are* supported), and `masonry` — parsed values
  that reference these leave the property unset rather than guessing.
- `grid-auto-flow: dense` and `column` flow are parsed but auto-placement is
  always sparse **row-major** at this fidelity.
- Intrinsic (`auto`/`min/max-content`) track sizing only counts items that lie
  **within a single track**; a multi-track spanner does not grow the tracks it
  crosses (fixed/`fr`/`%` tracks and explicit sizes are exact).
- `fr` **rows** distribute space only when the container has a **definite
  height**; otherwise they behave as `auto` (content-sized).
- Cross-axis `min/max-height` and grid `min/max` bounds are treated as
  **border-box** limits (box-sizing is not distinguished on that axis).
- Baseline alignment, and `justify-content` spacing distribution on grid columns
  when `auto` tracks are present (auto tracks stretch instead).

## What Phase 1 added (all with exact-geometry unit tests)

- **Box model**: `max-width`/`min-width` clamping, `width:auto` + `margin:auto`
  centring (CSS 10.3.3), `box-sizing:border-box`, `line-height` (px/em/%/unitless
  and `normal`), painted `border` (shorthand + per-side + width/style/colour),
  `height`.
- **Vertical margin collapsing**: adjacent siblings collapse to the larger
  margin; a parent with no top/bottom border or padding collapses through its
  first/last child; negative margins follow the max-positive-plus-min-negative
  rule.
- **Floats**: `float:left`/`right` with text flowing around them (line boxes
  shorten over a float's vertical band), float stacking when there is no room
  beside, and `clear:left`/`right`/`both`.
- **Flexbox**: `display:flex` with `flex-direction` row/column,
  `justify-content` (start/end/center/space-between/around/evenly), `align-items`
  (stretch/start/end/center) and `flex-grow`/`shrink`/`basis`.
- **Tables**: `display:table`/`<table>` auto layout — column widths from cell
  max-content scaled to the table width, row heights from the tallest cell,
  `thead`/`tbody`, `th`/`td`.
- **CSS breadth**: descendant / child (`>`) / adjacent (`+`) / general-sibling
  (`~`) combinators with full specificity; `@media (min-width/max-width)` width
  queries evaluated against the render width (so desktop rules apply); `vw`/`vh`
  (approximated as a percentage of the containing width) and `rem` units;
  `background-color` and `border` on any element.

## Per-page before → after

### 1. `https://example.com/` — `example.com.png`
- **Before (Phase 0):** the page flowed full-width to the left; no centred
  column, because `margin:auto`, `max-width` and viewport units were unsupported.
- **After (Phase 1):** example.com's current CSS centres the **body** with
  `width:60vw; margin:15vh auto`. Both `vw` and `margin:… auto` now resolve, so
  the content sits in a centred ~614 px column (`60vw` of 1024), matching the real
  page's centred layout. *(example.com changed its markup since Phase 0: the
  centred element is the body via `60vw`, not a fixed 600 px div — both paths are
  now handled.)*
- **Remaining:** the browser's `system-ui` font is substituted by the bundled
  sans; `border-radius`/`box-shadow` on the panel are not painted.

### 2. `https://en.wikipedia.org/wiki/Go_(programming_language)` — `wikipedia_go.png`
- **Before (Phase 0):** the right-hand infobox **stacked above** the article
  prose as a full-width block; the whole page linearised.
- **After (Phase 1):** the infobox now **floats to the right and the article
  prose flows down its left side** — Paradigm / Designed by / Developer / First
  appeared / Typing discipline / License etc. sit in a 22 em right column while
  the lead and *History* paragraphs wrap beside it. This works because the
  infobox's `float:right; width:22em` lives in an in-document `<style>` behind
  `@media (min-width:640px)`, which the new media-query evaluation now applies.
  The gopher mascot figure also floats within the prose. Verified in the
  committed PNG around y≈4600.
- **Remaining:** the top **navigation sidebar, TOC and 63-language list still
  linearise** — that chrome is collapsed/flex/grid- and JS-driven and its rules
  live in *external* stylesheets the engine does not fetch (see Known gaps).
  There is minor text overlap at the very top edge of the infobox where the first
  lead line was placed before the float registered. All text content is present
  and legible.

### 3. `https://www.rfc-editor.org/rfc/rfc1866.html` — `rfc1866.png`
- **Before / After:** essentially unchanged, and correctly so — the RFC is one
  large `<pre>` block with no floats/flex/tables to place. `white-space:pre` is
  honoured: the right-aligned author header, centred title, dot-leader table of
  contents and indentation all line up, and inline `<a>` cross-references render
  in link-blue. This confirms the new positioning layer did not regress the
  inline/`pre` path.

### 4. `testdata/floats_demo.html` — `floats_demo.png` *(new, float-heavy)*
A self-contained fixture proving the float + box-model work crisply and
deterministically. It shows: a `max-width` page **centred** with painted
left/right borders; a **right-floated info panel** (bordered, background-filled)
with prose wrapping down its left; a **left-floated figure** with text wrapping to
its right; margins between paragraphs **collapsing** to a single gap; `h2`
underline via `border-bottom`; and a **cleared footer** sitting below both floats.

### 5. `testdata/flex_table_demo.html` — `flex_table_demo.png` *(new, flex + table)*
A self-contained fixture proving flex and tables. It shows: a **flex navbar**
(`justify-content:space-between`, `align-items:center`) with the brand at the
left, nav links centred and a call-to-action button at the right; a **flex
two-column** body where a fixed-width sidebar (`flex-shrink:0`) sits beside a
`flex-grow:1` main panel; and a **data table** whose columns are sized from
content, with a styled header row, per-cell borders and right-aligned numeric
columns.

## Known gaps (unchanged scope boundaries)

- **No JavaScript** — SPA / script-rendered content is blank or skeletal
  (Phase 2 / goja).
- **No external stylesheets** — only in-document `<style>` and inline `style=`
  are applied. Layout rules that live in a site's external CSS (e.g. Wikipedia's
  top nav/sidebar collapse) are therefore not honoured, so that chrome
  linearises. In-document `@media` rules *are* now applied. (`css.CascadeVW`
  already accepts external sheets; wiring the fetch is a Phase-2 follow-up.)
- **No absolute/fixed/sticky positioning, `overflow`, transforms,
  `border-radius`, `box-shadow`, gradients, web fonts.** Percentage and viewport
  heights, and `vh`, are approximated. Table `colspan`/`rowspan` and
  `border-collapse` are not modelled. CSS grid is now supported (Phase 1.7); its
  deliberately-unsupported sub-features are listed in the Phase 1.7 section above.
- **Fonts**: only bundled Inter/Lora/Go-Mono (regular); bold is faux-bold and
  italic is not rendered.

## Reproduce

```
go run ./cmd/render -url https://example.com/ -out testdata/renders/example.com.png -w 1024 -h 768
go run ./cmd/render -url "https://en.wikipedia.org/wiki/Go_(programming_language)" -out testdata/renders/wikipedia_go.png -w 1024 -h 768 -timeout 90s
go run ./cmd/render -url "https://www.rfc-editor.org/rfc/rfc1866.html" -out testdata/renders/rfc1866.png -w 1024 -h 768 -timeout 90s
go run ./cmd/render -file testdata/floats_demo.html -out testdata/renders/floats_demo.png -w 900 -h 700
go run ./cmd/render -file testdata/flex_table_demo.html -out testdata/renders/flex_table_demo.png -w 1000 -h 700
go run ./cmd/render -file testdata/flex_wrap_demo.html -out testdata/renders/flex_wrap_demo.png -w 900 -h 700
go run ./cmd/render -file testdata/grid_demo.html -out testdata/renders/grid_demo.png -w 1000 -h 950
go run ./cmd/render -file testdata/tailwind_hero_demo.html -out testdata/renders/tailwind_hero_demo.png -w 1024 -h 620
```

The three live URLs were reachable from this environment; no offline substitution
was needed. The two demo fixtures render fully offline via `-file`.
