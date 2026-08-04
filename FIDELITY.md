# Phase-0 Fidelity Report

**Date: 2026-08-04**

Honest assessment of the Phase-0 **static** renderer on three live pages,
rendered at viewport width 1024 (height grows to fit the full page). The PNGs
are committed under `testdata/renders/`. This is a static renderer with **no
JavaScript**; anything a page paints via scripting is absent, and that is
expected and stated below, not hidden.

## Summary of what works vs what does not

**Works today (verified in the committed PNGs):**
- HTML parse → DOM → block/inline flow at a real viewport width.
- UA default styling for common tags (headings sized + bold, `<p>` margins,
  link colour, `<pre>` monospace, list/blockquote indents).
- Author CSS from `<style>` and inline `style=`, with cascade, specificity
  (inline > id > class > tag) and inheritance (color, font-size, weight,
  family, text-align, white-space).
- Colours: named, `#rgb`, `#rrggbb`, `rgb()/rgba()`; `background-color` boxes;
  the page backdrop (body/html background) extended over the whole viewport.
- Anti-aliased proportional text via go-opentype at the cascaded size, with
  correct advances driving greedy word-wrap line-breaking; faux-bold for weight
  ≥ 600; serif / sans / mono generic families.
- Complex-script shaping through go-opentype (Cyrillic, Vietnamese, etc. render
  legibly in the Wikipedia capture).
- `white-space: pre` preserves spaces and newlines (fixed-column `<pre>`).
- `<img>` best-effort: http(s) and `data:` sources, PNG/JPEG decode, downscale
  to viewport width, drawn inline.

**Not supported in Phase 0 (by design):**
- **No JavaScript** — SPA / script-rendered content is blank or skeletal.
- **No float / flex / grid / table layout, no absolute/fixed positioning** —
  every page linearises to a single column; multi-column chrome (sidebars,
  nav bars, infoboxes) stacks vertically.
- **No `max-width`/`min-width`, `line-height`, `border`, `box-shadow`,
  `letter-spacing`, backgrounds images/gradients, `@media`, web fonts.**
- Selectors: tag/class/id compound only — **no combinators** (descendant/child
  reduce to their key selector), no attribute/pseudo selectors.
- Italic is not rendered (no italic face); `<em>`/`<i>` render upright.
- Margin collapsing is not implemented; whitespace between inline element
  boundaries is approximated (a space is inserted between adjacent inline
  runs).

## Per-page findings

### 1. `https://example.com/` — `testdata/renders/example.com.png`
**Good.** Heading "Example Domain" (bold), the paragraph, and the blue "More
information..." link all render with correct colours on the light-grey page
backdrop (now filling the viewport).
**Wrong/missing.** The real page centres a ~600px column via `max-width` +
`margin: auto`; Phase 0 supports neither `max-width` nor auto-centring, so the
text flows to the full viewport width instead of a centred narrow column. Purely
a layout-width difference; all content is present and correctly styled.

### 2. `https://en.wikipedia.org/wiki/Go_(programming_language)` — `testdata/renders/wikipedia_go.png`
**Good.** This is the strongest demonstration of the content path: the article
title (serif bold), the full "Contents" table-of-contents, navigation link
lists, and the 63-language list all render, with links in link-blue and
multilingual text shaped correctly. Rendered full-page height ≈ 20 000 px.
**Wrong/missing.** Wikipedia's chrome is float/flex/grid- and JS-driven, so it
**linearises**: the left sidebar, the collapsible TOC, the top nav bar and the
right-hand infobox all stack as vertical single-column blocks rather than in
their real positions. Icons/logos that are CSS backgrounds or SVG are absent
(only `<img>` PNG/JPEG draw). No article thumbnails that rely on `srcset`/lazy
`data-src` load. This is the expected shape of a static render of a modern CMS
page: **all the text content is there and legible; the visual chrome layout is
not.**

### 3. `https://www.rfc-editor.org/rfc/rfc1866.html` — `testdata/renders/rfc1866.png`
**Good.** A plain-HTML spec served as one big `<pre>` block. With
`white-space: pre` honoured, the monospace column layout is preserved: the
right-aligned author header, the centred title, the dot-leader table of
contents, and indentation all line up, and inline `<a>` cross-references
(e.g. RFC 1590 / RFC 1521) render in link-blue. Full-page height ≈ 81 000 px.
**Wrong/missing.** `<title>` is empty in the info line because this document
has none; there is no author CSS beyond defaults, so it is essentially the UA
stylesheet exercising the `<pre>` path. No pagination/columnisation (the RFC's
"[Page N]" markers are literal text, as in the source).

## Reproduce

```
go run ./cmd/render -url https://example.com/ -out testdata/renders/example.com.png -w 1024 -h 768
go run ./cmd/render -url "https://en.wikipedia.org/wiki/Go_(programming_language)" -out testdata/renders/wikipedia_go.png -w 1024 -h 768 -timeout 90s
go run ./cmd/render -url "https://www.rfc-editor.org/rfc/rfc1866.html" -out testdata/renders/rfc1866.png -w 1024 -h 768 -timeout 90s
```

All three URLs were reachable from this environment; no offline substitution was
needed.
