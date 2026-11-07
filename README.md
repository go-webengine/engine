<p align="center"><img src="https://raw.githubusercontent.com/go-webengine/brand/main/social/go-webengine.png" alt="go-webengine/engine" width="720"></p>

# go-webengine / engine

[![CI](https://github.com/go-webengine/engine/actions/workflows/ci.yml/badge.svg)](https://github.com/go-webengine/engine/actions/workflows/ci.yml)
[![vuln](https://github.com/go-webengine/engine/actions/workflows/vuln.yml/badge.svg)](https://github.com/go-webengine/engine/actions/workflows/vuln.yml)
![coverage gate](https://img.shields.io/badge/coverage%20gate-css%2099.5%20%C2%B7%20layout%2Fpaint%2Fpaginate%20100%20%C2%B7%20dom%2097.4-brightgreen)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-webengine/engine.svg)](https://pkg.go.dev/github.com/go-webengine/engine)
[![Docs](https://img.shields.io/badge/docs-mkdocs--material-0079A8)](https://go-webengine.github.io/docs/)
[![License: BSD-3-Clause](https://img.shields.io/badge/License-BSD--3--Clause-blue.svg)](LICENSE)
[![Go 1.27.3+](https://img.shields.io/badge/Go-1.27.3%2B-00ADD8?logo=go)](https://go.dev/dl/)

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

**SVG stays vector** (v0.22.0). An inline `<svg>`, an `<img src="x.svg">` or a
`data:image/svg+xml`, and an SVG background are drawn by `PaintVector` as the
shapes they paint, not as their bitmap. This covers a go-tex formula, an emoji
and a logo. The shapes come from go-gfx/oksvg's `Shapes`, which follows what its
rasteriser draws:

- fills keep their curves and fill rule;
- strokes become their outlines, with width, caps, joins and dashes;
- linear and radial gradients become shadings;
- clip paths become clips;
- opacities go into the alpha.

`stats.Vectors` counts them. The outputs differ:

- **pdfcanvas** draws each distinct drawing once per document as a form XObject
  and paints it with one `Do` at each use, inside its Figure's or Formula's
  marked content. A formula's glyphs are outlines (go-tex/math draws them as
  paths), so its drawing also carries its TeX as `/ActualText`, which is what a
  reader copies and a search finds.
- **officecanvas** records the shapes as one `KindGroup`, which go-slides writes
  as a `p:grpSp` or `draw:g` of custom shapes. A slide's shape cannot be clipped,
  and an office radial gradient does not end at a radius. So an SVG that needs a
  clip, a box that cuts it (a background tile), or a radial gradient is placed as
  its picture there.
- **The bitmap render** does not change, and never computes the vector form: it
  is made the first time a vector canvas asks for it.

An SVG is still placed as its bitmap in these cases:

- `Shapes` has no exact form for it: a gradient whose stops change opacity, a
  reflected or repeated one, a focal radial one, or a clipPath of clipped
  pieces;
- a `filter` applies to the image;
- it is past `paint.MaxVectorOps` (200 000 path operations);
- it has a stroke of more than 65 536 dashes.

The library side of this is `paint.PaintVectorWith` with `paint.VectorImages`.
A canvas opts in to `paint.VectorCanvas` (it places the image itself),
`paint.GroupCanvas` (it keeps the shapes together) and `paint.VectorSupporter`
(it says what it can draw).

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
  `clamp()` on any other property, remain unimplemented — see below). The
  small/large/dynamic viewport-percentage units `svh`/`lvh`/`dvh` and
  `svw`/`lvw`/`dvw` (round 193) are accepted everywhere a plain `vh`/`vw` is,
  collapsing to the same value — this engine has no scrolling/mobile-chrome
  concept for the three variants to diverge over.
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
  `mask-image` (a single `url()` mask), **`clip-path: polygon()`** (round
  194) — including a `calc()` term mixing a percentage with an absolute
  length, resolved at paint time against the element's own border-box size —
  group `opacity`.
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
  **`<picture>`/`<source>`/`srcset`** (round 195): a `<source>` whose `media`
  matches the current viewport wins over the `<img>`'s own fallback `src` —
  only the first candidate of a matching `<source>`'s `srcset` is taken (no
  `type`-attribute filtering, no width/density descriptor selection).
- **JavaScript**: page scripts run via [goja](https://github.com/dop251/goja)
  against a real DOM, with `fetch()`/XHR and read-back of real laid-out geometry
  (`getBoundingClientRect`, `offsetWidth/Height`, `getComputedStyle`). A
  **settle-then-render loop** re-cascades and re-lays-out after scripts mutate the
  DOM (incl. dynamically injected `<script>`/`<style>`/`<link>`), to a bounded
  fixpoint — so `mw.loader`-style runtime chrome is reflected in the output. The
  same JS-settled DOM drives the click hit-map. `form.requestSubmit()`
  (round 192) fires a real, cancelable `submit` event so a page's own
  `onsubmit`-driven `fetch()` submission runs. `document.location` (round
  199) is the SAME object `window.location` returns, not a separate one —
  reading or navigating through either accessor stays consistent.
  `document.referrer` (round 200) is always `""` — honest, since this
  engine's single-URL fetch has no referring-page history to report.
  `window.postMessage()` (round 201) is a no-op — this engine's
  single-document model has no second window/frame to deliver to (a real
  `MessagePort.postMessage`, via `MessageChannel`, is unaffected and
  delivers for real).
- **WebAssembly** (round 189): a page's own `WebAssembly.compile`/`instantiate`
  (and, since 2026-11-03, `compileStreaming`/`instantiateStreaming` —
  `instantiateStreaming(fetch(url))`, the overwhelmingly common real shape)
  runs real `.wasm` modules via [wazero](https://github.com/tetratelabs/wazero)
  (pure Go, no CGO — its Interpreter engine covers every architecture this
  project cross-builds for; its Compiler engine speeds up amd64/arm64 where
  available). A module's own exported functions and linear memory
  (`instance.exports.memory.buffer`, a real zero-copy `ArrayBuffer`) are
  modelled, with function imports wired to real JS callbacks. Bounded like
  every other resource this engine budgets: a **configurable**
  `Engine.MaxWasmModuleBytes`/`MaxWasmMemoryBytes` quota (zero keeps the
  32 MiB/16 MiB defaults; raise either toward the wasm32 4 GiB ceiling for
  genuinely larger real modules), and the same wall-clock deadline JS
  execution is already held to.
- **Automation: wait for a page to post "done"** (2026-11-03,
  `Engine.EnableDoneSignal`): off by default (no behaviour change, not even
  an extra global). On, it installs `__webengineDone(value)`, which a
  page's own script calls once its async work is actually finished; the
  settle loop (`RenderDocument`/`RenderHTML`) then returns as soon as that
  fires, instead of running out the full pass/`JSTimeout` budget — the
  "post done, meaning async completion with a timeout" shape an automation
  caller needs, without any WebDriver/CDP protocol.
- **Dedicated Workers** (2026-11-04, `new Worker(scriptURL)`, engine#342):
  a real second [goja](https://github.com/dop251/goja) runtime on its own
  goroutine — goja supports one `Runtime` per goroutine but never passing
  object values between them, so `postMessage` crosses the boundary through
  a bounded, VM-agnostic structured-clone subset (booleans/numbers/strings/
  plain Array/plain Object; a function, Symbol, Date/RegExp/Map/Set, or
  ArrayBuffer/TypedArray throws, matching the real algorithm's own
  `DataCloneError` rather than silently losing the value — `SharedArrayBuffer`
  transfer is a later milestone). The round trip is deliberately
  synchronous/blocking rather than truly async (this engine has no
  background event loop to deliver an async reply into), bounded by the
  render's own wall-clock budget either way; `w.terminate()`/`self.close()`/
  the session itself closing all stop the worker's goroutine.
- **`Engine.EnableWorkerStreaming`** (2026-11-04): a Worker's own
  unsolicited, streamed messages (a container posting stdout for its whole
  lifetime, not replying to a specific `postMessage` call) need the settle
  loop to keep running past its own DOM-change fixpoint — off by default
  (zero behaviour change, not even the extra real-time polling cost). On, a
  live loop drains timers and Worker inboxes on a real ~15ms wall-clock
  cadence until the page signals `__webengineDone` (pair this with
  `EnableDoneSignal` and a large enough `JSTimeout`) or `JSTimeout` elapses
  either way — never open-ended. Confirmed needed directly by
  go-wasmcontainers after M2 shipped: their containers stream stdout/
  stderr/exit-status/GPU-frame messages continuously, which M2's own
  synchronous-reply `postMessage` round trip alone cannot surface.
- **`WorkerGlobalScope.importScripts()`** (2026-11-05, engine#342 follow-up):
  every url is resolved/validated first (a bad one throws `SyntaxError`
  before any fetch, per spec), then fetched and run IN TURN sharing the
  worker's own global scope like multiple top-level `<script>` tags. A fetch
  failure or an uncaught exception in an imported script propagates to the
  calling script (spec's own "rethrow errors") — the one case where this
  engine's usual "a script error never propagates" rule doesn't apply.
  Fixed alongside it: a worker's own relative-URL base was wrongly the
  creating document's `PageURL`, not the worker's own script URL — found
  writing this feature's own test (a worker not co-located with the main
  page, exactly `importScripts("wasm_exec.js")`'s real shape), not assumed.
- **A Worker's own global scope now has the full general-purpose (non-DOM)
  surface the main document has** (2026-11-07, engine#342 follow-up):
  `crypto`/WebCryptoAPI, `TextEncoder`/`TextDecoder`, `fetch`/
  `XMLHttpRequest`/`Headers`/`Request`/`MessageChannel`, `URL`,
  `URLSearchParams`, `Intl`, and `performance.now()`. Found by auditing
  every remaining call in the main document's own `install()` after
  `crypto.getRandomValues` turned out to be missing from a Worker for the
  SAME reason `DOMException` was (engine#342's own first follow-up): each
  rode along with a DOM-specific install path a Worker correctly never
  runs, despite having no DOM dependency of its own — confirmed by reading
  each one's own body, not assumed, before being added here. `fetch()`
  inside a Worker also benefits from the relative-URL base fix above: a
  worker's own `fetch('./sibling.json')` resolves against the WORKER's own
  script location, not the creating document's.
- **A module's own DECLARED shared memory** (2026-11-07, round 5, engine#342
  follow-up): resolves round 4's one open question below — asked
  go-wasmcontainers directly whether their guest modules import memory or
  declare it locally, and confirmed it's the latter (`wasmbin.LimitMemory`
  rewrites the module's own memory section to set `maximum` + the threads
  proposal's `shared` flag before compiling; the worker then reads
  `instance.exports.memory` and posts THAT). wazero's own public API has no
  way to ask whether a compiled module's memory is shared, so this engine
  answers it itself — `moduleHasSharedMemory` walks the raw module bytes
  using the exact limits-flag rule wazero's own internal decoder applies
  (confirmed by reading its source, not guessed). `wasmRT` enables wazero's
  `experimental.CoreFeaturesThreads` only when cross-origin isolated —
  without it, wazero's own decoder rejects a shared-memory module outright,
  the SAME restriction a real browser enforces. `instance.exports.memory`
  for such a module now gets the identical `SharedArrayBuffer`-classed,
  postMessage-able treatment round 4's standalone constructor already has.
- **Standalone `new WebAssembly.Memory(descriptor)`, including
  `shared: true`** (2026-11-07, round 4, engine#342 follow-up): the one
  still-failing line in go-wasmcontainers' own worker-globals probe, and
  confirmed by them as blocking every one of their guest containers (each
  constructs a shared `Memory` before instantiating anything, then posts
  that SAME object to a second worker that reads/writes it directly while
  the guest blocks in `Atomics.wait`). Built on the existing
  `SharedArrayBuffer` machinery below: a growable shared buffer
  pre-allocates its full `maximum` up front and only ever RE-SLICES on
  `grow` (never reallocates), so a write made through one agent's wrapper
  stays visible through another's — goja's own `ArrayBuffer` has no
  resize primitive, so a realloc-and-copy grow would have silently broken
  sharing for anyone already holding a reference. Gated on cross-origin
  isolation exactly like `SharedArrayBuffer`/`Atomics`; postMessage-able as
  the `Memory` object itself (not just its `.buffer`). **Not solved**:
  wazero's own public API (confirmed by direct inspection) has no way to
  satisfy a module's memory IMPORT with host-provided bytes, so an actual
  guest `.wasm` module cannot yet use this Memory as its own linear memory
  that way — a real upstream limitation, stated rather than routed around.
- **`WebAssembly`, `location`, nested `Worker`, and a global `Response`
  constructor** (2026-11-07, round 3, engine#342 follow-up): go-wasmcontainers'
  own rewritten capability probe (checking every worker global at once
  rather than stopping at the first `ReferenceError`) named these
  precisely. `WebAssembly`/`location` are the same recurring gap shape as
  `crypto` before them — no DOM dependency, now wired into a Worker's own
  scope; `location` correctly reflects the WORKER's own script URL.
  Nested `Worker` (a worker creating its own child workers, confirmed
  needed by their engine) needed one real fix: `stopWorkers`' own cleanup
  now recurses, so `Session.Close()` stops a grandchild worker too, not
  just the immediate child — without it, a nested worker's own goroutine
  would leak past the session's lifetime. `Response` was genuinely missing
  as a global constructor everywhere, not just in workers (`fetch()`
  always built one internally, but `new Response(...)` never existed) —
  fixed once, so both the main document and every Worker get it.
- **`SharedArrayBuffer` + `Atomics`** (2026-11-04, engine#343): real
  cross-VM shared memory for Worker's own `postMessage` boundary, gated on
  real cross-origin isolation (below) exactly like a real browser —
  `new SharedArrayBuffer(n)` wraps goja's own zero-copy `ArrayBuffer`
  (registered so a `postMessage` of it, OR of a typed-array/`DataView`
  VIEW over it, shares the SAME bytes instead of cloning them — the one
  deliberate exception to every other value's copy-not-share clone).
  `Atomics.load/store/add/compareExchange` work across the plain
  8/16/32-bit integer TypedArray kinds; `Atomics.wait`/`notify`
  (restricted to `Int32Array`) give a real, blocking cross-goroutine
  wait/wake handshake — `wait` genuinely parks the calling Worker's own
  goroutine until a matching `notify` (or its own timeout) wakes it.
  Deliberately restricted to a registered SharedArrayBuffer's own views —
  a plain `ArrayBuffer` throws a named `TypeError` rather than silently
  misbehaving, since cross-VM sharing is the one confirmed need Atomics
  exists for here. `instanceof SharedArrayBuffer`,
  `Object.prototype.toString.call(x) === "[object SharedArrayBuffer]"`,
  and `x.constructor === SharedArrayBuffer` all report correctly — on
  the receiving side of a `postMessage` too, not just where it was
  created — confirmed needed by go-wasmcontainers: real code (their own
  e2e probe included) branches on `instanceof SharedArrayBuffer` to
  choose a shared-memory path over a copy path. Remaining stated
  simplification: `x instanceof ArrayBuffer` is also `true` (the spec's
  two classes are siblings, not related by inheritance) — no confirmed
  need to separate them, since checking `instanceof SharedArrayBuffer`
  first (the common idiom) already takes the correct branch regardless.
- **`window.crossOriginIsolated` + real COOP/COEP gating** (2026-11-04):
  `true` only when the top document's own response actually carried
  `Cross-Origin-Opener-Policy: same-origin` and
  `Cross-Origin-Embedder-Policy: require-corp` (or `credentialless`) —
  `SharedArrayBuffer`/`Atomics` are not installed at all otherwise
  (`window.SharedArrayBuffer` is `undefined`), matching a real browser
  exactly rather than a simplification: this engine is used as a test
  lane beside Chromium/Firefox/Safari, so a page missing these headers
  must see SAB denied here too, and a render can load cross-origin
  subresources into the same runtime regardless of its own process
  boundary, so the threat COOP/COEP guards against is real here as well.
  `Engine.ForceCrossOriginIsolated` (off by default) is the escape hatch
  for a host building its own `Document` with no real headers to check
  (tests, a `Fetcher`). Workers inherit the creating page's own resolved
  value, per spec.
- **`setInterval` genuinely repeats** (2026-11-04): previously fired its
  callback once (`clearTimeout`/`clearInterval` were no-ops) — a
  simplification from before a page could usefully run long enough for
  repeating to matter. Now a real `setInterval` fires again at its own
  requested cadence (real wall-clock time, not an instant burst), and
  `clearTimeout`/`clearInterval` actually cancel by id, including from
  within the interval's own callback (`clearInterval(id)` to stop
  yourself). Confirmed needed the moment `EnableWorkerStreaming` made a
  genuinely long-running page a real thing: a page's own `setInterval`-based
  "is it done yet" poll previously ran exactly once, very early, reporting
  a stale snapshot forever after.
- **A one-shot `setTimeout`'s first firing also honours real wall-clock
  delay under `EnableWorkerStreaming`** (2026-11-04): the fix above only
  covered a repeating interval's own re-firings; a plain, one-shot
  `setTimeout`'s first (and only) firing still fired at "the next
  opportunity" rather than after its real requested delay. This breaks the
  extremely common "safety-net timeout" idiom (race a near-immediate retry
  against a much-later give-up timer) the moment a page expects it to wait
  real time — confirmed directly: go-wasmcontainers' own e2e probe still
  failed after the `setInterval` fix, and a standalone reproduction of
  their exact page showed the whole render completing in under 4ms against
  their own 15-second safety net. `EnableWorkerStreaming` now gates every
  timer job's first firing by its real delay, not just a repeating
  interval's own repeats.
- **Origin Private File System** (2026-11-05, `navigator.storage.getDirectory()`,
  engine#344): `FileSystemDirectoryHandle`/`FileSystemFileHandle`
  (`getFileHandle`/`getDirectoryHandle`/`removeEntry`) and
  `FileSystemSyncAccessHandle` (`read`/`write`/`truncate`/`getSize`/`flush`/`close`,
  `[Exposed=DedicatedWorker]` per spec — only reachable from inside a
  Worker). Off by default (`Engine.OPFSRoot` empty): nothing is installed,
  zero real-disk writes a page could ever trigger. A host opts in by naming
  a real, durable directory; each origin gets its own SHA-256-hashed
  subdirectory under it, so two pages fetched by the same `Engine` can
  never see each other's tree. Every name is validated BEFORE any path
  join (rejects empty, `.`, `..`, `/`, `\`, NUL), making traversal outside
  the origin directory structurally impossible rather than filtered.
  `Engine.MaxOPFSBytes` bounds a single origin's total usage (zero selects
  a 50 MiB default, negative disables the guard); a write/truncate that
  would exceed it throws `QuotaExceededError` with no partial change.
  `createSyncAccessHandle` takes a real exclusive lock per file
  (`NoModificationAllowedError` on a second concurrent open).
- **`wgslmsl`: a pure-Go WGSL→MSL compute-shader translator** (2026-11-05,
  engine#345, package `wgslmsl`): the first piece of WebGPU compute support
  (M5). Parses and type-checks with `go-gfx/wgsl` (a pure-Go, CTS-conformant
  WGSL frontend), then emits Metal Shading Language for a module's one
  `@compute` entry point — no Rust toolchain, no vendored binary blob (an
  earlier spike proved compiling `naga` to `wasm32-unknown-unknown` and
  running it through this engine's own WebAssembly support also works end
  to end, verified bit-for-bit against real Metal, but was superseded by
  this pure-Go path once go-gfx/wgsl turned out to need only a backend).
  Every array index is clamped against its real bound buffer's element
  count — Metal performs no bounds checking of its own, so this is a real
  security boundary: a page's own untrusted WGSL cannot read or write
  adjacent GPU memory through an out-of-range index, proven on real
  hardware by `TestOutOfBoundsIndexIsClamped`. Scoped deliberately to the
  realistic compute-kernel subset: one entry point reaching one function
  (no helper calls yet), `storage` resource arrays of scalar/vector element
  type, plus (2026-11-07) a bare `<uniform>` scalar/vector and a storage
  `atomic<u32>`/`atomic<i32>` with the ten common atomic built-ins (no
  textures/samplers/matrices/structs yet), the common control-flow
  statements and a fixed allow-list of built-in math functions — every
  construct outside that scope is a clear, named error, never a silent
  gap. Not yet wired to `navigator.gpu` (next in M5).
- **`navigator.gpu`/`GPUAdapter`/`GPUDevice`/`GPUBuffer`** (2026-11-05,
  engine#345): WebGPU compute via `go-macos/metal` (pure Go, no cgo,
  cross-builds and cross-runs on every architecture this repo already
  targets, reporting no usable adapter anywhere but darwin). Off by default
  (`Engine.EnableWebGPU`) — same opt-in convention as `OPFSRoot`/
  `ForceCrossOriginIsolated`: a page getting compute access to the host's
  real GPU the instant it renders is a comparable capability grant to real
  disk access, so it is not on by default like a real browser. When on,
  `requestAdapter()` genuinely never rejects (resolves `null` with no
  usable GPU, matching spec); `GPUBuffer`'s `mapAsync`/`getMappedRange`
  return a real, zero-copy view over the buffer's own unified memory, and
  `unmap`/`destroy` detach every such view, per spec. `GPUSupportedLimits`
  reports the spec's own documented default values (not yet queried from
  real hardware per-limit capability — stated honestly, not overclaimed).
  Shader modules and the rest of the compute pipeline (bind groups, command
  encoding, `queue.submit()`) follow below — M5 is now complete.
- **`GPUShaderModule`/`GPUComputePipeline`/`GPUBindGroup`/`GPUCommandEncoder`/
  `queue.submit()`** (2026-11-05, engine#345): the rest of the compute
  pipeline, closing out M5. `createShaderModule` translates via `wgslmsl`
  eagerly but never throws — a bad shader still gets a usable module object,
  matching spec; the error surfaces from `getCompilationInfo()` or a clear
  throw from `createComputePipeline`. Only `layout: "auto"` is supported
  (an explicit `GPUBindGroupLayout` is a clear, named error) — `wgslmsl`
  already infers the real binding layout from the shader source itself. A
  stated gap inherited from `go-macos/metal`, not from this binding's own
  choices: `Dispatch` picks its own threadgroup shape from the hardware,
  with no way to force it to match a shader's declared `@workgroup_size`,
  so `local_invocation_id`/`workgroup_id` may not reflect the intended
  grouping — harmless for the common case (a kernel keyed purely off
  `global_invocation_id`), stated rather than silently risked otherwise.
  `TestComputePipelineEndToEnd` is the roadmap's own final verification: a
  real compute shader created, compiled, bound and dispatched entirely
  through the public `navigator.gpu`-shaped API, output checked bit-for-bit
  against real Metal hardware.
- **`wgslmsl`: uniform buffers and storage `atomic<u32>`/`atomic<i32>`**
  (2026-11-07, engine#345 follow-up): go-wasmcontainers' own second
  confirmed need — every looping kernel in their batch path rewrites each
  loop to count against a `var<uniform> budget: u32` and sets a
  `var<storage, read_write> flag: atomic<u32>` via `atomicStore` before
  breaking. `atomicLoad`/`Store`/`Add`/`Sub`/`Max`/`Min`/`And`/`Or`/`Xor`/
  `Exchange` map 1:1 to `metal_atomic`'s own `atomic_*_explicit` functions
  (confirmed by compiling each against the real Metal compiler before this
  shipped, not assumed from documentation — Apple's own spec PDF exceeded
  the fetch tool's size limit). Confirmed directly, not just reasoned
  about: `layout: "auto"` already handles a kernel using `@group(0)` and
  an explicit `@group(3)` together correctly, with no changes needed.
  Still blocked, and stated: `var<workgroup>`/`workgroupBarrier()`/an
  exact `@workgroup_size`-matching dispatch (needed by their LLM kernels)
  — `go-macos/metal` v0.6.0 still has no way to pin an explicit
  threadgroup shape for `Dispatch`.

**Honest limits (not overclaimed)**

- `<canvas>.getContext(...)` is not implemented — a call throws (`Object
  has no member 'getContext'`, confirmed live on stripe.com's own bundle).
  No `CanvasRenderingContext2D`/WebGL surface at all, even an inert one.
- `<picture>`/`srcset`: a `<source>`'s `type` attribute (skip one whose
  declared MIME type can't be decoded) and width/density descriptors
  (picking the best of several `srcset` candidates) are not evaluated —
  only the first comma-separated candidate of the first `media`-matching
  `<source>` is ever taken. No confirmed trigger uses either.
- `WebAssembly`: no WASI (`wasi_snapshot_preview1` — stdio, filesystem,
  clocks) is wired, so a module assuming a WASI host (e.g. a `GOOS=wasip1`
  Go build, as opposed to `GOOS=js`) fails to instantiate with a LinkError
  naming the missing import. The synchronous
  `new WebAssembly.Module(...)`/`new WebAssembly.Instance(...)` constructor
  forms, Table and Global imports/exports, and standalone `Table` objects
  are not implemented — no confirmed container needs any of them yet.
  Standalone `new WebAssembly.Memory(descriptor)` (including `shared:
  true`) IS implemented (2026-11-07, round 4), and so is a module's own
  LOCALLY DECLARED shared memory, read back via `instance.exports.memory`
  (2026-11-07, round 5) — but passing a `Memory` as a module's own memory
  IMPORT is not: wazero's public API has no way to satisfy that, a real
  upstream limitation (see round 4's own entry above), confirmed NOT to be
  on go-wasmcontainers' own critical path (round 5). `compileStreaming`/`instantiateStreaming`
  ARE implemented (2026-11-03) but not via genuine incremental streaming —
  this engine's own `fetch()` has no streaming body to begin with, so the
  Response argument is fully read before compiling, same as the plain
  `compile()`/`instantiate()` forms.
- `clamp()` is only resolved for `font-size`, which resolves `%`/`vw` EAGERLY
  at cascade time — a width-like property's own percentage still depends on
  a containing block only known at layout time, the same wall `min()`/`max()`
  (unimplemented anywhere) and `calc()` mixing a percentage with an absolute
  length already disclose above. A `clamp()` term mixing a real percentage
  with `vw`/`rem` in the SAME term is not attempted — no confirmed trigger
  uses it.
- `calc()` mixing any viewport unit — `vh`/`vw` or the `svh`/`lvh`/`dvh`/
  `svw`/`lvw`/`dvw` variants alike — with another term is not resolved; this
  predates round 193's unit-parsing fix and round 193 deliberately left it
  as is (`calcParser.factor()` already rejects any percentage-kind term
  outside its own dedicated `%` branch).
- `clip-path` only recognises `polygon()` (with an optional leading
  `nonzero`/`evenodd` fill-rule), resolved against the border box — the
  spec's default reference box. A `<geometry-box>` keyword,
  `circle()`/`ellipse()`/`inset()`/`path()`, `round <length>` vertex
  rounding, and a `url()` reference to an SVG `<clipPath>` element (a
  different mechanism — see the SVG rasteriser's own clip handling) are not
  recognised and drop the whole declaration. Not extended to `::before`/
  `::after`. The polygon's edge has no antialiasing.
- A **dynamic `import(...)`, in ANY script** (not only `<script
  type="module">`, which this engine's own `esbuild` bundling pass already
  resolves statically) **is rejected by goja's parser outright** — confirmed
  directly against the pinned version. A runtime-computed import target
  (Astro's own universal client-hydration bootstrap, and likely other
  frameworks' lazy-loading patterns) can't be bundled away the way a static
  import can, since deferring resolution past build time is the whole
  point. No JS parser lives in this repo to patch.
- `form.requestSubmit()` fires the "submit" event only — no per-control
  constraint validation, and (like `window.location.assign()`/`.reload()`)
  no actual network submission or page navigation when the event is not
  cancelled, since this engine's single-document settle has no later page
  to navigate to. `form.submit()` fires no event at all, matching spec.
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
  the page's own time budget any more than an infinite JS loop can. Both
  caps are configurable (`Engine.MaxWasmModuleBytes`/`MaxWasmMemoryBytes`,
  2026-11-03) for a host that genuinely needs larger real modules, but
  always bounded by SOME finite quota — never unlimited, and clamped to the
  wasm32 4 GiB address-space ceiling regardless of what a caller requests.
  No WASI is wired, so a module cannot reach the filesystem, the network,
  or a clock through it.

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
