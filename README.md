<p align="center"><img src="https://raw.githubusercontent.com/go-webengine/brand/main/social/go-webengine.png" alt="go-webengine/engine" width="720"></p>

# go-webengine / engine

[![CI](https://github.com/go-webengine/engine/actions/workflows/ci.yml/badge.svg)](https://github.com/go-webengine/engine/actions/workflows/ci.yml)
![coverage](https://img.shields.io/badge/coverage-97%25%2B%20ratchet-brightgreen)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-webengine/engine.svg)](https://pkg.go.dev/github.com/go-webengine/engine)
[![Docs](https://img.shields.io/badge/docs-mkdocs--material-0079A8)](https://go-webengine.github.io/docs/)
[![License: BSD-3-Clause](https://img.shields.io/badge/License-BSD--3--Clause-blue.svg)](LICENSE)
[![Go 1.26.4+](https://img.shields.io/badge/Go-1.26.4%2B-00ADD8?logo=go)](https://go.dev/dl/)

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

The full per-feature and per-page assessment (five live pages, committed golden
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
- **Text**: anti-aliased proportional text (go-opentype) with **real bold and
  italic faces** (no faux-bold), serif / sans / mono, complex scripts (Cyrillic,
  Vietnamese, …); `white-space: pre`.
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

- No `conic-gradient`, CSS `filter` or `mask`; SVG has no `<filter>`/`<mask>`/
  `<pattern>`/embedded `<image>`/`<text>`, and a per-page image budget caps very
  icon-heavy pages.
- No `<li>` `list-style` marker discs yet; some icon-font / `visually-hidden`
  chrome renders as text where a browser shows an icon.
- Large computed pages (pkg.go.dev, go.dev) render **slower** than Chrome — an
  open perf gap, not a fidelity one.
- This is **not** a standards-complete browser and **not** "as good as Chromium".
  Measured mean windowed-SSIM across the five bench pages is **≈ 0.69**, with
  clear diminishing returns; the Wikipedia number (≈ 0.44) is JS-confounded and
  noisy. See the numbers below.

## Measured fidelity vs headless Chrome

From [`bench/REPORT.md`](bench/REPORT.md) (windowed SSIM over the common
top-left region, 1024px width; `speed×` = `chrome_ms / webengine_ms`, >1 = faster;
timings include the live network fetch and vary with it):

| URL | SSIM | pixdiff % | speed× | note |
|-----|-----:|----------:|-------:|:-----|
| example.com/ | **0.954** | 1.5 | 34.8 | near-parity, ~35× faster |
| react.dev/ | 0.727 | 26.4 | 1.15 | SPA; gradients + React SVG atom render |
| go.dev/blog/ | 0.670 | 33.2 | 0.34 | dark-mode + SVG logos render; slower |
| pkg.go.dev/net/http | 0.629 | 36.5 | 0.14 | large computed page; perf gap |
| en.wikipedia.org/wiki/Go | 0.441 | 22.4 | 1.20 | JS-confounded, noisy metric |

`example.com` is at near-parity and much faster; the JS-heavy and large computed
pages are the honest frontier. Re-run the harness with `cd bench && go run
./cmd/compare -urls urls.txt` (needs a Chrome/Chromium binary).

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
pure-logic package (`css`/`layout`/`paint`/`dom`), which CI fails below and which
is raised (never lowered) toward 100% as the engine matures. The live-network
paths (root `engine` package, `cmd/render`) are excluded from the gate because
their coverage is not reproducible in CI. The `bench/` fidelity harness is a
separate nested module (it pulls chromedp) and is not in the CGO=0 six-arch CI.
`go.mod` floor is `go 1.26.4`; cross-built for all six 64-bit Go targets.

## Links

- Landing: <https://go-webengine.github.io/>
- Documentation: <https://go-webengine.github.io/docs/>
- Fidelity report: [`FIDELITY.md`](FIDELITY.md) · Benchmark: [`bench/REPORT.md`](bench/REPORT.md) · Prior-art survey: [`SURVEY.md`](SURVEY.md)
- Remote-browser service: [`browserproxy`](https://github.com/go-webengine/browserproxy)

## License

BSD-3-Clause — see [`LICENSE`](LICENSE). Copyright (c) the go-webengine/engine
authors.
