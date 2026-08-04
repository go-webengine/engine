# go-webengine/engine — pure-Go static web renderer (Phase 0)

A CGO=0, pure-Go HTML/CSS layout + paint engine that fetches a URL and renders a
real web page to an image — **no Chromium, no cgo**. Phase 0 is a *static*
renderer (no JavaScript). Long-term this feeds a "browserproxy" that streams
rendered frames to the wasmdesk `clients/browser`.

> Module path `github.com/go-webengine/engine` is a **placeholder** pending the
> confirmed org name. Nothing is pushed; this is a local Phase-0 slice.

## Pipeline

```
Fetch (go-browserhttp)  →  Parse (x/net/html → dom)  →  Cascade (css)
     →  Layout: block + inline flow, word-wrap (layout)
     →  Paint: AA text (go-opentype) + backgrounds (go-widgets/painter) + images (go-images)
     →  image.RGBA  →  PNG
```

## Packages

| Package | Role |
|---|---|
| `dom` | Owned DOM node tree built from `golang.org/x/net/html`. |
| `css` | Minimal-but-real CSS: value model, stylesheet/declaration parser, tag/class/id selectors + specificity, UA stylesheet, cascade + inheritance. |
| `layout` | Block-and-inline flow, greedy word-wrap line-breaker, `white-space: pre`, driven by a `Measurer` interface (font-free, exactly testable). |
| `paint` | Rasterises the box tree to `*image.RGBA`; also the real `Measurer` (go-opentype faces). |
| `engine` (root) | `Fetch`, `Render`, `Screenshot`, `RenderInfo`, image loading. |
| `cmd/render` | CLI: `render -url URL -out shot.png -w 1024 -h 768`. |

## Usage

```go
img, info, err := engine.Render(ctx, "https://example.com/", image.Rect(0, 0, 1024, 768))
// info.Title, info.URL, info.ContentHeight
png, err := engine.Screenshot(ctx, "https://example.com/", image.Rect(0, 0, 1024, 768))
```

```
go run ./cmd/render -url https://example.com/ -out out.png -w 1024 -h 768
```

## Reuse (all BSD/MIT, all pure-Go — see `SURVEY.md`)

`go-browserhttp` (HTTP), `x/net/html` (parse), `go-opentype` + `go-opentype/fonts`
(text measure + AA raster), `go-widgets/painter` (backgrounds), `go-images`
(image decode/resize). Prior pure-Go browser `opossum`/`mycel` was studied but
**not** used as a base (its layout core is stub-quality and Plan 9 / duit / 9P
coupled); `goja` is the intended JS engine for Phase 1.

## Scope

Supported and unsupported CSS/features, plus an honest per-page assessment of
three live renders, are documented in **`FIDELITY.md`**. Short version: content,
text, colours, links, `<pre>` and basic images render; float/flex/grid, most
positioning, and all JavaScript do not (Phase 0).

## Test

```
CGO_ENABLED=0 go test ./...          # all green
CGO_ENABLED=0 go build ./...         # CGO=0 clean
```

Pure logic (cascade/inheritance, line-breaker, box metrics) is asserted at exact
geometry with ~98–99% coverage; a committed golden PNG covers the offline paint
path. `go.mod` floor is `go 1.26.4`.

## License

BSD-3-Clause — see `LICENSE`. Copyright (c) the go-webengine/engine authors.
