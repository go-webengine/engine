<p align="center"><img src="https://raw.githubusercontent.com/go-webengine/brand/main/social/go-webengine.png" alt="go-webengine/engine" width="720"></p>

# go-webengine / engine

[![CI](https://github.com/go-webengine/engine/actions/workflows/ci.yml/badge.svg)](https://github.com/go-webengine/engine/actions/workflows/ci.yml)
![coverage gate](https://img.shields.io/badge/coverage%20gate-css%2099.5%20%C2%B7%20layout%2Fpaint%2Fpaginate%20100%20%C2%B7%20dom%2097.4-brightgreen)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-webengine/engine.svg)](https://pkg.go.dev/github.com/go-webengine/engine)
[![Docs](https://img.shields.io/badge/docs-mkdocs--material-0079A8)](https://go-webengine.github.io/docs/)
[![License: BSD-3-Clause](https://img.shields.io/badge/License-BSD--3--Clause-blue.svg)](LICENSE)
[![Go 1.27.2+](https://img.shields.io/badge/Go-1.27.2%2B-00ADD8?logo=go)](https://go.dev/dl/)

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
viewport height).

### Vector output (PDF)

`LayoutHTML` / `LayoutDocument` run the same pipeline without painting and
return a `*PageLayout`. `Paint()` gives the image `RenderHTML` would;
`PaintVector(c)` draws the same boxes through a `paint.Canvas` instead — paths,
shadings, images placed in their boxes and positioned glyphs of the very faces
the layout measured with. The `pdfcanvas` package is that canvas for a
[go-pdfkit](https://github.com/go-pdfkit/pdfkit) page: text stays text
(subset-embedded Type0 fonts with a `/ToUnicode` map — selectable,
searchable), edges stay sharp at any zoom, and an image used on many pages is
embedded once.

```go
l, _ := eng.LayoutHTML(ctx, html, baseURL, image.Rect(0, 0, 1280, 720))
doc := pdfkit.New(pdfkit.Options{Compress: true})
page := doc.AddPage(pdfkit.NewPageSize(960, 540)) // a CSS px is 3/4 pt
stats := l.PaintVector(pdfcanvas.New(page, 0.75, pdfcanvas.NewFonts()))
```

**Tagged PDF** (after v0.13.2): `c.Tag(doc.Structure())` before
`PaintVector`, then `c.Finish()`, writes a tagged PDF that a screen reader can
read. `PaintVector` tells a `paint.StructuredCanvas` which DOM node each
drawing belongs to (`paint.Mark`). `pdfcanvas` builds the structure from the
DOM as a browser's accessibility tree does:
- headings, paragraphs, lists (`L`/`LI`/`Lbl`/`LBody`), tables (with each
  `TH`'s `Scope`), links, images and formulas, with their `alt`,
  `aria-label` and `lang`, in reading order;
- backgrounds, borders, generated text (`::before`/`::after`), `user-select:
  none` and `aria-hidden` content as artifacts.

`c.Element(link.Node)` ties a link annotation to its `Link` element. Pages
built this way pass veraPDF's PDF/UA-1 profile.

What has no vector form here — a subtree under `opacity` < 1, a `filter`,
`mask-image` or `transform: rotate()`, a blurred `box-shadow`, a gradient
whose opacity fades between two stops, a form control — is painted by the
raster painter as a bitmap of **that element only**, placed where it was, with
its text laid over it invisibly (text rendering mode 3) so that it can still
be selected; `stats.Rasterized` counts them. Laying out and painting are split
so that pages can be laid out concurrently and painted, in order, into one
document.

### Editable output (office decks)

`officecanvas` is the other `paint.Canvas` consumer: it *records* a page as
the objects an editable office slide is made of — filled shapes (rectangles,
rounded rectangles, rings as outlines with holes), gradient shapes (a CSS
gradient's constant-opacity pieces merged back into one gradient, transparent
across the gaps), one text box per line of words (runs split by face, size,
colour, link and letter-spacing, the gap between words a space), and pictures
cropped to their clip — plus the page background and the faces used (family
and style from the font's `name` table, embeddability from its OS/2 `fsType`;
`SFNT` reassembles the very font file the page was laid out with). A writer
maps the `Page` to DrawingML or ODF; go-slides does both. A slide cannot clip,
so a picture is cropped to its clip and a gradient takes its clip's outline;
`Shape.Render` draws a gradient as a picture for a format whose gradients are
poorer.

### Native adjustments and lighter builds

`Engine.Adjust` runs Go where a page's scripts run. It reads the layout through a
`*Geometry`, which answers what a script reads through CSSOM View:
`OffsetWidth`, `ClientWidth`, `Rect`, and getComputedStyle's `Computed`, laid out
again after each DOM change. It may change the DOM, for example with
`dom.Node.SetStyleProperty` as `el.style.prop = …` does. The render then settles as
it does after scripts. `JSParseFloat` and `JSNumberString` are JavaScript's
`parseFloat` and number-to-string, so a native port of a script computes the same
lengths. go-slides fits Marp's headings this way: a test checks that the native
fit and the script render the same pixels.

`Engine.Fetcher` replaces the HTTP client for every http(s) URL a render
needs: the page, its style sheets, images and fonts. It suits a host whose
platform fetches for it, such as a browser's `fetch` in WebAssembly.

Fonts are loaded on demand. Each bundled face (Inter, Lora, Go Mono, and DejaVu
Sans for the characters they lack) is parsed the first time a render draws it,
not all at once. A host's installed face can be lazy too:
`LazyFontFace(family, weight, italic, load)` fetches its file when its family is
first drawn, and passes it over if the fetch fails.

Three build tags leave out what a host does not need:

| tag | leaves out | what changes |
|---|---|---|
| `webengine_nojs` | goja, esbuild, `Engine.Open` / `LiveDocument` / form submission | no page script runs; `Adjust` still does |
| `webengine_nonet` | browserhttp (Chrome TLS fingerprint), the HTTP client; with `webengine_nojs` too, all of net/http | an http(s) URL goes to the host's `Engine.Fetcher`, or is refused with `ErrNoNetwork`; `data:` and `file:` (under `FileRoot`) load as usual |
| `webengine_fontsext` | the 13 bundled font files (6 MiB) | the host serves `paint.BundledFontNames()` (the bytes are `paint/bundledfonts`) and calls `paint.SetFontSource` before the first render |

`webengine_nojs` takes go-slides' WebAssembly export worker from 20.9 to 16.5
MiB gzipped (measured 2026-10-25). With `webengine_fontsext`, a deck that draws
only Inter Regular and Go Mono downloads those two files, not the other 11.

There is also a CLI:

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
| `js` | JavaScript execution via [goja](https://github.com/dop251/goja) bound to a minimal real DOM, with `fetch()`/XHR and laid-out-geometry read-back (`getBoundingClientRect`, `offset*`, `getComputedStyle`), plus WebAssembly via [wazero](https://github.com/tetratelabs/wazero). |
| `paint` | Rasterises the box tree to `*image.RGBA` — AA text (real bold + italic), gradients, border-radius, box-shadow, opacity, images and SVG; also the real `Measurer` (go-opentype faces). `PaintVector` draws the same tree through a vector `Canvas`. |
| `pdfcanvas` | A `paint.Canvas` drawing into a go-pdfkit page: vector PDF with real text. |
| `officecanvas` | A `paint.Canvas` recording a page as editable office objects (shapes, gradients, text lines, pictures, fonts) for PPTX/ODP writers. |
| `engine` (root) | `Fetch`, `Render`, `Screenshot`, `RenderHTML`, `RenderWithLinks`, `LayoutHTML` → `PageLayout` (paint as image or vectors), the settle-then-render loop, image + SVG loading, and the anchor hit-map. |
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
  `var()` custom properties; `calc()`; `@media` width queries; **dark-mode**
  (`prefers-color-scheme`); external `<link>` stylesheet fetch; UA defaults.
  **`font-size: clamp()`** (round 190) for fluid typography — `rem`/`vw`/`px`/
  `em`/`%` terms, resolved eagerly at cascade time (`min()`/`max()`, and
  `clamp()` on any other property, remain unimplemented — see below).
- **Selectors**: tag/class/id/compound, descendant + child + **sibling (`~`/`+`)
  combinators**, **`:checked`** and **`:not()`** (the checkbox-hack that collapses
  MediaWiki dropdowns), attribute selectors handled by a "reduce, don't drop" rule.
  **`::before`/`::after`** generate a real box for a `content` made of strings,
  `counter()` and `counters()` (the breadcrumb separator, the numbered heading),
  including their own `background-image`/`mask-image` (the icon-only separator
  idiom, e.g. MDN's breadcrumb chevron) — not yet on a form control, which is
  still laid out as an atomic box. CSS escapes (`"\2022\00a0"`, `.\31 0`) are
  decoded per CSS Syntax 3 in every string and identifier, selectors included.
- **Counters**: `counter-reset` (with `reversed()`), `counter-increment`,
  `counter-set`, their scoping and inheritance (CSS Lists 3 §4), and the
  implicit `list-item` counter shared with list markers.
- **Colour & decoration**: named/`#rgb`/`#rrggbb`, modern `rgb()`/`hsl()`,
  **`oklab()`/`oklch()`**, `color-mix()` (srgb/srgb-linear/oklab/oklch/hsl),
  `background-color`, **linear & radial gradients**, `background-image: url()`,
  **border** + **border-radius**, **box-shadow**, group **opacity**.
- **Transforms and effects**: `translate`, `rotate` and `scale` (each the
  function and the standalone property, one at a time — no mixing), CSS
  `filter` (blur and colour-matrix functions), `backdrop-filter`,
  `mask-image` (a single `url()` mask), group `opacity`.
- **Lists and columns**: `ul`/`ol` markers generated in layout as a
  `::marker` box, so every backend gets them as text (the raster canvas, the
  vector PDF, and a caller walking the layout tree such as go-pdfkit/html2pdf):
  `list-style-type` `disc`, `circle`, `square`, `decimal`,
  `decimal-leading-zero`, `lower-`/`upper-alpha` (`-latin`),
  `lower-`/`upper-roman` (decimal outside 1–3999), a `<string>` and `none`;
  `list-style-position` `outside` and `inside`; `::marker` colour, font and
  `content`; HTML `start`, `reversed`, `value` and `type`. Multi-column
  layout for `columns`, `column-count` and `column-width`.
- **Text**: anti-aliased proportional text (go-opentype) with **real bold and
  italic faces** (no faux-bold), serif / sans / mono, complex scripts (Cyrillic,
  Vietnamese, …); `white-space: pre` and `nowrap`; `text-transform`
  (`uppercase`/`lowercase`/`capitalize`, applied before measurement);
  `letter-spacing`, `word-break`/`overflow-wrap`, `text-overflow: ellipsis`,
  `-webkit-line-clamp`;
  **`@font-face`** — the document's own typefaces are fetched and registered
  before anything is measured, **WOFF2 included**, so a page is set in the
  face it asked for and not in a substitute at other metrics. A host can also
  install typefaces of its own with **`Engine.FontFaces`** (built with
  `ParseFontFace`), as a browser's system fonts; parsed fonts are shared
  across renders, so a page set in the same faces as the last one does not
  parse them again.
- **Tables**: automatic table layout (CSS 2.1 §17.5.2.2) — every column keeps
  at least its longest unbreakable word, percentage and fixed cell widths are
  honoured, surplus goes to the auto columns; `colspan`, `border-spacing`,
  `vertical-align` on cells (with the UA `middle` default and the `valign`
  attribute), and a row's own CSS `height` as its minimum.
- **Form controls**: inputs and buttons with UA chrome, and checkboxes/radios
  honouring `appearance: none` (custom toggle switches).
- **Containers and shadow DOM**: `@container` size queries, declarative
  shadow DOM (`<template shadowrootmode>`) with `<slot>` distribution, and
  (round 183) imperative `attachShadow()`/`customElements.define()` —
  upgrading an already-parsed element whose constructor attaches a shadow
  tree, the custom-element polyfill idiom confirmed live on
  developer.apple.com.
- **Images**: `<img>` over http(s) + `data:` (PNG/JPEG) and **SVG**
  ([go-gfx/oksvg](https://github.com/go-gfx/oksvg), a fork of srwiley/oksvg
  with `clipPath`, over rasterx) via `<img *.svg>`, `data:image/svg+xml` and **inline `<svg>`**.
- **JavaScript**: page scripts run via [goja](https://github.com/dop251/goja)
  against a real DOM, with `fetch()`/XHR and read-back of real laid-out geometry
  (`getBoundingClientRect`, `offsetWidth/Height`, `getComputedStyle`). A
  **settle-then-render loop** re-cascades and re-lays-out after scripts mutate the
  DOM (incl. dynamically injected `<script>`/`<style>`/`<link>`), to a bounded
  fixpoint — so `mw.loader`-style runtime chrome is reflected in the output. The
  same JS-settled DOM drives the click hit-map.
- **WebAssembly** (round 189): a page's own `WebAssembly.compile`/`instantiate`
  runs real `.wasm` modules via [wazero](https://github.com/tetratelabs/wazero)
  (pure Go, no CGO — its Interpreter engine covers every architecture this
  project cross-builds for; its Compiler engine speeds up amd64/arm64 where
  available). A module's own exported functions and linear memory
  (`instance.exports.memory.buffer`, a real zero-copy `ArrayBuffer`) are
  modelled, with function imports wired to real JS callbacks. Bounded like
  every other resource this engine budgets: a module-size cap, a
  memory-growth cap, and the same wall-clock deadline JS execution is already
  held to.

**Honest limits (not overclaimed)**

- `WebAssembly`: no WASI (`wasi_snapshot_preview1` — stdio, filesystem,
  clocks) is wired, so a module assuming a WASI host fails to instantiate
  with a LinkError naming the missing import. The synchronous
  `new WebAssembly.Module(...)`/`new WebAssembly.Instance(...)` constructor
  forms, Table and Global imports/exports, standalone `Memory`/`Table`
  objects (not tied to a module), and `compileStreaming`/`instantiateStreaming`
  are not implemented — no confirmed container needs any of them yet.
- `clamp()` is only resolved for `font-size`, which resolves `%`/`vw` EAGERLY
  at cascade time — a width-like property's own percentage still depends on
  a containing block only known at layout time, the same wall `min()`/`max()`
  (unimplemented anywhere) and `calc()` mixing a percentage with an absolute
  length already disclose above. A `clamp()` term mixing a real percentage
  with `vw`/`rem` in the SAME term is not attempted — no confirmed trigger
  uses it.
- `customElements.define()` only upgrades elements ALREADY in the document
  at the time it's called — a later `document.createElement(name)` or a
  node inserted afterward is not upgraded. There is no
  `observedAttributes`/`attributeChangedCallback`/`connectedCallback`/
  `disconnectedCallback`, and a constructor that itself calls
  `customElements.define()` reentrantly is not handled. None of this is
  needed by the confirmed trigger (round 183); it is the HTML spec's own
  materially larger construction-stack/reaction-queue machinery.
- `lab()`/`lch()` (CIE Lab, not Oklab) are not recognised — a different
  conversion from `oklab()`/`oklch()`, which this engine does support (round
  182). Neither supports the `none` component keyword (CSS Color 4 §4.4);
  such a value drops the whole declaration, same as any other unsupported
  value here.
- `conic-gradient` is recognised but not painted. `filter` and `mask-image` are
  modelled narrowly (see above): a mask is a single `url()` stretched over the
  box, with no `mask-size`/`mask-position`/`mask-repeat`. Transforms other than
  translate, rotate and scale (`skew`, `matrix`, any 3D function) are not
  supported, and the three supported ones never mix with each other, with
  filter/opacity/mask-image on the same element, or (for scale) with a
  negative, axis-flipping factor.
  SVG has no `<filter>`/`<mask>`/`<pattern>`/embedded `<image>`/`<text>`, and a
  per-page image budget caps very icon-heavy pages.
- `::before`/`::after` generated content is synthesised for strings,
  `counter()` and `counters()` (rounds 177, 187). `attr()`, `url()`, the quote
  keywords and the rest of the content grammar drop the declaration, same as
  any other unsupported value. `@counter-style` is not supported: an unknown
  counter-style name is decimal, and an unknown `list-style-type` keyword
  leaves the declaration unapplied. Counter values are 32-bit; an increment
  that would overflow is not applied (WPT's expectation; the spec text says
  clamp).
  A `background-image`/`mask-image` set on the pseudo-element is now fetched
  too (round 181), so an icon-only separator (e.g. MDN's breadcrumb chevron,
  a plain `<li>::after{content:"";mask-image:url(...)}`) renders — but only
  on an ordinary element: a form control's own `::before`/`::after` is still
  invisible, since a `<button>`/`<input>` is laid out as an atomic box
  regardless of what its generated content would be (rounds 55-56).
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
link-local addresses. `browserproxy` ships one (`guard.go`).

Since a renderer's whole job is turning arbitrary, untrusted page content into
output, several unrelated kinds of pathological input have been found and
capped (each confirmed live against a synthetic reproduction, not assumed):

- **Decoding**: raster images declaring more than 25 megapixels are refused
  before decode; every fetch is size-capped.
- **Tree-depth recursion**: a script-built DOM tree past 512 levels (bypassing
  the HTML parser's own 512-level parse limit via `createElement`+
  `appendChild`) used to crash the whole process with an unrecoverable Go
  stack overflow — not a panic, `recover()` cannot catch it — in the CSS
  cascade, layout, `cloneNode`/`isEqualNode`, and three CSS parsers
  (`calc()`, `:not()`/`:has()`/`:host()`, `:is()`/`:where()`/`:matches()`,
  the last of these via a shared output budget rather than a depth cap,
  since it explodes in alternative COUNT, not nesting depth). All are now
  capped at 512 (matching the parser's own bound) or a generous output
  budget.
- **Memory exhaustion**: a single `innerHTML` assignment, or a `cloneNode`
  call repeated in a tight loop, could previously allocate tens of
  gigabytes of real memory from a few lines of ordinary-looking script —
  no deep nesting or large string needed. `cloneNode` and the HTML→DOM
  conversion shared by `Parse`/`ParseFragment` (and so `innerHTML`/
  `outerHTML`/`insertAdjacentHTML`) now cap total node count per call; the
  rendered canvas itself is capped by total pixel count.
- **Unresponsive render, no malicious input required**: an ordinary,
  non-malicious page with several `background-repeat` sections (found live
  on a real production site, not a crafted page) drove the paint phase to
  roughly a billion per-pixel calls through `image.Image`'s `At()` — each
  one heap-allocating to box the returned `color.Color` and re-deriving a
  premultiplied value from straight storage — taking tens of seconds of
  CPU with the caller's `context.Context` deadline never consulted, because
  nothing in that per-pixel loop checked it. Fixed by normalising a
  background-image tile to a concrete `*image.NRGBA` once, before it is
  stamped, instead of once per stamped pixel.
- **SVG rasterisation cost scales with output pixel area** — a large,
  gradient-filled decorative SVG (a background pattern, a hero
  illustration) could previously cost several seconds regardless of how
  modest its on-page footprint looked, with no ceiling as that area grows.
  `svgToBitmap` (the screen-render path) now rasterises above a pixel-area
  budget at a reduced resolution and upscales to the requested size, the
  same way a mismatched-size background image or `<img>` already does.
  The separately exported `RasterizeSVG` (used by the PDF/office vector
  export, which needs the exact size asked for at print density) is
  deliberately NOT capped.
- **WebAssembly** (round 189) is arbitrary bytecode execution from an
  untrusted page, audited as its own category: a module-size cap bounds
  `compile()`'s input, a memory-growth cap bounds a hostile module's own
  `memory.grow` loop, and every call is deadlined to the SAME wall-clock
  budget JS execution is already held to — a runaway module cannot outrun
  the page's own time budget any more than an infinite JS loop can. No WASI
  is wired, so a module cannot reach the filesystem, the network, or a
  clock through it.

See `FIDELITY.md`'s rounds 166–178 for the full measured writeups. None of
these change how a well-formed page renders — only how a pathological one
degrades (truncated output, never a crash or unbounded allocation).

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
paint and paginate 100%, dom 98.5%), which CI fails below and which is raised
(never lowered) toward 100% as the engine matures. The live-network
paths (root `engine` package, `cmd/render`) are excluded from the gate because
their coverage is not reproducible in CI. The `bench/` fidelity harness is a
separate nested module (it pulls chromedp) and is not in the CGO=0 six-arch CI.
`go.mod` floor is `go 1.27.2`; cross-built for all six 64-bit Go targets.

## Links

- Landing: <https://go-webengine.github.io/>
- Documentation: <https://go-webengine.github.io/docs/>
- Fidelity report: [`FIDELITY.md`](FIDELITY.md) · Benchmark: [`bench/REPORT.md`](bench/REPORT.md) · Prior-art survey: [`SURVEY.md`](SURVEY.md)
- Remote-browser service: [`browserproxy`](https://github.com/go-webengine/browserproxy)

## License

BSD-3-Clause — see [`LICENSE`](LICENSE). Copyright (c) the go-webengine/engine
authors.
