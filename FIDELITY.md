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
- **No CSS grid, absolute/fixed/sticky positioning, `overflow`, transforms,
  `border-radius`, `box-shadow`, gradients, web fonts.** Percentage and viewport
  heights, and `vh`, are approximated. Table `colspan`/`rowspan` and
  `border-collapse` are not modelled.
- **Fonts**: only bundled Inter/Lora/Go-Mono (regular); bold is faux-bold and
  italic is not rendered.

## Reproduce

```
go run ./cmd/render -url https://example.com/ -out testdata/renders/example.com.png -w 1024 -h 768
go run ./cmd/render -url "https://en.wikipedia.org/wiki/Go_(programming_language)" -out testdata/renders/wikipedia_go.png -w 1024 -h 768 -timeout 90s
go run ./cmd/render -url "https://www.rfc-editor.org/rfc/rfc1866.html" -out testdata/renders/rfc1866.png -w 1024 -h 768 -timeout 90s
go run ./cmd/render -file testdata/floats_demo.html -out testdata/renders/floats_demo.png -w 900 -h 700
go run ./cmd/render -file testdata/flex_table_demo.html -out testdata/renders/flex_table_demo.png -w 1000 -h 700
```

The three live URLs were reachable from this environment; no offline substitution
was needed. The two demo fixtures render fully offline via `-file`.
