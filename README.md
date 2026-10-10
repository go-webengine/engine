<p align="center"><img src="https://raw.githubusercontent.com/go-webengine/brand/main/social/go-webengine.png" alt="go-webengine/engine" width="720"></p>

# go-webengine / engine

[![CI](https://github.com/go-webengine/engine/actions/workflows/ci.yml/badge.svg)](https://github.com/go-webengine/engine/actions/workflows/ci.yml)
![coverage gate](https://img.shields.io/badge/coverage%20gate-css%2099.5%20%C2%B7%20layout%2Fpaint%2Fpaginate%20100%20%C2%B7%20dom%2097.4-brightgreen)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-webengine/engine.svg)](https://pkg.go.dev/github.com/go-webengine/engine)
[![Docs](https://img.shields.io/badge/docs-mkdocs--material-0079A8)](https://go-webengine.github.io/docs/)
[![License: BSD-3-Clause](https://img.shields.io/badge/License-BSD--3--Clause-blue.svg)](LICENSE)
[![Go 1.27.1+](https://img.shields.io/badge/Go-1.27.1%2B-00ADD8?logo=go)](https://go.dev/dl/)

A pure-Go, **`CGO_ENABLED=0`** headless web engine: it fetches a URL, parses the
HTML into a DOM, applies a real CSS subset (cascade + inheritance + `var()` +
modern colour + dark-mode + gradients), **runs the page's JavaScript** against a
real DOM binding, lays the content out with a full box model (block, inline,
float, flexbox, CSS grid, tables and `position`), and paints anti-aliased text,
backgrounds, gradients, box-shadows, images and **SVG** to an `image.RGBA` — **no
Chromium, no cgo, no host web view**. Give it a URL, get back an image of the
page.

It is the rendering core of the
[`browserproxy`](https://github.com/go-webengine/browserproxy) remote-browser
service and the [wasmdesk](https://github.com/wasmdesk) in-desktop browser: a
server renders pages and streams frames (plus a click hit-map) to a thin client.

## Quickstart

```go
import (
    "context"
    "image"
    "os"

    "github.com/go-webengine/engine"
)

func main() {
    ctx := context.Background()

    // Render a live page to PNG bytes.
    png, err := engine.Screenshot(ctx, "https://example.com/", image.Rect(0, 0, 1024, 768))
    if err != nil {
        panic(err)
    }
    _ = os.WriteFile("out.png", png, 0o644)

    // Or get the raw image plus metadata (title, final URL, full content height).
    img, info, err := engine.Render(ctx, "https://example.com/", image.Rect(0, 0, 1024, 768))
    _ = img
    _ = info // info.Title, info.URL, info.ContentHeight
    _ = err
}
```

For more control use an `*engine.Engine` (from `engine.New()`): it adds
`RenderHTML` (render a local HTML string offline), `RenderWithLinks` /
`RenderDocumentWithLinks` (image **plus** a `[]Link` hit-map for click-to-navigate),
and a `DisableJS` field to render the static, no-JavaScript document.

The viewport width is fixed; the height grows to fit the whole page (at least the
viewport height). There is also a CLI:

```
go run ./cmd/render -url https://example.com/ -out out.png -w 1024 -h 768
# or render a local file offline:
go run ./cmd/render -file page.html -base https://example.com/ -out out.png
```

## Pipeline

```
Fetch (go-browserhttp)  →  Parse (x/net/html → dom)  →  Cascade (css: +var()/@media/dark-mode)
     →  JavaScript (js: goja + real DOM + fetch/XHR)
     →  Layout: block · inline · float · flex · grid · table · position (layout)
     ⟲   settle loop: JS reads laid-out metrics → re-cascade + re-layout to a bounded fixpoint
     →  Paint: AA text/SVG/gradients/shadows (paint) + backgrounds (go-widgets/painter) + images (go-images)
     →  image.RGBA  →  PNG (+ optional link hit-map)
```

| Package | Role |
|---|---|
| `dom` | Owned DOM node tree built from `golang.org/x/net/html`. |
| `css` | Real CSS subset: value model, stylesheet/declaration parser, tag/class/id + descendant/child/sibling combinators + `:checked`/`:not()` selectors with specificity, `var()` custom properties, `@media` width queries, modern colour (`rgb()/hsl()`), dark-mode, UA stylesheet, cascade + inheritance. |
| `layout` | Full box model — block-and-inline flow, floats + clear, flexbox, CSS grid, tables, `position` (relative/absolute/fixed/sticky), margin collapsing, greedy word-wrap — driven by a `Measurer` interface (font-free, exactly testable). |
| `js` | JavaScript execution via [goja](https://github.com/dop251/goja) bound to a minimal real DOM, with `fetch()`/XHR and laid-out-geometry read-back (`getBoundingClientRect`, `offset*`, `getComputedStyle`). |
| `paint` | Rasterises the box tree to `*image.RGBA` — AA text (real bold + italic), gradients, border-radius, box-shadow, opacity, images and SVG; also the real `Measurer` (go-opentype faces). |
| `engine` (root) | `Fetch`, `Render`, `Screenshot`, `RenderHTML`, `RenderWithLinks`, the settle-then-render loop, image + SVG loading, and the anchor hit-map. |
| `cmd/render` | CLI: `render -url URL -out shot.png -w 1024 -h 768` (or `-file page.html`). |

Everything reused is pure-Go and BSD/MIT — see [`SURVEY.md`](SURVEY.md) for the
prior-art verdict (opossum/mycel studied, not built on) and the full
reuse-vs-build decision.

## What works / What doesn't

The full per-feature and per-page assessment (ten live pages, committed golden
PNGs, measured vs headless Chrome) is in [`FIDELITY.md`](FIDELITY.md) and
[`bench/REPORT.md`](bench/REPORT.md). Short version:

**Works today**

- **HTML → DOM → full box-model layout** at a real viewport width: block/inline
  flow, floats + clear, **flexbox**, **CSS grid**, **tables**, **`position`**
  (relative/absolute/fixed/sticky), margin collapsing, greedy word-wrap.
- **CSS cascade** with specificity (inline > id > class > tag) and inheritance;
  `var()` custom properties; `@media` width queries; **dark-mode**
  (`prefers-color-scheme`); external `<link>` stylesheet fetch; UA defaults.
- **Selectors**: tag/class/id/compound, descendant + child + **sibling (`~`/`+`)
  combinators**, **`:checked`** and **`:not()`** (the checkbox-hack that collapses
  MediaWiki dropdowns), attribute selectors handled by a "reduce, don't drop" rule.
- **Colour & decoration**: named/`#rgb`/`#rrggbb`, modern `rgb()`/`hsl()`,
  `background-color`, **linear & radial gradients**, `background-image: url()`,
  **border** + **border-radius**, **box-shadow**, group **opacity**.
- **Transforms and effects**: `translate` and `rotate` (the function and the
  standalone property), CSS `filter` (blur and colour-matrix functions),
  `backdrop-filter`, `mask-image` (a single `url()` mask), group `opacity`.
- **Lists and columns**: `ul`/`ol` markers (`list-style` discs, circles and
  squares, `start`/`value`), and multi-column layout for `columns`,
  `column-count` and `column-width`.
- **Text**: anti-aliased proportional text (go-opentype) with **real bold and
  italic faces** (no faux-bold), serif / sans / mono, complex scripts (Cyrillic,
  Vietnamese, …); `white-space: pre` and `nowrap`; `text-transform`
  (`uppercase`/`lowercase`/`capitalize`, applied before measurement);
  `letter-spacing`, `word-break`/`overflow-wrap`, `text-overflow: ellipsis`,
  `-webkit-line-clamp`;
  **`@font-face`** — the document's own typefaces are fetched and registered
  before anything is measured, **WOFF2 included**, so a page is set in the
  face it asked for and not in a substitute at other metrics.
- **Tables**: automatic table layout (CSS 2.1 §17.5.2.2) — every column keeps
  at least its longest unbreakable word, percentage and fixed cell widths are
  honoured, surplus goes to the auto columns; `colspan`, `border-spacing`,
  `vertical-align` on cells (with the UA `middle` default and the `valign`
  attribute), and a row's own CSS `height` as its minimum.
- **Form controls**: inputs and buttons with UA chrome, and checkboxes/radios
  honouring `appearance: none` (custom toggle switches).
- **Containers and shadow DOM**: `@container` size queries, and declarative
  shadow DOM (`<template shadowrootmode>`) with `<slot>` distribution.
- **Images**: `<img>` over http(s) + `data:` (PNG/JPEG) and **SVG**
  (oksvg/rasterx) via `<img *.svg>`, `data:image/svg+xml` and **inline `<svg>`**.
- **JavaScript**: page scripts run via [goja](https://github.com/dop251/goja)
  against a real DOM, with `fetch()`/XHR and read-back of real laid-out geometry
  (`getBoundingClientRect`, `offsetWidth/Height`, `getComputedStyle`). A
  **settle-then-render loop** re-cascades and re-lays-out after scripts mutate the
  DOM (incl. dynamically injected `<script>`/`<style>`/`<link>`), to a bounded
  fixpoint — so `mw.loader`-style runtime chrome is reflected in the output. The
  same JS-settled DOM drives the click hit-map.

**Honest limits (not overclaimed)**

- `conic-gradient` is recognised but not painted. `filter` and `mask-image` are
  modelled narrowly (see above): a mask is a single `url()` stretched over the
  box, with no `mask-size`/`mask-position`/`mask-repeat`. Transforms other than
  translate and rotate (`scale`, `skew`, `matrix`) are not supported.
  SVG has no `<filter>`/`<mask>`/`<pattern>`/embedded `<image>`/`<text>`, and a
  per-page image budget caps very icon-heavy pages.
- `::before`/`::after` generated content is not synthesised, so icon-font and
  `visually-hidden` chrome can render as text where a browser shows an icon.
- `vertical-align` on a table cell models `top`, `middle` and `bottom`; the
  `baseline` alignment across a row is approximated as top.
- Rate-limited image hosts (Wikimedia's CDN, measured round 154) answer some
  requests with HTTP 429. The engine retries them, and the renders are
  pixel-identical, but each retry costs about 2 seconds of wall time on that page
  (measured round 155).
- Several pages render **slower** than Chrome (go.dev/blog, tailwindcss.com,
  pkg.go.dev): an open performance gap, not a fidelity one. The measured causes so
  far are the per-host request cap and the time spent on rate-limited responses.
- This is **not** a standards-complete browser and **not** "as good as Chromium".
  Measured mean windowed-SSIM across the ten bench pages is **≈ 0.66**, with
  clear diminishing returns; the Wikipedia number (≈ 0.42) is JS-confounded and
  noisy. See the numbers below.

## Measured fidelity vs headless Chrome

From [`bench/REPORT.md`](bench/REPORT.md): windowed SSIM over the common top-left
region at 1024px width (1.0 = identical), pixel-diff %, and `speed×` =
`chrome_ms / webengine_ms` (>1 = webengine faster). Single run against live pages,
so the content of several pages moves between runs (see the notes in FIDELITY.md).

| URL | SSIM | pixdiff % | speed× | note |
|-----|-----:|----------:|-------:|:-----|
| example.com/ | **0.831** | 5.6 | 18.3 | near-parity; its JS cross-fade is not modelled |
| en.wikipedia.org/wiki/Go | 0.417 | 30.3 | 1.6 | live article; JS and rate-limited thumbnails |
| pkg.go.dev/net/http | 0.716 | 11.3 | 0.8 | large computed page |
| go.dev/blog/ | 0.700 | 16.6 | 0.45 | slower than Chrome |
| react.dev/ | 0.725 | 33.0 | 1.3 | SPA; hydration fails, so the static fallback renders |
| news.ycombinator.com/ | 0.615 | 13.7 | 1.75 | table layout; rotating front page |
| developer.mozilla.org/…/CSS | 0.606 | 18.1 | 3.3 | docs layout |
| github.com/golang/go | 0.637 | 14.2 | 2.0 | live repository counters |
| tailwindcss.com/ | 0.724 | 12.2 | 0.5 | sponsor carousel rotates; slower |
| caniuse.com/ | 0.660 | 18.0 | 0.8 | data grid |

Mean SSIM over the ten pages is **≈ 0.66**. Summed over the run, webengine took
26.1 s and headless Chrome 26.3 s, so the overall time is at parity, but the
per-page spread is wide: example.com and the static docs pages are much faster,
while go.dev, tailwindcss.com and the large pkg.go.dev page are slower. Re-run the
harness with `cd bench && go run ./cmd/compare -urls urls.txt` (needs a
Chrome/Chromium binary).

### Larger corpora

Ten pages is a small, curated sample. Two larger, less-curated corpora —
[`bench/urls-100.txt`](bench/urls-100.txt) (100 pages, 100 distinct sites) and
[`bench/urls-500.txt`](bench/urls-500.txt) (500 pages across 268 sites, built
from each site's own sitemap, ≤4 pages/site) — exist specifically to surface
what a small hand-picked sample won't: round 166's and round 167's crash
fixes (an unrecoverable stack overflow from a script-built DOM tree past 512
levels, and a CSS Grid panic on `grid-row:1 / -1` with no explicit row
template) were both found this way, on `smashingmagazine.com` and `lego.com`
respectively — real pages no ten-page sample happened to include. Full
results: [`bench/REPORT-100.md`](bench/REPORT-100.md),
[`bench/REPORT-500.md`](bench/REPORT-500.md).

Mean windowed-SSIM is **≈ 0.53** on both (100-page median 0.55, 500-page
median 0.56) — lower than the curated ten-page set, which is expected: this
sample includes large, JS-heavy, highly dynamic sites (news homepages,
cloud-vendor marketing pages) the ten-page set was never meant to represent,
not a regression. 95 of 500 pages (19%) failed to render in BOTH webengine
and headless Chrome — dead URLs, DNS failures and timeouts, not an engine
gap; exactly one page failed in webengine alone (a transient DNS flake on the
measuring machine). Round 168 found and fixed a real, separate performance
bug this way too: several of the 500 pages' own origins (large organizations'
own CDNs/WAFs, confirmed via `www.cs.cmu.edu`) rendered 10-20× slower than
Chrome, traced to a font-face loading path that tried several candidate font
files in series instead of concurrently (see FIDELITY.md's own round-168
entry) — fixed, but the 500-page numbers above predate that fix and have not
yet been re-measured against it.

## Security

The engine fetches every resource a page names (images, stylesheets, fonts,
scripts and modules, `fetch`/XHR targets, form posts). It does **not** filter
destinations itself, so rendering an untrusted page from a machine with access to
private networks can reach internal addresses. If you embed the engine directly,
add a dial-time guard through `Engine.Client` that refuses loopback, private and
link-local addresses. `browserproxy` ships one (`guard.go`). Decoding is
bounded: raster images declaring more than 25 megapixels are refused, and every
fetch is size-capped.

## Test

```
CGO_ENABLED=0 go build ./...          # cgo-free build
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test -short ./...     # -short skips the live-network render
bash scripts/coverage-gate.sh          # ratchet coverage gate (see below)
```

The pure logic (cascade/inheritance, line-breaker, box metrics, DOM, selector
engine) is asserted at exact geometry; committed golden PNGs — including offline
JS/dynamic/gradient/position/SVG fixtures with a `DisableJS` control — cover the
paint path. `scripts/coverage-gate.sh` enforces a **ratchet** coverage floor per
pure-logic package (`css`/`layout`/`paint`/`dom`/`paginate`: css 99.5%, layout,
paint and paginate 100%, dom 97.4%), which CI fails below and which is raised
(never lowered) toward 100% as the engine matures. The live-network
paths (root `engine` package, `cmd/render`) are excluded from the gate because
their coverage is not reproducible in CI. The `bench/` fidelity harness is a
separate nested module (it pulls chromedp) and is not in the CGO=0 six-arch CI.
`go.mod` floor is `go 1.27.1`; cross-built for all six 64-bit Go targets.

## Links

- Landing: <https://go-webengine.github.io/>
- Documentation: <https://go-webengine.github.io/docs/>
- Fidelity report: [`FIDELITY.md`](FIDELITY.md) · Benchmark: [`bench/REPORT.md`](bench/REPORT.md) · Prior-art survey: [`SURVEY.md`](SURVEY.md)
- Remote-browser service: [`browserproxy`](https://github.com/go-webengine/browserproxy)

## License

BSD-3-Clause — see [`LICENSE`](LICENSE). Copyright (c) the go-webengine/engine
authors.
