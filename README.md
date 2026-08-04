<p align="center"><img src="https://raw.githubusercontent.com/go-webengine/brand/main/social/go-webengine.png" alt="go-webengine/engine" width="720"></p>

# go-webengine / engine

[![CI](https://github.com/go-webengine/engine/actions/workflows/ci.yml/badge.svg)](https://github.com/go-webengine/engine/actions/workflows/ci.yml)
![coverage](https://img.shields.io/badge/coverage-97%25%2B%20ratchet-brightgreen)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-webengine/engine.svg)](https://pkg.go.dev/github.com/go-webengine/engine)
[![Docs](https://img.shields.io/badge/docs-mkdocs--material-0079A8)](https://go-webengine.github.io/docs/)
[![License: BSD-3-Clause](https://img.shields.io/badge/License-BSD--3--Clause-blue.svg)](LICENSE)
[![Go 1.26.4+](https://img.shields.io/badge/Go-1.26.4%2B-00ADD8?logo=go)](https://go.dev/dl/)

A pure-Go, **`CGO_ENABLED=0`** headless web engine: it fetches a URL, parses the
HTML into a DOM, applies a minimal-but-real CSS subset (cascade + inheritance),
lays the content out in block-and-inline flow, and paints anti-aliased text,
backgrounds and images to an `image.RGBA` — **no Chromium, no cgo, no host web
view**. Give it a URL, get back an image of the page.

Phase 0 is a *static* renderer (no JavaScript). It is the rendering core of the
[wasmdesk](https://github.com/wasmdesk) **browserproxy** roadmap: a service that
renders pages server-side and streams frames to the `clients/browser` front-end.

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

The viewport width is fixed; the height grows to fit the whole page (at least the
viewport height). There is also a CLI:

```
go run ./cmd/render -url https://example.com/ -out out.png -w 1024 -h 768
```

## Pipeline

```
Fetch (go-browserhttp)  →  Parse (x/net/html → dom)  →  Cascade (css)
     →  Layout: block + inline flow, word-wrap (layout)
     →  Paint: AA text (go-opentype) + backgrounds (go-widgets/painter) + images (go-images)
     →  image.RGBA  →  PNG
```

| Package | Role |
|---|---|
| `dom` | Owned DOM node tree built from `golang.org/x/net/html`. |
| `css` | Minimal-but-real CSS: value model, stylesheet/declaration parser, tag/class/id selectors + specificity, UA stylesheet, cascade + inheritance. |
| `layout` | Block-and-inline flow, greedy word-wrap line-breaker, `white-space: pre`, driven by a `Measurer` interface (font-free, exactly testable). |
| `paint` | Rasterises the box tree to `*image.RGBA`; also the real `Measurer` (go-opentype faces). |
| `engine` (root) | `Fetch`, `Render`, `Screenshot`, `RenderInfo`, image loading. |
| `cmd/render` | CLI: `render -url URL -out shot.png -w 1024 -h 768`. |

Everything reused is pure-Go and BSD/MIT — see [`SURVEY.md`](SURVEY.md) for the
prior-art verdict (opossum/mycel studied, not built on) and the full
reuse-vs-build decision.

## What works / What doesn't yet

This is an honest Phase-0 **static** renderer. The full per-feature and
per-page assessment (three live pages, committed golden PNGs) is in
[`FIDELITY.md`](FIDELITY.md). Short version:

**Works today**
- HTML → DOM → block/inline flow at a real viewport width.
- UA default styling; author CSS from `<style>` and inline `style=`, with
  cascade, specificity (inline > id > class > tag) and inheritance.
- Colours (named, `#rgb`, `#rrggbb`, `rgb()/rgba()`), `background-color`, the
  page backdrop extended over the viewport.
- Anti-aliased proportional text (go-opentype) at the cascaded size driving
  greedy word-wrap; serif / sans / mono; faux-bold for weight ≥ 600; complex
  scripts (Cyrillic, Vietnamese, …).
- `white-space: pre`; `<img>` best-effort (http(s) + `data:`, PNG/JPEG).

**Not yet (Phase 0, by design)**
- **No JavaScript** — script-rendered content is blank or skeletal.
- **No float / flex / grid / table / positioning** — pages linearise to a single
  column; sidebars, nav bars and infoboxes stack vertically.
- No `max-width`/`min-width`, `line-height`, `border`, `box-shadow`,
  backgrounds images/gradients, `@media`, web fonts.
- Selectors: tag/class/id compound only (no combinators, attribute or pseudo
  selectors); italic faces are not rendered.

JavaScript (via [goja](https://github.com/dop251/goja)) and real box-model
layout (float/flex/grid) are the Phase-1+ roadmap; see the
[docs](https://go-webengine.github.io/docs/).

## Test

```
CGO_ENABLED=0 go build ./...          # cgo-free build
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test -short ./...     # -short skips the live-network render
bash scripts/coverage-gate.sh          # ratchet coverage gate (see below)
```

The pure logic (cascade/inheritance, line-breaker, box metrics, DOM) is asserted
at exact geometry; a committed golden PNG covers the offline paint path.
`scripts/coverage-gate.sh` enforces a **ratchet** coverage floor per pure-logic
package — currently css 99.5% / layout 98.4% / paint 97.7% / dom 97.4% — which
CI fails below and which is raised (never lowered) toward 100% as the engine
matures. The live-network paths (root `engine` package, `cmd/render`) are
excluded from the gate because their coverage is not reproducible in CI. `go.mod`
floor is `go 1.26.4`.

## Links

- Landing: <https://go-webengine.github.io/>
- Documentation: <https://go-webengine.github.io/docs/>
- Fidelity report: [`FIDELITY.md`](FIDELITY.md) · Prior-art survey: [`SURVEY.md`](SURVEY.md)

## License

BSD-3-Clause — see [`LICENSE`](LICENSE). Copyright (c) the go-webengine/engine
authors.
