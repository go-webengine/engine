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
- **A shader-creation error now surfaces at `createShaderModule`, not
  later** (fix, 2026-11-08, round 30, go-wasmcontainers report): reported
  on batch-shaders, their last red container case — a module using the
  reserved word `meta` as an identifier is genuinely invalid WGSL by
  spec, which every real engine refuses right at `createShaderModule`
  (captured by an open error scope, reported by `getCompilationInfo()`
  with its real line/column), not only once a pipeline first tries to
  use it. Two questions kept separate: go-gfx/wgsl's own `Validate` (is
  this real WGSL at all) runs independently of `wgslmsl.Translate` (can
  THIS translator's own backend run it) — a construct this translator
  doesn't support yet but is genuinely valid WGSL (a switch statement, a
  pointer parameter) stays exactly as before, never reported as a
  shader-creation error, since a real browser would compile it fine.
- **`arrayLength(&resource)`** (fix, 2026-11-08, round 29,
  go-wasmcontainers report): reported after v0.45.6 cleared the entire
  ONNX lane (123/123) — their batch/batch-grant kernel's own
  `if (id.x < arrayLength(&out)) { ... }`, the ONLY use of `&` in it
  (confirmed by reading their real source, not guessed — no atomics, no
  `ptr<function, T>` parameters). `callExpr` now recognizes
  `arrayLength(&X)` for a tracked runtime-sized storage resource and
  emits its already-computed bounds-check element count directly — the
  same one `indexExpr`'s own clamp reads, correctly the bound sub-range's
  count. General `&`/pointer support stays out of scope until a real
  kernel needs it.
- **A `continue` inside `for`/`loop` skipped the update/continuing block,
  hanging the kernel** (fix, 2026-11-08, round 28, go-wasmcontainers
  report): reported after v0.45.5 (57 → 100 of 123 ONNX cases) — every
  remaining failure was a padded conv/pool/mnist-12 case hitting their
  host's own loop-budget guard. `for`'s own lowering ran `update` AFTER
  the body, so a bare MSL `continue` (jumping straight back to the
  `while(true)` wrapper) skipped it outright — the loop variable never
  advanced, an infinite loop the instant that branch fired. Tried
  goto+labels first, confirmed wrong directly: Metal's own compiler
  refuses labeled statements outright. Fixed with `do { } while (false)` +
  a per-loop flag instead: `continue` becomes a bare `break` (exits only
  the wrapper, falls through to update); a real `break` sets the flag
  first, so the line after the wrapper also breaks the real, outer loop —
  without it, every `break` would silently become a continue. `loop {
  continuing { } }` had the identical shape and got the identical fix.
- **`bitcast<T>(e)` reinterprets bits, and a helper function may touch a
  resource directly** (two fixes, 2026-11-08, round 27, go-wasmcontainers
  report): reported on their v0.45.4 retest (26 → 57 of 123 ONNX cases).
  `bitcast<f32>(1u)` was translated as a numeric conversion (`float(x)`,
  giving `1.0`) instead of a bit reinterpretation (WGSL §17.2: the float
  whose bits equal 1, a tiny subnormal) — their Gemm/MatMul kernels decode
  an f32 alpha stored as raw bits in a u32 uniform this way, so every
  result came back ~1e9 too large. Fixed with Metal's own `as_type<T>(e)`,
  one emission covering scalar and same-arity vector bitcasts alike. A
  helper that directly touches a resource (round 26's own stated, bounded
  scope gap) is now threaded through as an extra MSL parameter — its own
  direct touches UNION every function it calls already needs, computed
  callee-before-caller — at both its own signature and every call site
  that reaches it, however many levels away.
- **wgslmsl translates calls to WGSL helper functions** (fix, 2026-11-08,
  round 26, go-wasmcontainers report, engine#391 follow-up): reported on
  97 of their 123 ONNX cases — their nn kernels call small WGSL helpers for
  index math and activation, and `Translate` refused any entry point
  reaching more than its own body. Every function the entry point
  transitively calls is now emitted as a real MSL function, callee-before-
  caller order (found along the way: go-gfx/wgsl's own `FuncObj.Calls`/
  `.Globals` are not populated outside an entry point — its own newer `msl`
  package hits the identical gap and writes its own AST walker instead of
  trusting the field; wgslmsl now does the same). Scoped to pure value
  functions: a helper touching a `<storage>`/`<uniform>` resource directly
  is rejected, clear and named — resources are wired to the entry kernel's
  own buffer parameters and nowhere else. Considered and rejected:
  delegating to go-gfx/wgsl's own new `msl` package for WGSL-exact
  arithmetic semantics — real advantage, but splits helper-function math
  from entry-body math onto two correctness bars for no reported need.
- **`createBindGroup` accepts a GPUBufferBinding sub-range, not only the
  whole buffer** (fix, 2026-11-08, round 25, go-wasmcontainers report):
  reported after v0.45.2 fixed their GPU hang — `createBindGroup: a
  GPUBufferBinding size other than the whole buffer is not supported`,
  blocking several of their batch/ONNX cases, all of which pack several
  logical buffers into one real allocation and bind each as its own
  sub-range. Two independent gaps, both confirmed needed by sabotaging
  each in isolation: go-macos/metal's own `Encoder.Buffer` always bound at
  offset 0 (fixed upstream, new `Encoder.BufferAt`, v0.7.0), and the
  bounds-check sizes buffer (wgslmsl's own per-binding array-length
  mechanism, since Metal performs no bounds checking of its own) was
  computed from the bound buffer's WHOLE size, not this binding's own
  sub-range. Fixed via a new `gpuBoundBuffer{buf, offset, size}`, validated
  against WebGPU §8.2.1/§4.2.1 (resides inside the buffer,
  `minStorageBufferOffsetAlignment`, size a multiple of 4) using the limits
  this binding already advertises.
- **`Worker.postMessage` is genuinely asynchronous now, not a blocking round
  trip** (fix, 2026-11-08, round 24, engine#388, M2/M3 follow-up): diagnosed
  jointly with go-wasmcontainers via a `SIGQUIT` goroutine dump on their own
  failing `TestBatchComputeContainers`. `postToWorker` blocked the sender
  until the receiving Worker finished handling the posted message — a
  deliberate M2 trade-off ("a deterministic, testable round trip"), and a
  real deviation from the spec (`postMessage` must be async: HTML §9.4.3).
  For a container whose entire life runs inside handling its first message,
  "wait until handled" became "wait until the container exits" — and a GPU
  call needing the sender's OWN event loop to run again (to resolve a
  promise, then `Atomics.notify` the waiting container) could never get it.
  A narrower fix (keep blocking, but also drain the sender's own timers
  while it waits) was tried and rejected: confirmed by a direct test that it
  breaks JS's own run-to-completion guarantee, letting a `setTimeout`/
  `Promise.then` callback run in the middle of the currently-executing
  script that called `postMessage`. The real fix: `postToWorker` clones (at
  post time, unchanged) and queues, then returns immediately; a reply, if
  any, arrives later through the same mechanism that already delivers a
  worker's unsolicited messages. A genuinely new data race this exposed —
  `Options.Log` is one func value every binder in a session shares, no
  longer serialized by `postToWorker`'s own block once workers run truly
  concurrently with main — is fixed with a new `emitLog` helper, serialized
  by a session-wide mutex. go-wasmcontainers' own four required invariants
  (per-port FIFO order, clone-at-post-time semantics, a child's
  `Atomics.wait` not blocking its parent, unsolicited-message delivery while
  a child runs) are each covered by a test. Every existing test that assumed
  a worker's reply was visible immediately after `postMessage` returned is
  restructured to poll instead — several of these were caught only by
  running the suite repeatedly (`-count=25`, `-race -count=10`), since they
  passed by scheduling luck on a single run.
- **A self-requeuing short-delay timer could starve an older, larger-delay
  one indefinitely** (fix, 2026-11-08, round 23, engine#342 fallout): found
  while investigating a new go-wasmcontainers hang on v0.45.0, via a minimal
  `GOOS=js` program whose background goroutine keeps re-arming its own
  `time.Timer` — exactly `go-gfx/webgpu`'s own `kickLoop` shape, which exists
  so Go's "all goroutines asleep" deadlock detector doesn't fire while a
  sibling goroutine awaits a promise. `drainTimers`' own "ascending delay,
  ties FIFO" selection compared each due job's raw, static delay; a job that
  keeps re-queueing a fresh copy of itself never grows that field, so it
  always wins over an older job with a larger delay, for as many sweeps as
  it keeps re-arming. This narrower defect does not, by itself, explain
  go-wasmcontainers' own hang — their driver's `EnableWorkerStreaming` gates
  every job's first firing by a real wall-clock `readyAt`, under which mode
  a further reproduction points at the render's own live-loop/callback path
  instead, still open. Each `timerJob` now ages via a `skipped` counter,
  incremented whenever a sweep passes it over for a smaller-priority due
  job; selection compares `delay - skipped`, so a persistently-starved job's
  priority strictly falls until it wins — bounded and deterministic, not
  dependent on real wall-clock time (a wall-clock-based attempt turned out
  flaky: a tight unit-test loop doesn't reliably advance even one
  millisecond between sweeps).
- **`Blob`, `URL.createObjectURL`/`revokeObjectURL`, and `importScripts`
  resolving a `blob:` URL** (feature, 2026-11-08, round 22, engine#342
  fallout): round 21's fix got go-wasmcontainers their first fully green
  container case — the whole chain running end to end, a guest starting,
  making its WASI calls, and exiting with its real code. The other five
  cases stop earlier, in their GPU host: `go-gfx/webgpu`'s own
  `record_js.go` loads its replay script inside a Worker as `new
  Blob([source], {type: "text/javascript"})` -> `URL.createObjectURL(blob)`
  -> `importScripts(thatBlobURL)` -> `URL.revokeObjectURL(url)`, and none
  of `Blob`/`createObjectURL`/a `blob:`-aware `importScripts` existed in
  this engine at all. Scope, confirmed by their own report: a `Blob` built
  from an array of STRING parts with a `type` option, and the File API's
  own "blob URL store" — not `BufferSource`/`Blob` parts or any other Blob
  method, none of which any confirmed caller touches. `Blob`'s own content
  and `URL`'s own static methods are scoped PER-BINDER (never a
  package-level registry) — a `blob:` URL is only ever valid in the
  environment that minted it, matching both a real browser and
  go-wasmcontainers' own shape (one Worker creates and consumes its own
  Blob entirely by itself). `importScripts` now resolves a `blob:` URL
  against that store directly, never through the HTTP fetch path.
- **A module's own shared memory now stays live for every agent even when
  grown by the wasm `memory.grow` INSTRUCTION, not just the JS-level
  grow() method** (fix, 2026-11-08, round 21, engine#342 fallout): round
  20's fix cleared the exception-identity failure, and go-wasmcontainers'
  `clock_time_get` failure from the SAME report turned out to be the exact
  same grow-visibility class of bug, one layer deeper — their real guest
  grows its OWN memory with the `memory.grow` instruction during Go
  runtime start-up, inside wazero, and the container worker that ran it
  never touches `.buffer` itself again afterward (it's blocked in
  `Atomics.wait` inside a host import), so round 20's "sync on grow() or a
  local read" fix never fired at all. wazero exposes no public "memory
  grew" hook, but its own `Grow`, for a shared memory specifically, stores
  the new length with an ATOMIC operation explicitly to make it visible
  across threads — confirmed not just by reading that comment but by an
  isolated program hammering `Size()`/`Read()` from one goroutine while
  another grows the SAME shared memory 200,000 times, clean under
  `go test -race`. Fixed by giving the registered buffer a live reference
  to wazero's own memory and re-syncing from it, live, on every access —
  by this binder's own `.buffer` accessor, by every OTHER agent's own
  reconstructed wrapper after a postMessage, and by every `Atomics.*` op —
  rather than ever caching a snapshot.
- **A host import's own thrown exception propagates unchanged again, and a
  module's exported shared memory now grows visibly across every agent
  holding it** (fix, 2026-11-08, round 20, engine#342 fallout): with round
  19's fix, go-wasmcontainers' container guest fully RAN for the first
  time — instantiated, memory accepted, `_start` entered, `fd_write`
  working — then stopped, on two independent findings. First: their own
  `worker.js` throws `new Exit(code)` from a host import when the guest
  calls `proc_exit`, expecting it (per the WebAssembly JS API: a host
  function's exception unwinds the wasm frames UNCHANGED, rethrown to the
  caller as the SAME value) to arrive intact for their own `instanceof
  Exit` check — instead it arrived as a generic, wrapped `RuntimeError`,
  since `hostImportFunc` panicked with a brand-new Go error wrapping only
  the original exception's STRING, discarding the real value before
  wazero's own panic recovery (confirmed by reading it directly) ever got
  a chance to preserve it via `%w`. Fixed by panicking with the original
  error unchanged on the way in, and unwinding back to the original JS
  value (`errors.As` + `exc.Value()`, the SAME pattern `importScripts`
  already uses for a script-level exception) on the way out. Second,
  found the same round: a worker that grows its module's own exported
  shared memory and posts it to its creator — the creator saw the
  ORIGINAL byteLength, not the grown one, because the Go-level
  `*sharedBuffer` identity `clone.go` hands to other agents was rebuilt
  from scratch on every local size change instead of being updated in
  place, and `grow()` never touched it at all. Fixed by creating it once
  and updating its own `.data` field in place — a fresh, zero-copy view
  over wazero's own backing array, confirmed never relocated for a shared
  memory — eagerly, right when `grow()` succeeds.
- **`WebAssembly.Memory` never satisfied `instanceof WebAssembly.Memory`**
  (fix, 2026-11-08, round 19, engine#342 fallout): round 18's fix cleared
  the `_start` double-run, and go-wasmcontainers' next run still failed
  every container — their own `worker.js` validates a guest's exported
  memory with `!(memory instanceof WebAssembly.Memory)`, which always
  fired even though the guest genuinely exported a real, present memory.
  Every Memory wrapper this engine builds (a module's own exported memory,
  and the standalone constructor, shared or not) stamped
  `Symbol.toStringTag` so `Object.prototype.toString.call()` reported
  correctly, but NONE of the three ever called `SetPrototype` — unlike
  `WebAssembly.Module`/`.Instance`, which got a real prototype in round 7
  specifically so `instanceof` would hold for THEM. Masked until now
  because every existing test checked the toStringTag, never `instanceof`,
  for Memory specifically. Fixed with a new `wasmMemoryProto` (mirroring
  `wasmModuleProto`/`wasmInstanceProto`) stamped onto all three
  construction sites, and a real `WebAssembly.Memory` constructor wired
  with its own `.name`/`.prototype`.
- **`WebAssembly.instantiate()` no longer auto-runs an export merely named
  `_start`** (fix, 2026-11-08, round 18, engine#342 fallout): round 17's fix
  cleared the hang, and go-wasmcontainers' very next run hit every container
  trapping 9 seconds in with `LinkError: module[] function[_start] failed:
  wasm error: invalid table access`, a repeating `runtime.write ←
  runtime.writeErrData ← ...` wasm stack trace. Their own diagnosis named
  the mechanism precisely: wazero's `NewModuleConfig` defaults to running an
  exported `_start` automatically (a WASI-CLI convenience, confirmed by
  reading wazero's own config.go), but the WebAssembly JS API's own
  `instantiate()` only ever runs a module's internal, unexported start
  SECTION — never an exported function merely named `_start`. Their own real
  `worker.js` instantiates, posts the guest's shared memory to its creator,
  THEN calls `instance.exports._start()` itself — exactly that division of
  responsibility. Running it a first time during `instantiate()`, before
  the memory message had even been sent, reached the guest's own first
  `fd_write` with no memory registered on the host side yet, and its own
  panic/error-write path recursed into the identical failure. Fixed by
  clearing wazero's default (`.WithStartFunctions()` with no arguments) on
  the one `InstantiateModule` call every JS-facing instantiate path funnels
  through.
- **`Worker.postMessage` no longer blocks its own wake-up** (fix, 2026-11-08,
  round 17, engine#342 fallout): round 16's fix cleared one failure in
  go-wasmcontainers' `TestBatchComputeContainers`, and the next run hung the
  full 10-minute `JSTimeout` instead. Their own real `runtime/browser`
  package spawns one container Worker per guest and posts it a setup
  message; the container's own `worker.js` answers by posting a "wake me"
  doorbell and then calling `Atomics.wait` SYNCHRONOUSLY, still inside that
  same `onmessage` call — valid, spec-compliant code, since a real
  browser's `postMessage` is asynchronous and never blocks the sender. This
  engine's own `postToWorker` was, since round M2, deliberately
  synchronous/blocking instead (a documented trade-off for a deterministic
  test round trip) — which turned that exact valid pattern into a real
  deadlock: the creator's `postMessage` call couldn't return until the
  container finished, and the container could never finish without a
  notify only the (blocked) creator could send. Confirmed fast, before
  touching the real 10-minute repro: a new test reproducing the identical
  shape hung out its own 2-second deadman with the original code, every
  time. Fixed by turning `postToWorker`'s wait into a poll loop (the same
  15ms cadence a worker's own event-loop ticker already uses) that keeps
  draining the creator's own pending worker inboxes — including a child's
  doorbell — the whole time it waits, instead of only once at the end.
- **`WebAssembly.Module` survives `postMessage` across workers** (feature,
  2026-11-08, round 16, engine#342 fallout): go-wasmcontainers compile
  each guest module ONCE in an engine worker and post the resulting
  `Module` to a container worker, which instantiates it there — never the
  bytes, recompiling per-container at their scale being wasted work.
  Required sharing one `wazero.Runtime` across every worker (a
  `CompiledModule` cannot be instantiated under a different Runtime than
  the one that compiled it, confirmed directly), which in turn exposed
  two real collision pitfalls go-wasmcontainers flagged in advance and
  both confirmed directly, isolated, before touching production code:
  wazero refuses a second instance under a module name already live on
  a shared Runtime (fixed: `instantiateWasm` always instantiates
  anonymously), and the temporary host-import module `buildHostImports`
  registers under the guest's own required namespace (e.g. `"env"`)
  collides the SAME way when two guests importing from `"env"`
  instantiate concurrently on the shared Runtime — confirmed that wazero
  binds an import at instantiate time, not by live name lookup, so
  closing that host module immediately after and reusing the freed name
  for the next guest doesn't disturb either guest's own already-bound
  calls; fixed with a new mutex (`wasmInstantiateMu`) serializing just
  that narrow window. New tests post a compiled `Module` through the
  main document to a second worker and confirm it instantiates and
  satisfies `instanceof WebAssembly.Module` there, and confirm two
  SEPARATE workers instantiating the SAME posted Module concurrently
  both succeed independently.
- **THE actual cause, independently confirmed against go-wasmcontainers'
  own exact reproduction** (fix, 2026-11-08, round 15): rounds 12-14
  (below) were all real, independently-justified fixes, but none was
  the crash — go-wasmcontainers eventually bisected it themselves to a
  GPU-free, ~20-line repro and shared it, pointing at nested JS→wasm
  reentry. Confirmed directly (not just by reasoning): copied their own
  probe program and ran it against a locally-patched copy of this
  engine — crashed under the current code, passed cleanly (20/20 rounds,
  including real WebGPU calls) once `js/wasm.go`'s wazero
  `RuntimeConfig` was switched from the default `NewRuntimeConfig()` to
  `NewRuntimeConfigInterpreter()`. Root cause, found in wazero's own
  documentation: its default "Compiler" engine (active on amd64/arm64,
  the only two architectures this crash was ever seen on) JIT-compiles a
  module to native code that "uses the different stack than the calling
  Goroutine," and wazero's own doc comment names the exact consequence —
  "the signal handler might get invoked on the wazero's stack, which may
  cause a stack overflow," the literal "split stack overflow" signature
  seen throughout this whole investigation. The pure-Go Interpreter
  engine generates no native code and hence has no separate stack to
  desynchronize, removing the entire crash class. Measured, not just
  asserted: a 200M-iteration compute loop took 3.3s under the Compiler
  engine and 52.7s under the Interpreter engine (~16x), a real cost
  accepted directly in exchange for the crash. New test
  `TestReentrantResumeAfterEventLoopResume` reproduces the peer's own
  minimal pattern as a real Go-compiled-to-WASM fixture (this repo's
  first test to exercise the full `wasm_exec.js`/`go.run` shape, not
  just a bare WASM module) — verified in both directions, including
  reproducing the EXACT same crash signature inside this repo's own
  `go test` process when the fix was temporarily reverted. Follow-up
  found by reading, not a new bug: a heavier, nested-WASM-in-WASM
  workload started tripping the EXISTING heap-safety guard
  (`heapGuardedContext`, default ceiling `baseline + 768 MiB`, from
  round 172's own real memory-exhaustion defect) — the Interpreter
  engine's own execution state needs more Go-heap for the same work
  than the Compiler engine's JIT-compiled code did. The guard cancels
  its OWN derived child context, not the caller's, which is why the
  caller's own `ctx.Err()` stayed nil throughout despite scripts being
  interrupted — resolved by raising `Engine.MaxJSHeapBytes` on the
  caller's side for workloads this heavy, not a code change here.
- **A real device-leak fix, independently real but not THE crash either**
  (fix, 2026-11-08, round 14): round 13's fix below was
  also real, but go-wasmcontainers' own retest proved it wasn't their
  crash either — same signatures, deterministic 6/6, and happening 8-12s
  into the render, well before any `Session.Close`. `destroy()`
  (`js/webgpu.go`) only resolved the JS-visible `lost` promise; it never
  called `.Close()` on the underlying `*metal.Device`. Their own workload
  loads ~123 ONNX models, each into its own fresh device, explicitly
  "destroying" it between models — since `destroy()` did nothing
  natively, all 123 stayed alive, crashing deep inside Apple's own
  Metal/Foundation frameworks once some OS-level resource ceiling was
  crossed. Live lldb debugging was attempted first but is fundamentally
  stuck in this sandboxed environment (every `task_for_pid`-requiring
  tool hangs at 0% CPU); found by direct code reading instead, armed with
  the corrected "mid-render, not teardown" fact. Verified both directions
  via a test that cannot safely reproduce the real exhaustion itself (that
  would crash the test binary) but proves the mechanism: after `destroy()`,
  a subsequent `createBuffer()` must come back as a captured
  `GPUOutOfMemoryError` through the existing error-scope path, proving the
  native device genuinely closed.
- **A real worker-GPU-teardown race, independently fixed but not THE
  crash** (fix, 2026-11-08, round 13): round 12's go-macos/metal bump was
  real but go-wasmcontainers' own retest proved it did not fix their
  crash — it made it deterministic (6/6). The actual bug found this
  round: `js/worker.go`'s `stopWorkers` (called from `Session.Close`)
  signalled a worker to stop and IMMEDIATELY released its GPU devices on
  the MAIN goroutine, with no wait for the worker's own goroutine (the
  only one allowed to touch its binder, per `jsWorker`'s own struct doc
  comment) to actually stop touching them — a worker whose own
  autonomously-scheduled timer callback was mid-flight inside a real,
  blocking Metal dispatch never even observed the stop signal, so
  `Close()` freed objects the worker was still using: a genuine
  concurrent use-after-free on native memory, invisible to `-race`.
  `stopWorkers` now waits (bounded by the render's own deadman) for the
  worker to genuinely finish before touching anything it owns. Verified
  both directions: with the fix defeated, `Close()` returned in
  microseconds regardless of an in-flight ~600ms dispatch; with it in
  place, `Close()` correctly waited for the dispatch to finish.
- **go-macos/metal bumped to v0.6.1** (dependency, 2026-11-08, round 12):
  closes an `objc.AutoreleasePool` gap in 8 functions
  (`Default`/`Device.Close`/`Device.Compile`/`Library.Close`/
  `Library.Pipeline`/`Pipeline.Close`/`Device.NewBuffer`/`Buffer.Close`)
  matching the exact signature of a native SIGSEGV go-wasmcontainers
  reported from concurrent per-Worker WebGPU calls — a goroutine migrating
  OS threads mid-Objective-C-call, the same documented bug class the
  SAME file's own `submit`/`Wait` already guard against. Not reproduced
  locally despite a dedicated 96,000-op concurrent stress test (consistent
  with the race being machine-dependent); the gap itself was real and is
  now closed at the library layer. See go-macos/metal#17.
- **`Options.MaxRequests`/`Engine.MaxRequests`** (feature, 2026-11-08,
  round 11): go-wasmcontainers' own real workload fetches on the order of
  123 ONNX models plus their inputs/outputs per render — several hundred
  requests — which hit the previous hardcoded 100-request cap with no way
  to raise it. Now configurable, zero selects the unchanged default, the
  SAME convention `MaxResponseBytes`/`MaxWasmModuleBytes`/
  `MaxWasmMemoryBytes` already follow.
- **WebGPU error scopes now actually CAPTURE errors** (fix, 2026-11-08,
  round 10): round 9's `pushErrorScope`/`popErrorScope` only got the
  push/pop balance right — every GPU error still threw synchronously,
  which go-wasmcontainers caught immediately: per WebGPU's own error
  model (§22), `create*`/`submit`/`dispatch` NEVER throw for a GPU
  content-validation failure (bad WGSL, a dispatch missing a bind group,
  an out-of-range `writeBuffer`) — the error is captured by the innermost
  open scope matching its class, or fires `"uncapturederror"` with none
  open, and the call itself still returns. A real, WebIDL-aware
  distinction is kept throughout: an actual argument-shape problem
  (missing descriptor, wrong type) still throws a synchronous `TypeError`
  regardless of any open scope — only GPU content validation is captured.
  `GPUValidationError`/`GPUOutOfMemoryError` objects are real, with their
  own `.constructor.name`, matching exactly what go-gfx/webgpu reads off
  a popped error. `createComputePipelineAsync` deliberately bypasses
  scopes entirely and rejects its own promise directly on a compile
  failure — a documented special case in spec, not an oversight.
- **WebGPU: feature/limit iterables, error scopes, explicit bind group
  layouts, `copyBufferToBuffer`** (feature, 2026-11-07, round 9): with
  Go's `net/http` fetch working (round 8), the next stop was WebGPU
  itself — go-gfx/webgpu panics reading `adapter.features`/
  `device.features` with `.values()` (`GPUSupportedFeatures` is
  `setlike<DOMString>`: `has`/`size`/`values`/`keys`/`entries`/`forEach`/
  `Symbol.iterator`, and only the first two existed). A follow-up
  measurement, each item checked independently, narrowed the remaining
  gaps to exactly four: the features iterable, `device.pushErrorScope`/
  `popErrorScope` (push/pop balance was real and spec-correct from the
  start; actually CAPTURING an error instead of still throwing past the
  scope took one more day — see round 10 below, which go-wasmcontainers'
  own report caught immediately), the `GPUShaderStage` namespace (never installed, unlike its
  `GPUBufferUsage`/`GPUMapMode` siblings), and `device.createBindGroupLayout`
  (which also blocked `createPipelineLayout`/`createComputePipelineAsync`/
  the mapAsync-readback item behind it). Explicit (non-`"auto"`) pipeline
  layouts are now supported — confirmed needed because go-wasmcontainers'
  own batch compute path binds a loop-budget group next to the guest's
  own groups, which a single pipeline's inferred `"auto"` layout cannot
  express. `GPUCommandEncoder.copyBufferToBuffer` is new too, with a real
  correctness subtlety worth citing: executing it as a plain Go `copy()`
  the instant it is RECORDED (rather than in the order commands actually
  run on the GPU) could read stale, pre-dispatch bytes, since Metal's own
  command buffer only executes once a `dev.Run` closure returns — fixed
  by flushing (and blocking on) every pending dispatch before each copy,
  rather than batching an entire submission into one `dev.Run` call.
- **`Headers.entries()`/`.keys()`/`.values()`, and `AbortController`**
  (feature, 2026-11-07, round 8): with Go actually running inside both
  workers (round 7), the next stop was its first `net/http` fetch —
  `net/http/roundtrip_js.go` panics on `headers.entries()`, which did not
  exist, and constructs `new AbortController()` before every fetch, which
  did not exist as a global at all. `Headers` now implements the Fetch
  Standard's own pair-iterable (`entries`/`keys`/`values`, sorted and
  combined by name, plus `Symbol.iterator` defaulting to `entries()` so
  `for...of` works directly over a `Headers` object). `AbortController`'s
  `.signal` is real (`.aborted`/`.reason`/`.onabort`/`throwIfAborted`);
  this engine's `fetch()` is always synchronous by construction, so there
  is no genuinely in-flight request to cancel mid-flight — stated
  directly rather than faked — but a signal already aborted BEFORE
  `fetch()` is called now correctly rejects immediately with its reason.
- **`WebAssembly.Instance`/`.Module` identity, and a real
  `performance.now()`** (fix, 2026-11-07, round 7): round 6's fetch fix
  unblocked go-wasmcontainers' probe; the next run found both inside the
  Worker a guest runs in. Go's own `wasm_exec.js` does `instance
  instanceof WebAssembly.Instance`, which threw outright (not just
  `false`) when neither global existed — now both are real constructors
  with their own `.prototype`, stamped onto every `compile()`/
  `instantiate()` result; the deliberately-unsupported synchronous
  constructor forms now throw a clear `TypeError` instead of not existing
  at all. `performance.now()` was a pure stub (`return 0`, for the main
  document too, not Worker-specific) masked by an existing test's own
  exact-string assertion pinning the wrong output — go-wasmcontainers'
  probe measured a 2,000,000-iteration loop between two calls giving the
  identical value. Fixed with a real per-binder `timeOrigin`
  (`time.Since` from it), since `wasm_exec.js` uses `performance.now()`
  for Go's own monotonic clock — under the stub, elapsed time inside a
  guest never advanced AT ALL, however long it actually ran.
- **`fetch()` no longer silently truncates a response over 8 MiB** (fix,
  2026-11-07, round 6): found investigating a SEPARATE go-wasmcontainers
  report that a ~10 MiB plain Go-compiled wasm module "did not report
  within 5 min and logged nothing at all" via `instantiateStreaming`. A
  real ~9 MiB fixture compiled through wazero directly in 1.6s — not slow
  at all — so the real cause was upstream: `io.ReadAll(io.LimitReader(...,
  8<<20))` returns a **nil error** whether a body was exactly 8 MiB or far
  larger and simply cut off, so ANY fetch over 8 MiB (a WASM module, an
  image, JSON) used to come back looking like a successful response
  carrying silently truncated, corrupt bytes. `doRequest` now reads one
  byte past the limit and REJECTS outright, naming the real cause and the
  exact limit, if that extra byte arrived — never a truncated "success".
  The cap itself is now configurable (`Engine.MaxResponseBytes`/`Options.
  MaxResponseBytes`, zero selects the unchanged 8 MiB default), the SAME
  convention `MaxWasmModuleBytes`/`MaxWasmMemoryBytes` already follow, so
  a caller with legitimately larger resources raises it explicitly.
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
  throw from `createComputePipeline`. `layout: "auto"` was the only
  supported form at first — `wgslmsl` already infers the real binding
  layout from the shader source itself — until round 9 (below) added
  explicit `createBindGroupLayout`/`createPipelineLayout`. A stated gap
  inherited from `go-macos/metal`, not from this binding's own
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
  naming the missing import. `WebAssembly.Module`/`WebAssembly.Instance`
  exist as real constructors with their own `.prototype` (so `instanceof`
  holds for `compile()`/`instantiate()` results — round 7, 2026-11-07,
  confirmed needed by Go's own `wasm_exec.js`), but calling either
  SYNCHRONOUSLY via `new` still throws a `TypeError` — no confirmed
  container uses that form over the async `compile()`/`instantiate()`.
  Table and Global imports/exports, and standalone `Table` objects are not
  implemented — no confirmed container needs any of them yet.
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
