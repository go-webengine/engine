# Survey — Pure-Go Headless Browser Engine

**Date: 2026-08-04** (prior-art verdict) · **Updated: 2026-08-05** (status notes).

Goal: a CGO=0 HTML/CSS layout + paint engine that renders a real web page to an
image, no Chromium. This document records the prior-art verdict and the
reuse-vs-build decision the engine is built on. It was written at Phase 0; the
verdicts still hold, and inline **Status (2026-08)** notes below record where the
shipped engine has since moved past what Phase 0 assumed (JavaScript now runs via
goja; text uses real bold + italic faces). Current capabilities live in
[`README.md`](README.md), [`FIDELITY.md`](FIDELITY.md) and
[`bench/REPORT.md`](bench/REPORT.md).

## Prior pure-Go browser engines: opossum / mycel

**Verdict: study it, do not build on it.**

- `github.com/psilva261/opossum` was **renamed** to `github.com/psilva261/mycel`
  (same author `psilva261`, same code, opossum redirects to mycel). They are one
  project, not two, and not a fork — treat as a single reference.
- **License: BSD-3-Clause** — compatible with ours. **CGO-free.**
- Composition (the instructive part): `golang.org/x/net/html` for HTML5 parsing,
  `github.com/andybalholm/cascadia` for CSS-selector matching, and
  `github.com/tdewolff/parse/v2` for CSS/HTML tokenizing. This is a clean,
  permissive, pure-Go parse/selector/tokenize stack worth copying.
- **Rendering backend is `github.com/mjl-/duit`** — a Plan 9 pixel-UI toolkit —
  and the DOM is exposed as a **9P filesystem**. Both are dead weight for a
  *headless* engine and are baked into the codebase's core assumptions.
- **JS is a separate WIP module `sparklefs`** (a fork of goja), off by default
  (`-jsinsecure`). Prefer upstream `goja` directly over a stale fork.
- **Layout is the weak spot and is explicitly stub-quality**: the README states
  float/flex layout are "just stub implementations". The single hardest piece of
  a browser — the one we most want to inherit — is exactly what opossum has
  nothing to give us.
- Low-activity solo experimental project: depending on it means owning its
  bus-factor and bugs.

**Conclusion:** clean-room the layout/box/flow engine (opossum can't help there),
but reuse the same permissive building blocks it wisely chose.

## JS engine

- `github.com/dop251/goja` — pure-Go, **no cgo**, **MIT**. ES5.1 fully + most of
  ES2015 (classes, arrow fns, destructuring, template strings, Promises,
  generators). Actively maintained; the credible pure-Go JS engine. **Status
  (2026-08): shipped.** goja is now a direct dependency; the `js` package binds it
  to a real DOM with `fetch()`/XHR and laid-out-geometry read-back, and a
  settle-then-render loop reflects script-driven DOM mutations in the output.

## Supporting libraries (all permissive, all pure-Go)

| Library | License | Role | Copyleft? |
|---|---|---|---|
| `golang.org/x/net/html` | BSD-3 | HTML5 tokenizer + tree builder | none |
| `andybalholm/cascadia` | BSD-2 | CSS selector matching over x/net/html | none |
| `tdewolff/parse/v2` | MIT | CSS/HTML/JS streaming tokenizers | none |
| `vanng822/css` | MIT | simple CSS stylesheet AST | none |
| `go-shiori/go-readability` | MIT | main-content/metadata extraction | none |
| `dop251/goja` | MIT | ES5.1+ES6 JS engine | none |

No GPL/LGPL/MPL anywhere — every candidate is BSD-2/BSD-3/MIT, all compatible
with our BSD-3-Clause with standard attribution only.

## Reuse from this ecosystem's own orgs (preferred, verified via `go doc`)

| Module | Version | What we use it for |
|---|---|---|
| `github.com/go-browserhttp/browserhttp` | v0.1.0 | `NewClient(timeout) *http.Client` — Chrome TLS fingerprint, cookie jar, redirects. Our `Fetch` uses it. |
| `github.com/go-opentype/opentype` | v0.5.0 | `Face.Measure` (advance widths for line-breaking), `Face.GlyphMask` (8-bit AA coverage for text paint), `Face.Metrics` (ascent/height). FreeType-parity AA. |
| `github.com/go-opentype/fonts` | v0.5.0 | Embedded OFL fonts: `inter` (sans), `lora` (serif), `gomono` (mono), with **real bold and italic** static faces. |
| `github.com/go-images/images` | v0 | `Decode` (PNG/JPEG → RGBA) + `Resize` for `<img>`. |
| `github.com/go-widgets/painter` | v0.2.0 | `PixelPainter.FillRect` / `PushClip` for backgrounds onto the `image.RGBA` buffer. |

Notes discovered during API inspection:
- `painter.PixelPainter.Text` uses a fixed built-in fallback font (not sized), so
  it is **not** used for body text. Sized proportional text is rendered by
  blitting `opentype` glyph masks directly onto the RGBA buffer (src-over alpha).
- The bundled `fonts` families now embed **real regular / bold / italic** static
  faces. **Status (2026-08):** the earlier faux-bold synthesis (blit the glyph
  mask offset by 1px in x) has been **removed** in favour of the real bold and
  italic faces; the paint layer selects the face for the cascaded weight/style.
- `go-images.Decode` handles PNG + JPEG only (no GIF/WEBP). **Status (2026-08):**
  SVG is now rendered separately via `oksvg` + `rasterx` (`<img *.svg>`,
  `data:image/svg+xml`, inline `<svg>`); other unsupported/failed decodes still
  skip best-effort.

## What we reuse vs build

**Reuse:** HTTP (go-browserhttp), HTML parse (x/net/html), text measure+raster
(go-opentype + fonts), backgrounds (go-widgets/painter), image decode/resize
(go-images), JavaScript (goja — now shipped), SVG rasterisation (oksvg +
rasterx — now shipped).

**Build (clean-room, because prior art can't give it to us):**
- Our own DOM node tree (`dom`) — a thin, owned wrapper over x/net/html.
- A minimal but real **CSS cascade + inheritance** (`css`) — UA default stylesheet,
  `<style>` + inline `style=`, tag/class/id selectors with specificity. We write
  our own small tokenizer/parser here rather than pulling tdewolff/vanng822, for
  full control and to make the pure cascade logic 100%-coverable. (cascadia /
  tdewolff remain the drop-in upgrade path when the selector set grows.)
- A **block + inline flow layout engine** (`layout`) with a greedy word-wrap
  line-breaker, driven through a `Measurer` interface so the geometry is unit-
  testable without fonts.
- The **paint** orchestration (`paint`) and the public `Render`/`Screenshot` API.

License posture: everything above is BSD/MIT/BSD-2 — all compatible with our
BSD-3-Clause. New files carry `Copyright (c) the go-webengine/engine authors`.
