# Fidelity Report

**Date: 2026-08-05**

Honest assessment of the renderer on five pages. Phase 0 shipped a static
HTML→CSS→block/inline→paint→PNG pipeline that **linearised every page to one
column**. Phase 1 adds the real positioning layer — box model completeness,
vertical margin collapsing, floats + clear, basic flexbox, basic tables, painted
borders, `@media` width queries, and selector combinators. This is still a
**static** renderer with **no JavaScript**; anything a page paints via scripting
is absent, and that is stated below, not hidden.

The committed PNGs under `testdata/renders/` back every claim here. Reproduce
them with the commands at the bottom.

## Phase 2.3 — CSS `:checked` + `:not()` (the checkbox-hack that collapses MediaWiki's dropdowns)

**Date: 2026-08-05.** Phase 2.2's honest residual was that Wikipedia's collapsed
Vector-2022 **dropdown contents** still painted, because the CSS checkbox-hack was
not honoured. This phase closes it with three selector features (`css/selector.go`):

- **Sibling combinators `~` / `+`** — already matched (Phase 1.7); reconfirmed with
  positive + negative tests. They were never the gap.
- **`:checked`** — at static render an element is checked iff it carries the default
  `checked` attribute (checkbox/radio) or is an `<option selected>`; there is no
  interaction to toggle it. A default-unchecked toggle therefore does **not** match.
- **`:not(...)`** — negation over a simple/compound selector or a comma list
  (including `:not(:checked)`); the compound matches only when no negated selector
  matches. Critically, an argument the engine cannot model (an attribute-only
  `:not([attr])`, a pseudo-element, an empty `:not()`) imposes **no constraint**
  rather than dropping the rule — the same "reduce, don't drop" rule already used
  for unknown pseudos and bare `[attr]`. `:not(:hover)` (never matches statically)
  also imposes no constraint.
- A **paren/bracket-aware tokenizer + pseudo splitter** so the space/comma in
  `:not(.a, .b)`, the nested `:` in `:not(:checked)`, and a `:` inside an attribute
  value are all literal (not split points).

The bug was that `:checked` and `:not(:checked)` were previously *dropped*, so
MediaWiki's paired reveal (`…:checked ~ … {display:block}`) and hide
(`…:not(:checked) ~ … {display:none}`) rules both collapsed to the same selector
and the reveal won — the panel rendered open. Now the unchecked toggle fails
`:checked` and matches `:not(:checked)`, so the panel defaults to hidden.

**Regression found and fixed along the way (measured).** A first cut made a
`:not()` with an unmodelled argument *drop the whole selector*. That silently
disabled the dark theme on go.dev / pkg.go.dev, whose design system gates its
dark custom properties on `:root:not([data-theme])`-shaped rules — dropping that
rule fell the page back to **light** while Chrome renders **dark** (a full-page
inversion, SSIM ≈ 0.24). The fix is the "reduce, don't drop" rule above: the
unmodelled attribute negation degrades `:root:not([data-theme])` to `:root`, so
the rule still applies. Verified offline: after the fix `go.dev/blog` renders its
`#202224` dark background again (it was `#ffffff` white before). This is exactly
the "don't silently drop rules it shouldn't" hazard the task flagged.

**Wikipedia verdict — montage-verified.** A cropped top-left montage compare (this
change vs the committed baseline render) confirms the expanded "Main menu / Main
page / Contents / Current events / Random article / …" list and the right-hand
"Tools" list are **gone** — the render matches Chrome's collapsed state. That is
the deliverable, and it holds. **Residual (honest):** the small *text labels* of
the collapsed toggles ("Main menu", "Toggle the table of contents", the skip link)
still render as text where Chrome shows icons — an icon-font / `visually-hidden`
gap, not the checkbox hack. The Wikipedia **windowed SSIM** is the documented
noisy metric (it swings run-to-run on the variable infobox-logo region); across
interactive runs it measured 0.41–0.45 around the 0.441 baseline, i.e. flat within
its noise band while the *targeted* defect (open dropdown lists) is visibly fixed.
**The full 5-URL bench was not re-run/committed in this pass** (`bench/` is
unchanged); the feature's correctness rests on the deterministic goldens below and
the montage inspection, not on the noisy live number.

### Proven deterministically (offline golden, JS disabled)

- `checkbox_hack.html` → `TestCheckboxHackGolden` — a `display:none` menu that a
  default-unchecked `:checked` toggle leaves **hidden**, the same menu **shown**
  when the toggle carries `checked`, and a `:not(:checked) ~ x` inverse hiding a
  menu while unchecked. Asserts the FINAL rendered pixels (solid-colour blocks,
  font-independent). Plus exact unit tests in `css/selector_test.go` for every new
  branch (including malformed `:not()`). `layout`/`paint` 100%; `css` ≥ 99.5% floor.

## Phase 2.2 — dynamic rendering (layout↔JS feedback + settle-then-render)

**Date: 2026-08-05.** Phase 2 ran JavaScript, but only *before* any layout, so a
page whose final visual state is produced at runtime by JS reading layout metrics
could never reach it. This phase adds the feedback loop.

**1. Real layout metrics to JS.** After an initial cascade+layout, the JS DOM
returns real numbers instead of stubs: `getBoundingClientRect()` (x/y/width/
height/top/left/right/bottom), `offsetWidth/Height`, `clientWidth/Height`,
`scrollWidth/Height`, `offsetTop/Left` (relative to the offset parent), and
`window.getComputedStyle(el)` resolving **used** values (width/height/display/
position/margins/padding/font-size/line-height/color/…). These read from the
actual laid-out box tree via a new `layout.BuildIndex` (border-box rect per
element; inline elements get the union of their fragments). `window.innerWidth/
innerHeight` were already wired. This is what responsive scripts and MediaWiki's
`mw.loader` consult to decide what to collapse or reveal.

**2. Dynamic `<script>` execution.** A `<script>` inserted into the DOM at runtime
(inline text or `src`) is fetched through `e.Client` (so the SSRF guard applies)
and executed in the same goja runtime, in document order, once each — the
ResourceLoader mechanism. Bounded by the existing script-count / byte / wall-clock
limits.

**3. Injected `<style>`/`<link>` + inline-style mutations take effect.** The
re-cascade re-collects `<style>` text and `<link>` sheets from the *mutated* DOM
(not a stale snapshot), so `document.createElement('style')`+append, `el.style.*`
writes, `setAttribute('class'|'style')` and `classList` changes all show up on the
next layout. Injected `<link>` sheets are refetched only when the link set
changes.

**4. Settle-then-render.** `RenderDocument` now: initial cascade+layout → run
scripts + drain the async loop (timers/promises/fetch/XHR + injected scripts) →
re-cascade + re-layout → **iterate to a bounded fixpoint** (≤3 passes; a pass runs
only when a DOM/attribute/text signature changed, so a page whose JS mutates
nothing pays no extra layout). Deterministic and always terminating: a pass cap
plus a wall-clock deadline guard (an expensive page keeps its pre-settle layout
rather than risk a timeout). A budget-gated final resource refresh reloads
theme-specific images/backgrounds the pre-JS layout never fetched.
`DisableJS` bypasses the whole loop for the byte-deterministic offline fixtures.

### Proven deterministically (offline goldens, JS enabled)

- `dynamic_metrics_toggle.html` — a script reads `offsetWidth === 200` and toggles
  a class that grows+recolours a box. The rendered pixels show the widened box;
  the `DisableJS` control does not. Proves **real metric read-back drives layout**.
- `dynamic_style_inject.html` — `createElement('style')` + `appendChild` injects a
  rule that recolours/resizes an element; the next cascade re-collects it.
- `dynamic_script_inject.html` — a dynamically appended inline `<script>` executes
  and mutates the DOM; the mutation reaches the final paint.

### Measured effect (see `bench/REPORT.md`)

**Wikipedia (the target): 0.413 → 0.441 SSIM, pixdiff 23.0% → 22.4%.** Instrumented,
`mw.loader` now runs — 3 scripts injected at runtime and executed, 0 errors — and
sets Vector-2022 to its collapsed/unpinned feature-class state (Chrome's mode),
realigning the article to the top. **Residual (honest):** the collapsed
main-menu dropdown's *contents* still paint on the left — a **CSS selector gap**
(the `#…-checkbox ~ .vector-unpinned-container { display:none }` checkbox-hack is
not applied), not a JS or ES-modules gap. example.com/pkg.go.dev/go.dev hold;
react.dev is flat within its live-SPA noise band; no page regresses. Timing rises
(one or two extra layouts for JS-mutating pages) but stays well within budget.

## Phase 2.1 — SVG rendering (`<img src=*.svg>`, `data:image/svg+xml`, inline `<svg>`)

**Date: 2026-08-05.** The next-ranked visible gap was **SVG**: the react.dev React
atom logo (an inline `<svg>`) and any SVG raster/logo art rendered **blank**
because go-images only decodes PNG/JPEG. This phase adds a real, pure-Go SVG
rasteriser and wires it into both the `<img>` image path and inline `<svg>`.

### SVG-library survey and decision (measured, not assumed)

The brief said reuse a maintained CGO=0 rasteriser, not hand-write one. Candidates
evaluated:

| Library | CGO | License | Verdict |
|---------|-----|---------|:-------|
| **`github.com/srwiley/oksvg` + `github.com/srwiley/rasterx`** | **0** | **BSD-3-Clause** | **chosen** |
| `github.com/tdewolff/canvas` | 0 for the SVG path, but pulls a large multi-backend surface (PDF/HTML/fonts, some rasterisers need cgo) | MIT | heavier; more than needed |

**Chosen: `oksvg` + `rasterx` (both BSD-3-Clause, both pure-Go / CGO=0).**
Reasons: it is the standard Go SVG-icon rasteriser, licence-compatible with our
BSD-3 (no copyleft), a tiny two-package surface, and — verified empirically — it
renders real SVGs correctly: **paths, basic shapes, fills, strokes, linear and
radial gradients (`url(#id)`), transforms and viewBox scaling** to an `image` we
composite. A 100×100 test SVG rasterised at 2× target confirmed gradient
interpolation (top-left `#5cd6f7`, bottom-right `#0e82a8`), path fills and stroke
geometry, all under `CGO_ENABLED=0`, and it cross-builds on all six 64-bit arches
(amd64/arm64/ppc64le/riscv64/loong64/s390x). No vendoring — a normal go.mod
dependency (BSD-3, "build from source").

**What oksvg does / doesn't do (documented, not faked):** it handles the drawing
subset above plus `currentColor` (we feed the element's computed CSS `color` via
`ReadReplacingCurrentColor`, so `fill="currentColor"` logos take their real
colour). It does **not** implement SVG `<filter>` effects, `<mask>`/`<clipPath>`
compositing, `<pattern>` fills, embedded raster `<image>`, `<text>`/font shaping,
CSS `<style>` selectors inside the SVG, or animation. Such features degrade to
"drawn without them" or empty, never a crash.

### What Phase 2.1 implements

- **`<img src="*.svg">` and `data:image/svg+xml`** (`images.go`, `svg.go`):
  SVG is detected by extension, the `image/svg` media hint, or an `<svg` content
  sniff. It is rasterised at the element's **used size** — CSS width/height win,
  then the HTML `width`/`height` presentation attributes, then the viewBox
  intrinsic size, with a single specified axis deriving the other from the
  viewBox aspect ratio; an image wider than the viewport is scaled down. A
  non-base64 `data:` payload is now **percent-decoded** (RFC 2397), which the
  previous code skipped — that alone was why `data:image/svg+xml,%3Csvg…` images
  were blank.
- **Inline `<svg>`** (`images.go`, `svg.go`, `layout/`): the `<svg>` subtree is
  **serialised back to SVG text** from the DOM (restoring the handful of
  mixed-case names the HTML parser/DOM lowercased — `viewBox`, `linearGradient`,
  `radialGradient`, `gradientUnits`, `gradientTransform`, `spreadMethod` — with
  deterministic attribute ordering and XML-escaped values), rasterised, and
  treated as a **replaced element** with intrinsic size from its
  width/height/viewBox. Layout emits it exactly like an `<img>` box, so paint
  composites it through the existing image-blit path (no new paint branch).
- **Graceful degradation + cost bounds**: a document that fails to parse or has
  no usable intrinsic size renders as empty space (a `recover` turns any
  rasteriser panic into a clean skip — rasterx does panic on some degenerate
  geometries); the rasterised bitmap is capped at 4096 px per axis.

### Before → after vs headless Chromium (this machine, OS-dark)

| URL | SSIM before | SSIM after | pixdiff before | pixdiff after | Verdict |
|-----|-----------:|-----------:|---------------:|--------------:|:-------|
| example.com/ | 0.954 | 0.954 | 1.5% | 1.5% | held (no SVG) |
| en.wikipedia.org/wiki/Go | 0.413 | 0.413 | 23.0% | 23.0% | held |
| pkg.go.dev/net/http | 0.628 | 0.628 | 36.5% | 36.5% | held exactly |
| go.dev/blog/ | 0.666 | **0.670** | 33.2% | 33.2% | **improved** (Go wordmark + gopher + social icons now render at the right size) |
| react.dev/ | 0.719 | **0.729** | 30.2% | **26.1%** | **improved** (the hero React atom + nav atom render) |

**react.dev — the target — improves: +0.010 SSIM, pixdiff 30.2%→26.1% (−4.1pp).**
The inline-`<svg>` React atom in the hero (a `currentColor` circle + three
`rotate()`-transformed orbital ellipses over a `viewBox="-10.5 -9.45 21 18.9"`)
and the small nav atom now render where Chrome paints them. go.dev/blog improves
slightly: its Go wordmark, the footer gopher and the Bluesky/Mastodon/GitHub
social icons are `<img src="*.svg">` that now rasterise — at their **own**
16×16/82×32 intrinsic size, not the raw viewBox (Bluesky's viewBox is 568×501),
which is the sizing rule this phase got right. example.com, Wikipedia and
pkg.go.dev hold exactly. The authoritative regenerated numbers live in
`bench/REPORT.md`.

**Honest remaining gaps.** oksvg renders the atom's orbits a touch thinner than
Chrome's and does not composite SVG `<filter>`/`<mask>`/`<pattern>`/embedded
`<image>`/`<text>`; a handful of react.dev's ~120 icons fall past the per-page
image budget (`MaxImages`); and an SVG whose display size lives only in CSS a
selector we don't resolve still falls back to its intrinsic size. None regress
the five pages.

### Tests

`testdata/svg_golden.html` is a text-free byte-golden fixture (inline `<svg>`
with a linear gradient, a solid fill, a `currentColor` stroke and a path fill,
its viewBox scaled 2×) behind `TestSVGGolden`, which asserts exact sampled
colours at known points (gradient endpoints, the solid rect, the path fill and
the `currentColor` stroke). `svg_test.go` unit-tests the glue to 100% statement
coverage including every error path: SVG sniffing, attribute-dimension parsing,
`currentColor` formatting, the used-size resolver, SVG serialisation/escaping,
parse-error and sizeless-SVG failures, the `recover` guard (via a substitutable
rasteriser seam), dimension clamping, and `data:` percent-decoding. End-to-end
tests cover inline-`<svg>` intrinsic-size layout, `<img>`-SVG attribute sizing
and a broken `<svg>` degrading to empty. `layout` and `paint` hold **100%**
statement coverage; `css` holds its floor.

## Phase 2.0 — CSS gradients, `background-image: url()`, box-shadow, opacity

**Date: 2026-08-05.** Phase 1.9 closed the dark/colour gap and left one ranked
backlog item (rank 5): the paint-only decorations — **gradients**,
**`background-image: url()`**, **box-shadow** and **opacity**. This phase
implements them, confirming the ranking first by pixel-sampling the Chrome
reference montages.

### Ranking confirmation (measured, before implementing)

Re-running the pre-change engine on this machine reproduced the committed numbers
exactly (react.dev **0.710**, go.dev/blog 0.666, pkg.go.dev 0.628, Wikipedia
0.423, example.com 0.954), and the react.dev montage shows the remaining
divergences precisely where the task predicted: the right-hand demo panels render
**flat grey** where Chrome paints **teal→blue→green gradients**, and the
rounded controls sit **shadowless**. Counting the real CSS confirmed the target:
react.dev's stylesheet has **4 `linear-gradient` + 1 `radial-gradient`**, **39
`box-shadow`** and **2 `background-image:url()`** declarations. So gradients +
`url()` backgrounds were implemented first, then box-shadow and opacity. (The
big centre React atom is an **inline `<svg>`** — SVG rasterisation stays out of
scope, documented below — and the hero demo panels' *corner* shading is
`conic-gradient`, also deferred; the panel **fills** are linear/radial and now
paint.)

### Before → after vs headless Chromium (this machine, OS-dark)

| URL | SSIM before | SSIM after | pixdiff before | pixdiff after | Verdict |
|-----|-----------:|-----------:|---------------:|--------------:|:-------|
| example.com/ | 0.954 | 0.954 | 1.5% | 1.5% | held (no gradients/images) |
| en.wikipedia.org/wiki/Go | 0.423 | 0.413 | 22.9% | 23.0% | ~flat (−0.010; box-shadows now paint on its chrome; JS-`mw.loader`-confounded) |
| pkg.go.dev/net/http | 0.628 | 0.628 | 36.5% | 36.5% | held exactly |
| go.dev/blog/ | 0.666 | 0.666 | 33.2% | 33.2% | held exactly |
| react.dev/ | 0.710 | **0.719** | 33.4% | **30.2%** | **improved** (gradient panels paint) |

**react.dev is the target and moves the right way: +0.009 SSIM, pixdiff
33.4%→30.2% (−3.2pp).** The right-hand demo panels that rendered flat grey now
show the navy→teal→green linear gradient Chrome paints, and the teal pill buttons
gain their gradient/rounded fill (see the montage). The residual gap is the
inline-`<svg>` React atom (out of scope) and the larger-centred hero typography
(a font/layout gap, not a paint gap). example.com, pkg.go.dev and go.dev/blog hold
to three decimals; Wikipedia's −0.010 is a marginal, measured move — its Vector
chrome box-shadows now paint, nudging pixels on a page whose SSIM has always swung
on the un-run `mw.loader` sidebar. The authoritative regenerated numbers live in
`bench/REPORT.md`.

### What Phase 2.0 implements (each measured, tested, 6-arch clean)

- **CSS gradients** (`css/background.go`, `css/gradient_sample.go`): `linear-gradient`
  (explicit angles in deg/grad/rad/turn, `to <side>` and the box-dependent
  `to <corner>` "magic corners", multi-stop with px/%/unpositioned stops
  normalised per spec, hex/`rgb()`/`hsl()`/named/transparent colours,
  premultiplied-alpha interpolation so transparent stops don't bleed) and
  `radial-gradient` (circle/ellipse, `closest/farthest-side/corner` + explicit
  radii, `at <position>`). Painted into the element's background box and **clipped
  to the `border-radius` rounded rect**. Multiple layers are stacked
  first-on-top; vendor-prefixed and `repeating-` names parse.
- **`background-image: url(...)`** (`images.go`, `paint/paint.go`): resolved
  against the document base and fetched through `e.Client` (bounded, deduped,
  data-URI aware), decoded via **go-images**, painted honouring `background-size`
  (`auto`/`cover`/`contain`/px/%, one-axis `auto` keeps the aspect ratio),
  `background-position` (keywords + px/%) and `background-repeat`
  (`no-repeat`/`repeat`/`repeat-x`/`repeat-y`), clipped to the rounded box.
- **`box-shadow`** (`css/shadow.go`, `paint/paint.go`): comma-separated drop and
  `inset` layers with offset, blur, spread and colour. The blur is an **exact
  Gaussian-blurred box** via the error function (`erf`), so the soft extent is
  offset+spread+blur and is unit-test-checkable; a drop shadow paints behind the
  box, an inset shadow as a soft inner band clipped to the box.
- **`opacity`** (`paint/paint.go`): true **group opacity** — an element with
  `0 < opacity < 1` and its whole subtree render into an offscreen buffer that is
  then alpha-composited, so overlapping descendants fade as one group (not
  double-blended); `opacity: 0` skips the subtree.

### Deliberate simplifications (documented, not faked)

- **`conic-gradient` is not modelled** — the layer is dropped and the element
  falls back to its background colour (react's demo-panel corner shading uses it).
- **SVG `<img>`/inline `<svg>` are not rasterised** — go-images decodes PNG/JPEG;
  the go.dev logo and the react.dev hero atom are SVG and stay blank. PNG/JPEG
  bitmaps (react's `uwu.png`, the conf-2021 JPGs) do render.
- **Gradient border-radius clip is a hard rounded-rect test** (no corner AA);
  the painter's own `FillRoundRect` still AA-clips solid-colour rounded fills.
- **box-shadow blur ignores the corner radius** (a soft rectangle) and its extent
  is an `erf` box, not a per-corner rounded blur — visually indistinguishable at
  these blur radii.
- **`background-size`/`position`/`repeat` from the `background` shorthand** are
  not parsed (only the colour and the image layer are); the longhand properties
  are honoured. Gradient layers always fill the box (auto size).

### Reproduce (offline)

```
go run ./cmd/render -file testdata/gradients_demo.html   -out /tmp/gradients_demo.png   -w 740 -h 380
go run ./cmd/render -file testdata/gradients_golden.html -out /tmp/gradients_golden.png -w 200 -h 260
```

`testdata/gradients_golden.html` is the byte-golden fixture behind
`TestGradientsGolden` (exact sampled colours at each gradient's start/mid/end, a
box-shadow halo, a border-radius-clipped background and a `url()` data-URI
bitmap). `testdata/gradients_demo.html` is the labelled visual montage.

## Phase 1.9 — CSS breadth: modern colour syntax, Tailwind variants, border-radius

**Date: 2026-08-05.** The engine is strong on layout; the remaining gaps on
styled pages were **CSS breadth**. This phase was **measure-first**: for each
bench page I (a) fetched the page's real HTML + external CSS and tallied every
declaration, flagging the properties/constructs the cascade dropped; and (b)
**sampled the Chrome reference montage's own pixels** to see what actually
diverged, rather than trusting a description. That second step corrected a wrong
assumption and reshaped the ranking.

### The diagnosis (what actually drives the divergence — measured)

**Ground-truth correction found by pixel-sampling the montages:** this
machine runs macOS in **Dark** appearance and the reference headless Chromium
**follows it**, so Chrome renders pkg.go.dev `rgb(32,34,36)`, go.dev
`rgb(32,34,36)` and react.dev `rgb(35,39,47)` — all **dark**. example.com (no
dark theme) stays light. So the engine's job on these pages is to render **dark
too**, and the pre-existing optimistic `@media (prefers-color-scheme: dark)`
matching was already doing the right thing for pkg/go.dev (they matched Chrome).
The one page that did **not** match was react.dev — and that turned out to be the
single biggest, cleanest win, for a reason none of the initial "likely suspects"
named:

| Rank | Gap (measured) | Evidence | Fixed |
|-----:|----------------|----------|:-----:|
| **1** | **Modern space-separated `rgb()/hsl()` colour syntax** — `rgb(R G B / A)`. The cascade only parsed the legacy comma form, so **every** Tailwind colour dropped. | react.css: **192/192** `rgb()` use the space form, **0** comma. `background-color` was doubly broken (it split the value at the first space). Dark react sections rendered **white**. | ✅ |
| **2** | **Tailwind variant classes don't match**: `dark:bg-…`, `sm:…`, `lg:…` compile to escaped-colon class names inside **`:is(.dark …)`** wrappers. The selector parser broke at the first `:` and on the space inside `:is()`. | react.css: **110** `.dark ` rules, **87** `:is(` wrappers. The dark page backdrop is `body.dark\:bg-wash-dark` under `:is(.dark …)`. | ✅ |
| **3** | **`matchMedia()` always returned `false`**, so react.dev's theme script never added `<html class="dark">` → engine stayed light while Chrome (OS-dark) was dark. | react's inline theme script calls `matchMedia('(prefers-color-scheme: dark)')`. | ✅ |
| **4** | **`border-radius` not painted** — rounded cards/buttons/code panels render square. | 34–60 `border-radius` decls **per page** (all five). | ✅ |
| 5 | `box-shadow`, `transform`, `opacity`, `object-fit`, `text-transform`, `letter-spacing`, `::before/::after content`, gradient/`url()` backgrounds | present but lower measured pixel ROI on these five pages (gradients 1–5/page; `::before` mostly decorative/icon-mask on Wikipedia). | ⏳ next |

**Following the data, not the guesses.** The task's prior expected the top win to
be `::before/::after` pseudo-elements or gradients. Measurement said otherwise:
the dominant react.dev divergence was a **whole-page dark/light inversion** whose
root causes were (1) the modern `rgb()` syntax, (2) Tailwind `:is()`/escaped
variant selectors, and (3) `matchMedia` — a chain that, once closed, flips
react.dev to the dark theme Chrome renders. `border-radius` is the broad,
every-page polish item and was implemented too.

### What Phase 1.9 implements (each measured, tested, 6-arch clean)

- **Modern colour syntax** (`css/value.go`): `rgb()/rgba()/hsl()/hsla()` in both
  the legacy comma form **and** the modern space-separated `R G B / A` form
  (numbers or percentages; `/`-alpha as number or percent), plus `#rgba` and
  `#rrggbbaa` hex. `background-color` now parses the whole value; the `background`
  shorthand extracts a leading functional colour; the `border` shorthand and
  `border-color` keep functional tokens intact (a paren-aware tokenizer).
- **Tailwind variant selectors** (`css/selector.go`): backslash escapes in
  identifiers are honoured, so `.dark\:bg-wash-dark` parses as the class
  `dark:bg-wash-dark`; `:is()` / `:where()` / `:matches()` wrappers are expanded
  (paren-aware comma splitting, then splicing each alternative in place), so
  `:is(.dark .dark\:bg-wash-dark)` becomes the descendant selector it means.
- **`matchMedia()`** (`js/window.go`): evaluates the query instead of returning a
  constant — `prefers-color-scheme: dark` → `true` (consistent with the CSS
  cascade's optimistic dark and the dark Chrome reference), width features against
  the real viewport — which lets react.dev's theme script add `<html class="dark">`.
- **`border-radius`** (`css/…`, `paint/paint.go`): rounded-rect background fills
  and rounded **uniform** borders via `go-widgets/painter`'s anti-aliased
  `FillRoundRect`/`StrokeRoundRect`; `%` radii resolve against the box's smaller
  side (so a square + `50%` is a disc, a large px is a pill).

### Before → after vs headless Chromium (this machine, OS-dark)

| URL | SSIM before | SSIM after | pixdiff before | pixdiff after | Verdict |
|-----|-----------:|-----------:|---------------:|--------------:|:-------|
| example.com/ | 0.954 | 0.954 | 1.5% | 1.5% | held (no dark theme / Tailwind) |
| en.wikipedia.org/wiki/Go | 0.423 | 0.423 | 22.9% | 22.9% | held (JS `mw.loader` confound, unchanged) |
| pkg.go.dev/net/http | 0.628 | _see REPORT_ | 36.5% | _see REPORT_ | ~flat (hex-colour theme; residual is layout) |
| go.dev/blog/ | 0.666 | _see REPORT_ | 33.2% | _see REPORT_ | ~flat (already dark-matched) |
| react.dev/ | 0.263 | **0.710** | 90.5% | **33.4%** | **major win** (dark theme recovered) |

The numeric table in `bench/REPORT.md` is the authoritative, regenerated record.
react.dev's **+0.45 SSIM / pixdiff 90.5%→33.4%** is the headline: with the four
fixes, its dark backdrop, white text, teal rounded controls and dark code panels
now line up with Chrome (see the montage). No page regressed.

### Deliberate simplifications (documented, not faked)

- **`border-radius` is a single uniform radius.** Tailwind `rounded-*`, pills and
  circles set all four corners equal (the real common case); differing per-corner
  radii collapse to the last-applied value, and the elliptical `h / v` form keeps
  the horizontal radius. Rounded borders are stroked only when all four edges are
  identical; otherwise straight per-edge fills are used.
- **`:where()` specificity** is approximated as its argument's (should be 0); a
  minor over-count that does not affect these pages.
- **`matchMedia` dark** is a fixed signal consistent with the optimistic CSS
  cascade and the dark reference; it is not derived from a live OS-appearance
  probe.
- Gradients, `background-image:url()`, `box-shadow`, `transform`, `opacity` and
  `::before/::after content` remain **unimplemented** (rank 5) — the next phase.

## Phase 1.8 — CSS `position` + dynamic-pseudo suppression

The bench pinned the go.dev/blog regression to two missing features that let a
site's sticky/dropdown nav chrome render **in flow**, pushing real content far
down: no `position` support, and dynamic pseudo-classes (`:hover`/`:focus`)
effectively always-on. Phase 1.8 adds both, with **exact-geometry unit tests**
(`layout` and `paint` hold **100%** statement coverage; `css` at 99.7%).

### CSS `position`
- **`relative`** — the box stays in normal flow (reserves its space) and is
  painted shifted by `top`/`left`/`right`/`bottom` (`left` wins over `right`,
  `top` over `bottom`; px/em/% offsets, % resolved against the containing block).
- **`absolute`** — removed from normal flow (reserves **no** space), placed
  against the **padding box of the nearest positioned ancestor**, else the
  initial containing block. `top`/`left`/`right`/`bottom`, a `left`+`right` pair
  fixing the width, shrink-to-fit auto width, and `min/max-width` clamping.
- **`fixed`** — removed from flow and resolved against the initial containing
  block; for a full-page static shot it paints once at its place (document
  coordinates) and never reserves flow space.
- **`z-index`** — positioned boxes paint after in-flow content, ordered by
  z-index (stable within a tie). This is a correct-enough stacking order, not the
  full CSS stacking-context algorithm (see simplifications).

### Dynamic pseudo-classes are suppressed in the static render
`:hover`, `:active`, `:focus`, `:focus-within`, `:focus-visible` and `:target`
**never match** (a screenshot has nothing hovered/focused/targeted), so a
`display:none`-until-`:hover` submenu stays hidden exactly as in Chrome's default
shot. The non-dynamic parts are unaffected: `.btn:hover` does not match but
`.btn` base rules still apply, `.menu:hover .sub` simply does not apply, and in a
selector list (`a:hover, .base`) the other members still match.

Two offline fixtures back this: `testdata/position_demo.html` (relative /
absolute / fixed + z-index overlap, golden `position_demo.png`) and
`testdata/dropdown_hover_demo.html` (a `:hover`/`:focus-within` dropdown that must
render hidden, golden `dropdown_hover_demo.png`).

### Deliberate simplifications (documented, not faked)
- **`sticky`** is approximated as **`relative`** for a static full-page shot
  (there is no scroll position to stick to).
- **Stacking** paints all positioned boxes after in-flow content and orders them
  by z-index; it does not implement per-element stacking contexts, negative
  z-index painting behind the in-flow layer, or `opacity`/`transform`-induced
  contexts.
- **Initial containing block height** — this entry point has no separate viewport
  height, so `fixed`/`absolute` bottom/right offsets and viewport-anchored
  percentages resolve against the **in-flow document height** as the ICB height.
- **Static position** — an out-of-flow box with all offsets `auto` is placed at
  its containing block's origin (the true in-flow static position is not
  reconstructed). Auto margins on out-of-flow boxes are treated as zero.
- Out-of-flow boxes inside `<table>` internals are not collected (rare); the
  block/inline/flex/grid paths all handle them.

## Phase 1.7 — flexbox completeness + CSS grid

The bench harness identified CSS **flex/grid layout** (not JavaScript) as the
dominant remaining fidelity gap on modern Tailwind-driven pages. Phase 1.7
closes most of it. All of the following land with **exact-geometry unit tests**
(`layout` and `paint` stay at **100%** statement coverage; `css` at 99.7%).

### Flexbox — now complete enough for real Tailwind layouts
- **`flex-wrap`** (`wrap` / `wrap-reverse`) breaking items into multiple flex
  lines; **`flex-flow`** shorthand.
- **`gap` / `row-gap` / `column-gap`** between items (main axis) and lines
  (cross axis), including the two-value `gap` shorthand.
- **`align-content`** (start / end / center / space-between / around / evenly)
  distributing lines across a definite cross size.
- **`align-self`** overriding `align-items` per item; **`order`** reordering
  items within the container.
- **`min/max-width`** and **`min/max-height`** resolved within flex (clamping
  grow/shrink on the main axis and stretch on the cross axis).
- Nested flex containers, percentage bases against the container, `flex:1` /
  `flex:0 0 200px` shorthands.

### CSS grid (`display:grid`) — new
- **`grid-template-columns` / `grid-template-rows`** with `px`, `%`, **`fr`**,
  `auto`, **`repeat(n, …)`** and **`minmax(min, max)`** (incl. `minmax(_, 1fr)`).
- **`gap` / `row-gap` / `column-gap`** between tracks.
- **Explicit placement**: `grid-column` / `grid-row` with line numbers
  (positive and negative), `/`-ranges and **`span N`**; `grid-area` line form.
- **Auto-placement**: row-major flow for items without explicit placement, plus
  fixed-column-auto-row and fixed-row-auto-column items.
- **`grid-template-areas`** with `grid-area: <name>` placement.
- **`justify-items` / `align-items` / `place-items`** and per-item
  **`justify-self` / `align-self` / `place-self`** (stretch fills the cell,
  otherwise the item keeps its size and is positioned within the cell);
  **`justify-content`** distributes/centres the whole track band.
- `grid-auto-rows` / `grid-auto-columns` for implicit tracks; a lone `auto`
  column stretches to fill the container (matching browsers).

### Deliberately out of scope this phase (→ Phase 1.8, not faked)
- `repeat(auto-fill / auto-fit, …)`, `fit-content()`, `subgrid`, named grid
  **lines** (named **areas** *are* supported), and `masonry` — parsed values
  that reference these leave the property unset rather than guessing.
- `grid-auto-flow: dense` and `column` flow are parsed but auto-placement is
  always sparse **row-major** at this fidelity.
- Intrinsic (`auto`/`min/max-content`) track sizing only counts items that lie
  **within a single track**; a multi-track spanner does not grow the tracks it
  crosses (fixed/`fr`/`%` tracks and explicit sizes are exact).
- `fr` **rows** distribute space only when the container has a **definite
  height**; otherwise they behave as `auto` (content-sized).
- Cross-axis `min/max-height` and grid `min/max` bounds are treated as
  **border-box** limits (box-sizing is not distinguished on that axis).
- Baseline alignment, and `justify-content` spacing distribution on grid columns
  when `auto` tracks are present (auto tracks stretch instead).

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
- **No `overflow` clipping, transforms, `border-radius`, `box-shadow`,
  gradients, web fonts.** Percentage and viewport heights, and `vh`, are
  approximated. Table `colspan`/`rowspan` and `border-collapse` are not modelled.
  CSS grid is supported (Phase 1.7) and `position:relative/absolute/fixed` plus
  dynamic-pseudo suppression are supported (Phase 1.8); the deliberate
  simplifications of each are listed in their sections above (`sticky` ≈
  `relative`, approximate z-index stacking).
- **Fonts**: only bundled Inter/Lora/Go-Mono (regular); bold is faux-bold and
  italic is not rendered.

## Reproduce

```
go run ./cmd/render -url https://example.com/ -out testdata/renders/example.com.png -w 1024 -h 768
go run ./cmd/render -url "https://en.wikipedia.org/wiki/Go_(programming_language)" -out testdata/renders/wikipedia_go.png -w 1024 -h 768 -timeout 90s
go run ./cmd/render -url "https://www.rfc-editor.org/rfc/rfc1866.html" -out testdata/renders/rfc1866.png -w 1024 -h 768 -timeout 90s
go run ./cmd/render -file testdata/floats_demo.html -out testdata/renders/floats_demo.png -w 900 -h 700
go run ./cmd/render -file testdata/flex_table_demo.html -out testdata/renders/flex_table_demo.png -w 1000 -h 700
go run ./cmd/render -file testdata/flex_wrap_demo.html -out testdata/renders/flex_wrap_demo.png -w 900 -h 700
go run ./cmd/render -file testdata/grid_demo.html -out testdata/renders/grid_demo.png -w 1000 -h 950
go run ./cmd/render -file testdata/tailwind_hero_demo.html -out testdata/renders/tailwind_hero_demo.png -w 1024 -h 620
go run ./cmd/render -file testdata/position_demo.html -out testdata/renders/position_demo.png -w 400 -h 600
go run ./cmd/render -file testdata/dropdown_hover_demo.html -out testdata/renders/dropdown_hover_demo.png -w 400 -h 300
go run ./cmd/render -file testdata/modern_css_demo.html -out testdata/renders/modern_css_demo.png -w 400 -h 300
```

The Phase-1.9 golden test (`go test -run TestModernCSSDemoGolden`, regenerate
with `UPDATE_GOLDEN=1`) pins the modern-colour / border-radius / dark-variant
pixels of `testdata/modern_css_demo.html` against `testdata/golden/`.

The three live URLs were reachable from this environment; no offline substitution
was needed. The two demo fixtures render fully offline via `-file`.

## Visual-fidelity protocol vs Chromium (`scripts/compare/`)

Typography and layout are checked against a real browser with a reproducible
side-by-side, not by eye alone:

```
scripts/compare/chromium-compare.sh [-w WIDTH] [-o OUTDIR] [page.html ...]
```

It renders each page (those in `scripts/compare/pages/`, or any passed
explicitly) with **go-webengine** and with **headless Chromium** at the *same*
viewport width and device scale 1, then writes a labelled `NAME.sidebyside.png`
(OURS | CHROMIUM) and prints a coarse mean-absolute pixel difference per page.

This is a **local developer tool** — it shells out to a Chrome/Chromium binary
and is deliberately **not** a CI gate (CI has no browser). The MAD score is a
guide for tracking regressions/improvements run to run, never expected to reach
0 (font rasterisers differ between engines). Regenerate the committed
`testdata/golden/*.png` with `UPDATE_GOLDEN=1 go test ./...` when a change is a
verified improvement.
