# Fidelity Report

**Date: 2026-08-05, audited 2026-08-30 (see the first log entry below)**

Honest assessment of the renderer on five pages. Phase 0 shipped a static
HTML→CSS→block/inline→paint→PNG pipeline that **linearised every page to one
column**. It has since grown a full box model (floats, flexbox, CSS grid, tables,
`position`), broad CSS (`var()`, `@media`, dark-mode, gradients, box-shadow,
border-radius, opacity, `!important`), SVG rasterisation, and **JavaScript
execution** (goja + a real DOM + a settle-then-render loop). The renderer is
**no longer static**: script-driven DOM mutations are reflected in the output;
render with `DisableJS` to get the static, no-JavaScript document. Where a page
still diverges from a browser it is stated below, not hidden. The phase-by-phase
log runs newest-first; see "Known gaps" for the current, audited scope boundary
rather than trusting any single phase entry in isolation.

The committed PNGs under `testdata/renders/` back every claim here. Reproduce
them with the commands at the bottom. The measured-vs-Chrome numbers live in
[`bench/REPORT.md`](bench/REPORT.md).

## 2026-09-06 (round 48) — an inline-level element generated NO BOX AT ALL: its background, border and padding were never reserved and never painted, so a styled `<span>` label rendered as bare text and, with white-on-coloured text, vanished entirely (engine#128)

An inline-level element — `<span style="background:…;padding:…;border:…">`,
a bordered `<code>`, a highlighted `<a>`, the near-universal "pill"/"chip"/
"badge" pattern — had no representation in the layout tree of any kind.
`collectInline` flattened it into a flat list of `InlineItem` words, each
carrying only the innermost element's `Style` pointer and `Node`; nothing
recorded that a run of those words belonged to a box with edges.

Two distinct failures followed from the same root cause.

**Nothing reserved the horizontal space.** CSS reserves an inline box's
`border-left + padding-left` before its first content and its
`border-right + padding-right` after its last. `collectInline` reserved
neither, so the words after a padded `<span>` sat exactly where that span's
own padding should have been: any decoration painted for it would have
overlapped its neighbours, and a shrink-to-fit container sized to the text
alone clipped the padding off entirely.

**Nothing painted the decoration.** `paintBoxContent` paints backgrounds and
borders for a `*layout.Box`, and an inline element never gets one;
`paintItem` draws glyphs, image blits and form controls and nothing else. A
narrow stopgap existed for the background alone — `paintInlineBackground`
filled a rectangle behind each item whose `Style` pointer differed from the
block's, merging adjacent items of the same originating node — but it could
reach only the INNERMOST element (a `<b>` inside a styled `<span>` hid the
span's own background from it completely, since the words then carry the
`<b>`'s style, not the span's), it painted per item interleaved with the
glyphs (so a later item's background could paint over an earlier item's
letters), and it had no notion of a border, of padding, or of an element
being split across lines. Borders on an inline element simply never appeared.

Fixed by modelling what CSS actually specifies: an inline box FRAGMENTS, one
fragment per line box it spans, with `box-decoration-break: slice` (the CSS
default) putting its leading edge on the first fragment only and its trailing
edge on the last only.

**Layout.** Inline collection now carries a `decor` chain (`layouter.decor`):
the inline-level ancestors currently in scope that generate a box —
`newInlineDecor` says an element does when it has a background to paint or an
edge (border or padding) to reserve, and says NO for a `<b>`, `<em>`, `<a>` or
UA-default `<code>` carrying nothing but typographic style. Every `InlineItem`
keeps a reference to that chain, shared per element rather than copied per
word, so a page of plain text allocates nothing at all for this.
`resolveInlineEdges` then walks one formatting context in document order and,
from where each item's ancestor chain DIVERGES from its neighbour's, fills in
`decorFirst`/`decorLast` (the depths at which this item is the first/last item
of an ancestor) and `padLead`/`padTrail` (the horizontal edges it therefore
reserves). Those two lengths enter the line's advance in `wrapOneLine`,
`WrapItems`, `lineMetrics` and `preferredWidth`'s max-content estimate — the
same fields, and the same machinery, the collapsible-whitespace and
inline-margin work of round 44 already uses, not a parallel path. They are
deliberately NOT `SpaceBefore`: collapsible whitespace legitimately disappears
at the start of a line, an element's own padding does not.

A single new positioning loop, `placeLine`, replaces the two (wrapped and
`white-space:pre`) that layoutInline carried, opening a fragment when an
ancestor's first item arrives and closing it when the next item leaves it —
taking the trailing edge only when the element genuinely ENDS there rather
than merely continuing on the next line. Vertical padding/border deliberately
do NOT grow the line: they extend the fragment beyond it, which is what CSS
says an inline box's vertical padding does.

**Paint.** `paintInlineBackground` is gone, replaced by `paintInlineFragment`,
which paints a fragment's background (honouring `border-radius`, including the
rounded-fill path) and then its border. `paintBorders` was refactored into a
shared `paintEdges(…, left, right bool, …)` so a block box and an inline
fragment stroke their edges through the same code: a block passes true/true, a
fragment passes its own `First`/`Last`. All of a line's fragments now paint
BEFORE any of its items, outermost fragment first, so an enclosing background
lands under a nested one and no background ever paints over an already-drawn
glyph.

**Exported API** (`layout/box.go`), for a downstream painter — a PDF writer,
specifically — to consume without re-deriving any of it:

```go
type InlineFragment struct {
	Node        *dom.Node   // the inline element this is a piece of
	Style       *css.Style  // its computed style
	X, Y, W, H  float64     // absolute document px, BORDER box
	First, Last bool        // box-decoration-break: slice
}

type LineBox struct {
	// …
	Inlines []InlineFragment // outermost first
}
```

A consumer iterates `for _, line := range box.Lines { for _, fr := range
line.Inlines { … } }` and paints in that order. `X..X+W` covers the element's
content on this line plus its leading edge when `First` and its trailing edge
when `Last`; `Y..Y+H` covers the items' font box grown by the element's
vertical border+padding, and may exceed the line box's own height. Fragments
translate with their box (`translateBox`), so a flex/grid/float-repositioned
subtree's decoration follows its words.

**Proven deterministically.** In `layout` (fake measurer: 10px per rune,
ascent 8, line height 20, so every coordinate below is exact):
`TestInlineDecorationReservesEdges` (a span's 7px edges reserved: its text at
x=27 and the FOLLOWING text pushed to x=54, fragment X=20/W=34, and the line
height unchanged at 20 by the vertical border),
`TestInlineDecorationFragmentsPerLineBox` (a wrapped span: two fragments,
First/Last true/false then false/true, and no phantom re-indent on the
continuation line — its first word at x=0),
`TestNestedInlineDecorationOutermostFirst` (a decorated `<b>` inside a
decorated `<span>`: two fragments, span BEFORE b, both edges nested),
`TestAdjacentInlineDecorationsAreSeparateFragments`,
`TestInlineDecorationSpansAForcedBreak` (a `<br>` is not content: the edge is
reserved once, before the first word),
`TestInlineDecorationCountsTowardMaxContentWidth` (a float sizes to 34, not
20), `TestInlineDecorationInPreformattedText` (the `white-space:pre` path),
`TestBorderStyleNoneReservesNothing`,
`TestInlineDecorationTranslatesWithItsBox`, and
`TestUndecoratedInlineElementsMakeNoFragment` — which pins the
no-regression claim directly: `<a>`, `<code>`, `<b>` and `<em>` produce no
fragment, reserve nothing, and leave every item's X exactly where it was.

In `paint`: `TestPaintInlineFragmentBackground`,
`TestPaintInlineFragmentBorderSlice` (all three cases — a first fragment with
a left border and no right, a last with a right and no left, a middle with
neither and both horizontals), `TestPaintInlineFragmentRounded`,
`TestPaintInlineFragmentDegenerate`.

End to end: `TestInlineDecorationGolden` renders
`testdata/inline_decoration.html` and asserts the FINAL pixels — the top
border band, the left border on the first fragment, the background present on
BOTH lines, NO left border where the element continues onto line two, the
nested `<b>`'s green over its ancestor's blue, the right border on the last
fragment only, the bottom border, and not one decoration pixel anywhere on the
undecorated `<span>`'s rows — against a committed golden PNG.

**Every one of the other twelve committed goldens regenerates byte-identical**
(`UPDATE_GOLDEN=1 go test -short ./...` leaves `testdata/golden/` clean), which
is the direct evidence that no existing page changed. `layout` and `paint` both
hold 100% statement coverage.

**Honest residual.**

- **`box-decoration-break: clone` is not supported** — the property is not
  parsed at all; every fragment is sliced. `slice` is the CSS initial value,
  so this is only wrong for a page that explicitly asks for `clone`.
- **A fragment's content height is the item's LINE BOX height, not the font's
  own em box.** CSS paints an inline background over the content area, whose
  height is font-derived and independent of `line-height`; this engine uses
  `InlineItem.LineHeight`, so with a large `line-height` the band is taller
  than a browser's (the same convention the previous `paintInlineBackground`
  used — this change does not make it worse, and does not fix it). The font's
  natural height is not currently carried on the item, which is what fixing it
  would need.
- **`background-image`/gradient layers on an inline element still do not
  paint** — only the solid `background-color` does. An element whose ONLY
  background is a gradient generates no fragment at all.
- **`box-shadow`, `outline` and `filter` on an inline element** are likewise
  unpainted; those live on the `*layout.Box` path only.
- **No RTL.** Fragments are built left to right in document order;
  `direction: rtl` is unmodelled engine-wide, so a fragment's First edge is
  always its LEFT one.
- **An inline element split by a promoted block-level child** (the round-40
  `BlockBreak` path) yields fragments in each surrounding anonymous box with
  First on the earlier and Last on the later, rather than CSS's own
  "split the inline box around the block" model. No confirmed live page
  depends on the difference.
- **Vertical padding overflows the line box and is not clipped**, so a tall
  inline background can overlap the line above or below. That IS what a
  browser does; it is listed here because it looks like a defect and is not.

## 2026-09-05 (round 47) — a flex container mixing bare text with an element child silently dropped the text entirely, so a nav dropdown's own label vanished, leaving only its icon (engine#127)

go.dev/blog re-investigated fresh (by far the most stale page in the corpus:
last dedicated look was round 20, 27 rounds ago, on the already-declined
`@font-face` icon-ligature gap). A fresh full-resolution comparison found a
DIFFERENT, previously-undiscovered bug in the same nav bar: "Why Go" and
"Docs" — two of the nav's dropdown menu links — rendered as EMPTY, showing
only their `arrow_drop_down` icon (itself the already-known, correctly-
declined ligature-text gap) with no visible label text before it at all.
"Learn" and "Packages" (plain links, no dropdown icon) rendered correctly.

- Real markup: `<a class="js-desktop-menu-hover" aria-label=Docs
  aria-describedby="...">Docs <i class="material-icons"
  aria-hidden="true">arrow_drop_down</i></a>`, and go.dev's own stylesheet
  sets `.Header-menuItem > a:link, ...:visited { display: inline-flex; ... }`
  — the anchor itself is an `inline-flex` container holding BOTH bare text
  ("Docs ") and a real element child (the icon `<i>`).
- **Root cause, confirmed with a minimal offline reproduction before
  touching any source** (`<div style="display:flex">Why Go
  <i>ICON</i></div>` rendered as "ICON" alone): `layout/flex.go`'s
  `flexItems()` — and `layout/floats.go`'s `preferredWidth` flex-row branch —
  only ever collect `dom.Element`-type children, never bare text. Round 14
  already handled the case of a flex container whose content is bare text
  with ZERO element children (react.dev's `<h1 style="display:flex">React
  </h1>`) by falling back to plain inline layout — but that fallback only
  triggered when `len(items) == 0`. A MIXED container (bare text ALONGSIDE
  at least one real element, go.dev's actual case) has `len(items) > 0`, so
  it skipped straight to `flexRow`, silently discarding every bare-text
  sibling: the icon rendered, the label never did. The exact same gap
  existed independently in `preferredWidth`'s flex-row sum, which would have
  under-measured the container's width even if `flex()` alone were fixed.
- Fixed with a new `hasDirectText(node)` helper (any direct child that is a
  non-blank text node) used in BOTH places: `flex()` now takes its existing
  bare-text fallback whenever `len(items) == 0 OR hasDirectText(node)`, and
  `preferredWidth`'s flex-row branch is skipped (falling through to the
  general inline-measurement path already used for the zero-element case)
  under the same condition. This is the shared root cause for BOTH
  `display:flex` and `display:inline-flex` — `layoutNestedInlineFlex`
  (round 45) internally forces plain `DisplayFlex` and routes through this
  exact same `flex()` function, so go.dev's real `inline-flex` bug and a
  plain `display:flex` case are fixed by the identical code path.
- **A second, narrower bug surfaced while testing the fix against the
  pre-existing `TestFlexSkipsTextAndHidden` fixture** (text + a
  `display:none` span + a genuinely BLOCK-level `<div style="width:50px">`
  — heavier than go.dev's real case, where the second child is merely
  `display:inline`): the naive fallback unconditionally called
  `layoutInline` and set `box.Lines` directly, exactly like round-14's
  original fallback — but when the bare text sits alongside a true
  block-level element, `collectInline` promotes that block via the ordinary
  BlockBreak mechanism, which needs `box.Children` populated by
  `placeInlineSegments`, not a bare `box.Lines` assignment. This is the SAME
  "secondary box nothing walks" defect class round 45 fixed for
  `InlineItem.NestedBox` — an orphaned box, not a crash, so nothing but a
  targeted test would have caught it. Fixed by checking `hasBlockBreak(items)`
  and delegating to `placeInlineSegments` when true, mirroring `contents()`'s
  own identical check a few lines away.
- The pre-existing `TestFlexSkipsTextAndHidden` test asserted exactly the
  bug (`len(outer.Children) == 1`, treating the dropped text as CORRECTLY
  "not a flex item" when it should have become an anonymous one) —
  rewritten as `TestFlexRendersMixedTextHiddenAndElement` to assert the text
  and the block child both render while the `display:none` child still does
  not, matching this session's established precedent (rounds 42/45) for
  fixing a test that encoded a bug rather than working around it.
- Verified live: go.dev/blog's "Why Go" and "Docs" now render their labels
  correctly before the (separately, already-known) literal
  "arrow_drop_down" ligature text. **Bench flat on go.dev/blog itself
  (SSIM 0.684→0.684, pixdiff 19.0%→19.0%)** — two two-word nav labels are a
  tiny fraction of a page dominated by a long article list, the same "real
  fix, tiny aggregate movement" pattern this session has hit on nearly every
  nav-bar round. **developer.mozilla.org moved the OTHER way this run
  (pixdiff 13.3%→17.9%)** — confirmed NOT caused by this fix: Chrome's own
  capture now includes a live third-party ad banner ("Discover Otari") that
  was absent from round 46's capture (Chrome's own page height changed
  4188px→4278px between runs with no engine change at all, and re-running
  MDN alone reproduces the same new score deterministically against
  whatever ad is currently being served) — the same "ads/cookie banners are
  non-deterministic, not a rendering bug" category already documented for
  pkg.go.dev's cookie banner. news.ycombinator.com moved slightly better
  (16.2%→15.7%), plausibly the same general fix touching an unrelated flex
  usage there; not separately investigated.
- Coverage: css 99.5%, layout 100.0% (two new branches — the mixed-content
  fallback and the `hasBlockBreak` delegation — were 0%-covered immediately
  after the initial implementation, closed with
  `TestFlexContainerMixedTextAndElement` and the rewritten
  `TestFlexRendersMixedTextHiddenAndElement`), paint 100.0%, dom 98.1% (all
  floors held).

## 2026-09-05 (round 46) — an SVG `fill`/`stroke` set via an ordinary CSS class (Tailwind's `fill-*`/`stroke-*` utilities) was silently dropped, so the element rasterised with SVG's initial fill (black) instead of its intended colour (engine#126)

tailwindcss.com re-investigated fresh (last dedicated look pre-dated this
session's round numbering — by far the corpus's most stale page). Wasn't
picked for its bench score alone (13.3% pixdiff, mid-table) but for
staleness, per this round's own "pick the worst-scoring/longest-stale page"
discipline — and it paid off: the nav bar's Tailwind logo mark rendered as a
near-invisible dark blob instead of Chrome's blue swirl+wordmark, and the
search-box and version-badge icons were entirely missing.

- Real markup, fetched fresh: `<a aria-label="Home"><svg class="h-5
  text-black dark:text-white"><path class="fill-sky-400" d="..."/><path
  fill="currentColor" d="...(wordmark letterforms)..."/></svg></a>`. The
  wordmark path (`fill="currentColor"`, a literal XML attribute) is the
  session's ALREADY-documented oksvg multi-subpath mis-render gap (round 25,
  MDN's "mdn" logo) — ruled out as a re-discovery, not chased again. The
  ICON path is different: `class="fill-sky-400"`, no `fill=` attribute at
  all. `.fill-sky-400{fill:var(--color-sky-400)}` is a real, separate,
  previously-undocumented gap.
- **Root cause, confirmed with a minimal offline reproduction before
  touching any source** (`<style>.blue{fill:#1e90ff}</style><svg><rect
  class="blue".../></svg>` rendered black instead of blue): this engine's
  CSS cascade has NEVER modelled `fill`/`stroke` as properties at all — no
  field on `css.Style`, no case in `apply()`. Worse, even if it had,
  `svg.go`'s `serializeSVG` (which re-emits an inline `<svg>` DOM subtree as
  raw XML text for the third-party rasteriser, oksvg, to parse) only ever
  copies each element's literal `n.Attr` map — it has no notion of this
  engine's CSS cascade at all, so a class-driven paint colour had nowhere to
  go even in principle. oksvg then sees a `<path>` with no `fill` attribute
  and falls back to SVG's OWN initial value, black — invisible or
  near-invisible against a dark nav bar.
- Confirmed the SAME gap independently affects the nav's search-icon SVG
  (`class="fill-gray-600 dark:fill-gray-500"`, no attribute) and the
  version-badge's chevron SVG (`class="fill-gray-400"`, alongside an
  IGNORED-by-CSS-priority `fill="currentColor"` attribute) — both were
  invisible against the dark background for the identical reason, and
  `stroke-white` (a CTA underline) is a second, live, real usage of the
  parallel `stroke` property on the same page, so both were fixed together
  rather than fill alone — not speculative, both confirmed load-bearing on
  this one page.
- Fixed with three additive changes: (1) `css/value.go` — `Style` gains
  `Fill`/`FillSet`/`FillNone` and `Stroke`/`StrokeSet`/`StrokeNone` (the
  *Set/*None split matters because `Color{}` — transparent black — is also a
  legitimate real colour value, so "unset" needs its own bit rather than
  overloading the zero value), inherited like `color` in `inheritFrom`. (2)
  `css/parse.go` — `fill`/`stroke` cases parse a colour via the same
  `parseColor` every other colour property already uses, or set the *None
  flag for `none`; an unparseable value (an unresolved `var()`, matching
  every other colour property's existing behaviour) is silently a no-op,
  not a special case. (3) `svg.go`'s `serializeSVG`/`writeSVGNode` now take
  the document's already-computed `css.StyleMap` (already available at both
  call sites in `images.go` — no new plumbing needed) and, per element,
  override (or add) the regenerated XML's `fill`/`stroke` attribute when CSS
  resolved one, leaving every element CSS left untouched exactly as before.
  Deliberately NOT implemented: `fill:currentColor`/`stroke:currentColor`
  via a CSS class (no confirmed live usage — `currentColor` is fully
  supported already, but only via the pre-existing literal-XML-attribute
  path) and `fill:inherit`/`stroke:inherit` through the CSS-wide `inherit`
  keyword dispatcher (ditto) — both narrower gaps than base support, left
  undone rather than gold-plated in ahead of a confirmed need, matching this
  session's established scope discipline.
- **A second, separate real bug was found but NOT root-caused this round**:
  the "v4.3" version-badge button renders overlapping the START of the logo
  (a layout/positioning defect, unrelated to fill/stroke — it reproduced
  identically before and after this round's fix). Two reasonable minimal
  reproductions (a plain block-level flex sibling; an inline `<a>`
  containing an SVG as a flex item, matching the real markup's un-styled
  default `display:inline` `<a>`) both positioned correctly, failing to
  reproduce it — the real page's specific combination (a `position:fixed`
  ancestor, `justify-between` on the outer flex row, and a badge button
  whose own text content is split by an empty React-hydration `<!--
  -->` comment node between "v" and "4.3") wasn't narrowed down further
  before time was better spent shipping the confirmed, well-scoped fix
  above. Documented here rather than silently dropped, so a future round
  doesn't have to rediscover the symptom from scratch.
- Verified live: the nav's Tailwind logo now renders its correct blue swirl
  (confirmed via a full-resolution crop of a fresh render, matching Chrome).
  **Bench moved only slightly on tailwindcss.com itself (SSIM 0.706→0.709,
  pixdiff 13.3%→13.1%)** — the fixed icons are a handful of pixels on a
  13,585px page, the same "real fix, small aggregate movement" pattern this
  session has hit repeatedly. The fix's real value is general: it applies to
  any inline SVG on any page styled via CSS classes rather than XML
  attributes, a very common pattern (every Tailwind-based site's icon set).
- Coverage: css 99.5% (two new branches — `fill`/`stroke`'s color and
  `none` cases — were 0%-covered immediately after the initial
  implementation, closed with `TestApplyFillAndStroke`), layout 100.0%,
  paint 100.0%, dom 98.1% (all floors held). Also added
  `TestSerializeSVGCSSFillOverridesAttribute` (unit-level: override vs.
  untouched vs. `none`) and `TestInlineSVGCSSClassFill` (end-to-end,
  mirroring the real bug's shape) — the root `engine` package itself is not
  coverage-gated (it performs live network I/O), but both were verified to
  fail via a full revert-and-confirm-fail (`git stash`) before shipping.

## 2026-09-05 (round 45) — `display:inline-flex` parsed down to the SAME value as block-level `flex`, making every such element block-level; fixing it surfaced a SECOND, unrelated bug in `translateBox` that silently dropped a nested box tree's own coordinates during re-translation (engine#125)

pkg.go.dev re-investigated with a full top-to-bottom comparison (last
touched round 43, on a narrow icon-colour issue only) after noticing it
carries this session's corpus's highest pixdiff (49.6%) despite a middling
SSIM — a strong signal that SOMETHING beyond the small icon fix was
dominating the diff on this particular 78,426px page. The bench tool's own
diff-heatmap montage showed a ghosting pattern that visibly WORSENED going
down the page — the classic signature of a fixed vertical offset introduced
early that then propagates through everything below it, rather than
scattered noise.

Traced to the very top: pkg.go.dev's breadcrumb (`Discover Packages > Standard
library > net > http`) rendered as FOUR SEPARATE LINES, one per segment,
instead of Chrome's single row. `.go-Breadcrumb li{display:inline-flex}` —
this engine's CSS parser mapped BOTH `flex` and `inline-flex` to the exact
same `css.DisplayFlex` value, discarding the "inline" qualifier entirely and
making every `inline-flex` element block-level, stacking each breadcrumb
`<li>` onto its own line and inflating the header's height by roughly 3
extra lines — a fixed offset at the very top of the page that then shifted
everything below it, compounding into exactly the growing-misalignment
pattern the diff-heatmap showed.

This is the "opposite direction" half of the already-documented "inline-
flex/grid/table conflated with block" architectural gap (Known Gaps,
originally scoped as "comparable to Shadow DOM… not attempted", confirmed
on github.com round 24 and this same page's breadcrumb round 26) — but it
turned out substantially more tractable than that estimate, because this
engine already has the exact CONCEPT needed: `DisplayInlineBlock` already
exists as "an atomic inline-level box, laid out as a block", and
`layoutIsolated` (already used by flex/grid/table children) already knows
how to lay a node out at a fixed content width in its own isolated context.

Implemented for `inline-flex` specifically (the one confirmed live case;
`inline-grid`/`inline-table` are NOT attempted, unconfirmed as live bugs):
a new `css.DisplayInlineFlex` value, kept distinct from `DisplayFlex` so
`isBlockLevel` correctly excludes it. `appendElementInline` treats it as a
NEW kind of atomic inline item — `InlineItem.NestedBox`, a real laid-out
`*Box` (unlike Image/FormControl's opaque bitmap/drawn-control) — sized via
`layoutNestedInlineFlex`, which forces the isolated clone's `Display` back
to plain `DisplayFlex` (inline-vs-block only matters for how the PARENT
places the box, not how it lays out its own children) and shrink-to-fits it
at its own `preferredWidth` (no surrounding line width is known yet at
inline-collection time — the same reason Image/FormControl are sized up
front). `layoutInline`'s two positioning loops translate the NestedBox to
its final position once resolved, and `paintItem` recurses straight into
`paintBox` for it, so it gets the identical filter/opacity/background/
border/children handling any other box does for free.

**A second, unrelated, more surprising bug surfaced verifying this live,
found by the SAME "measure the real pixels" discipline this session
applies throughout**: after the base fix, the breadcrumb correctly flowed
onto one line — but rendered OVERLAPPING the page's own top search bar,
painted well above where its own correctly-computed layout box said it
should be. Direct instrumentation (comparing a from-scratch reproduction of
the engine's OWN pipeline against `engine.Render`'s real output) proved the
LAYOUT was already correct in both — the bug was in PAINT. Root cause:
`translateBox` — the general mechanism that repositions a subtree already
laid out at a local, pre-positioning origin (used for every flex/grid
item, and for floats) — walks `box.Children` and updates each
`InlineItem`'s scalar `X`/`Y`, but `NestedBox` is a SECOND, independent box
tree hanging off an `InlineItem` that neither of those paths reaches. This
is the EXACT SAME blind spot an earlier fix in this same function already
found and fixed for a DIFFERENT kind of attached data (a list-item
marker's absolute position, confirmed live on caniuse.com) — a flex/grid
ancestor positioned ABOVE the inline-flex element (pkg.go.dev's header
section, itself flex-laid-out) re-translated the breadcrumb's containing
box to its final position, correctly updating the InlineItem's own `X`/`Y`
scalar, but leaving `NestedBox`'s internally-stored coordinates — and by
extension, everything painted from them — stuck at their stale,
pre-translation position.

Fixed by making `translateBox` also recurse into `it.NestedBox` when
translating a line's items, keeping the two representations in sync.
**Caught a THIRD, smaller bug while writing the regression test for the
whitespace-before-an-inline-flex case**: `layoutNestedInlineFlex` lays out
the element's OWN content using the SAME layouter instance's
`wsPending`/`wsEmitted` whitespace-tracking fields, which its internal
layout call correctly mutates for ITS OWN text — but doing so silently
clobbers the OUTER context's pending-whitespace state before the calling
code can check it, dropping a real leading space. Fixed by capturing the
outer state before the nested layout call.

Verified live: the breadcrumb now renders as one line, positioned
correctly below the header. **Bench: SSIM 0.616→0.642, pixdiff
49.6%→33.4%** — a 16-point pixdiff drop, by far the largest single-round
bench movement this session, confirming the cumulative-offset theory. This
also moved developer.mozilla.org's and react.dev's page heights slightly
(both also use `inline-flex` somewhere), not separately investigated this
round.

Six new regression tests across `layout/inlineflex_test.go` and a new
`paint/inlineflex_test.go`, covering: inline flow (the base bug), the
translateBox propagation bug, whitespace-before handling (and the
wsPending-clobbering bug it caught), a `box-sizing:border-box` width-
narrower-than-padding edge case, the `white-space:pre` code path, and
paint's own recursion into a NestedBox — every one confirmed to fail via
genuine revert-and-rerun before its corresponding fix.

## 2026-09-05 (round 44) — a plain inline element's own `margin-left`/`margin-right` had NO EFFECT at all — `InlineItem` carries no margin field, so a browser-rendered gap between two adjacent inline elements with no source whitespace between them silently vanished (engine#124)

news.ycombinator.com re-investigated fresh with a full top-to-bottom
comparison (last touched round 33/38, via the general BlockBreak mechanism
rather than a dedicated look at this page's own remaining diffs). Live at
1024px, the header read "**Hacker News**new | past | comments | ..." — the
site title and the first nav link ran together with no gap, where Chrome
shows a clear space.

Root cause, confirmed against the real source: `<b class="hnname"
style="margin-right:5px">Hacker News</b><a href="newest">new</a>` — genuinely
no whitespace at all between the two elements in the HTML. The margin-right
IS the only source of the gap Chrome renders. `InlineItem` (the atom of
inline layout) has no margin field of any kind, and nothing in inline
collection ever reads `Style.Margin.Left`/`.Right` for a plain (non-replaced,
non-atomic) inline element — its margin was simply never consulted.

Fixed with a `pendingMargin` accumulator on the layouter: entering a plain
inline element adds its `margin-left`, leaving one adds its `margin-right`,
and whichever `InlineItem` is created next (in `appendWords`, or the
img/svg and form-control atomic-item branches) takes the accumulated
value into its `SpaceBefore` field via a new `takeMargin()` method — the
same field collapsible whitespace already uses to represent "space before
this item," reusing the existing line-layout machinery rather than adding a
parallel code path. Adjacent inline elements' margins correctly ADD (not
collapse, unlike block margins) since accumulation only resets at
`takeMargin()`. Reset alongside the whitespace-collapsing state at a
genuine break (a promoted block or forced `<br>`): a stale margin has
nothing left on the same line to apply to once one interrupts.

**A real, narrower limitation surfaced by the session's own OWN regression
test, not shipped over**: an inline margin lands correctly whenever the
margined element is not the very first item on its line — but
`layoutInline`/`wrapOneLine`/`WrapItems` deliberately ignore `SpaceBefore`
for a line's first item (so collapsible leading whitespace never creates a
phantom indent), and a margin riding in that SAME field is ignored for the
identical reason. A dedicated non-collapsible field would be needed to
survive that case too, not attempted here absent any CONFIRMED live page
needing it (this engine's real, confirmed use of inline margin-right is
always between two things already sharing a line, as on this page) — the
regression test suite covers the working case, documents the gap plainly in
`pendingMargin`'s own doc comment, and does not claim more than what is
actually fixed.

Verified live: "Hacker News new | past | ..." now renders with the correct
gap. **Bench is flat (SSIM 0.559→0.556, pixdiff ~16% either way)** — a
single-word header spacing fix on a page whose overall diff is dominated by
ordinary font-rasteriser variance across dozens of story-list rows, the
same already-documented pattern as every other small, correctly-scoped
fix this session that didn't move a shared-region score.

Four new regression tests in a new `layout/inlinemargin_test.go`
(margin-right, margin-left, adjacent-margins-add, and the block-break
drops-pending-margin boundary case), all confirmed to fail via genuine
revert-and-rerun before the fix.

## 2026-09-05 (round 43) — `filter` had NO EFFECT on any `<img>` element, block or inline, because an image is never represented as a layout.Box (the only thing the filter/opacity group-buffer pipeline knows how to wrap) (engine#123)

pkg.go.dev re-investigated fresh (last touched round 35, 8 rounds stale).
Live at 1024px, the "Details" panel's Valid-go.mod/Redistributable-license/
Tagged-version checkmark icons rendered as flat, uncoloured grey circles
instead of Chrome's teal accent colour, alongside a "Latest" version badge
missing its expected pill shape.

Traced the icon colour gap first (the more clearly load-bearing of the two).
pkg.go.dev ships each status icon as ONE shared, plain-grey SVG asset
(`check_circle_gm_grey_24dp.svg`) referenced via an ordinary `<img src>`,
then recolours it per context with the well-known "SVG-to-CSS-filter" idiom
— `filter: brightness(0) invert(45%) sepia(94%) saturate(6735%)
hue-rotate(176deg) brightness(94%) contrast(101%)` (the output of tools like
the codepen "SVG to CSS filter" converter) — since an `<img src="...svg">`,
unlike an inline `<svg>`, cannot be recoloured with `fill`/`currentColor`.

Root cause, confirmed with a minimal reproduction (a solid greyscale test
image via a `data:` URI, isolating the bug from the page's real assets)
before touching any page-specific code: `layout`'s `contents()` represents
EVERY `<img>` — whatever its `display` value — as an `InlineItem` with its
`Image` field set (see `isReplacedTag`), never as an ordinary `layout.Box`.
`paint`'s `filter`/opacity handling lives entirely in `paintBox`'s own
group-buffer wrapping (render offscreen, apply the filter chain, composite)
— which an `InlineItem` never passes through. Its own paint path,
`paintItem`, blitted the decoded image bytes directly with no reference to
`it.Style.Filters` (or opacity) at all. This is general, not specific to
this one icon or even to `<img>` under an inline ancestor: `filter` on ANY
image element had zero effect, block or inline.

Fixed by applying the SAME `applyFilters` function `paintBox` already uses,
directly in `paintItem`'s image branch, converting the decoded source to
`*image.RGBA` first (a decoded JPEG or other non-RGBA `image.Image`
implementation doesn't expose the raw pixel buffer `applyFilters`' colour-
matrix math needs — a new `toRGBA` helper, pass-through for the common
already-`*image.RGBA` case). Confirmed with the same minimal reproduction:
the filtered pixel now matches an equivalent plain `<div>` with the
identical `background-color`+`filter`, which was already correct — proving
the filter MATH was never the problem, only the missing application point
for images specifically.

Verified live: pkg.go.dev's three Details-panel checkmarks now render in
the correct teal accent colour. **Bench is flat (0.617→0.616 SSIM,
49.6%→49.6% pixdiff)** — the icons are a tiny fraction of ink on an
extremely tall (78,426px), text-and-code-block-dominated page whose
comparison region (capped to 2500px) is overwhelmingly governed by
font-rasteriser variance, the same already-documented pattern as Wikipedia
and github.com's README. developer.mozilla.org moved slightly the OTHER
way (13.5%→15.6% pixdiff) in this round's bench run — MDN also uses
filtered icons, so this fix legitimately changed pixels there too; a small
movement either direction from a real, independently-verified fix is
consistent with this session's own repeated finding that SSIM/pixdiff
comparison noise on real pages doesn't reliably track correctness at this
scale.

The "Latest" badge's missing pill shape is a SEPARATE, NOT-yet-investigated
issue (looks like a border-radius or padding gap, not a filter one) —
flagged for a future round rather than folded in speculatively.

Two new regression tests in `paint/filter_test.go`
(`TestInlineImageFilterApplied` and `TestInlineImageFilterConvertsNonRGBASource`,
the latter specifically exercising `toRGBA`'s conversion branch with a
decoded `*image.NRGBA` source), both confirmed to fail via genuine
revert-and-rerun before the fix.

## 2026-09-05 (round 42) — preferredWidth's max-content estimate summed a promoted block's contribution to zero instead of treating it as its own candidate line, collapsing a shadow-DOM header control down to nothing (engine#122)

developer.mozilla.org re-investigated fresh (last touched round 34, 8 rounds
stale). Live at 1024px, the header's "Theme" and "English (US)" controls
rendered as invisible, zero-width boxes — their text labels never appeared —
alongside a missing search-box outline, though the hamburger menu icon and
breadcrumb text (mangled separately by the already-documented oksvg defect,
round 25) were unaffected.

Traced with the same throwaway-instrumentation approach as rounds 39-41,
extended this time to dump declarative-shadow-DOM structure directly (an
element's `Shadow` field and its children), since MDN's header controls are
custom elements (`<mdn-color-theme>`, `<mdn-language-switcher>`) each hosting
their own shadow tree, in some cases nesting a SECOND custom element
(`<mdn-dropdown>`) with ITS OWN shadow tree and slots inside. Confirmed the
shadow-attachment and slot-projection machinery itself is sound (already
verified architecturally correct in earlier rounds, and re-confirmed here by
instrumenting the actual live structure rather than assuming) — the real
element ultimately at fault is an ordinary `<button>` with `display:flex`
(matching MDN's own icon-plus-label button styling), nested a few levels
down through the custom-element/shadow/slot chain, inside an otherwise-empty
`display:inline` wrapper.

Because the button computes to `display:flex` — a block-level value, per
this engine's Display enum — it gets promoted by round 38's BlockBreak
mechanism exactly like any other block-level element found under an inline
ancestor. `preferredWidth`'s inline max-content estimate (`layout/floats.go`)
summed ordinary inline items but skipped every non-floated BlockBreak
entirely, on the reasoning (correct on its own terms, and true for a run of
several sibling items) that a promoted block starts its own line and
contributes nothing to a DIFFERENT line's width. But CSS max-content is
properly defined as the WIDEST line, and a promoted block IS a line in its
own right — when it is the wrapper's ONLY content, as here, excluding it
left literally nothing to sum, reporting 0 instead of the button's real
width and collapsing the `display:flex` custom element (and its
`flex-shrink:0`-equivalent slot in the breadcrumbs-bar's flex row) down to
nothing.

Fixed by tracking a running MAX across candidate lines instead of a single
sum: an ordinary inline item still accumulates into the current line; a
floated BlockBreak still joins the current line (round 40's fix, unchanged);
a non-floated BlockBreak flushes the current line into the running max, then
independently recurses into its OWN preferred width as a second candidate —
matching how a genuine mix of text and promoted blocks actually behaves. A
`<br>` forced break is deliberately left as NOT a line boundary for this
estimate, preserving this function's pre-existing convention
(`TestPreferredWidthInlineWithBreak`, unaffected).

An earlier version of a round-40 test (`TestNonFloatedBlockBreakStillExcludedFromPreferredWidth`)
had asserted the OLD, now-understood-to-be-wrong behaviour (a promoted block
contributes nothing); corrected in place
(`TestNonFloatedBlockBreakIsItsOwnCandidateLine`) and joined by
`TestMultipleNonFloatedBlockBreaksTakeTheWidestLine`, which specifically
orders the wider block FIRST — a regression that tracked "the last block
seen" instead of a true running max would still pass the single-block case
but fail here.

Verified live: MDN's "Theme" and "English (US)" text labels, and a
search-box outline, now render. **Measured: SSIM 0.611→0.636, pixdiff
17.9%→13.5%** — the mangled "mdn" wordmark (oksvg, round 25) and the missing
`::before`/`::after` breadcrumb separators (documented gap, generated
content not synthesised at all) remain, unrelated to this fix and not
attempted here.

## 2026-09-05 (round 41) — preferredWidth never special-cased form controls, so an `<input>` reached through an element ancestor (rather than through ordinary inline-content collection) measured as 0 width instead of its UA-default size (engine#121)

Closes the last of the three github.com/golang/go header defects this session's rounds 39-40 flagged and left open: the "Go to file" search box and the "Code" button rendered overlapping instead of side by side.

Root cause, found via the same throwaway-instrumentation approach as rounds 39-40: the search box's real markup is `<input>` inside a `<span style="display:flex">` inside a plain `<div>`, sitting in a `flex-shrink:0` container beside a shrinkable title. `preferredWidth` (`layout/floats.go`) is the function responsible for estimating how wide that `flex-shrink:0` container needs to be — and it reaches the `<span>` (block-level, since `display:flex` is a block-level display) via its OWN `hasBlockLevelChild → max of children` branch, which recurses into the span with a plain `l.preferredWidth(span, ...)` call. That call, in turn, is itself a `display:flex` row, so it takes the "flex row with element children" branch and recurses again — straight into the `<input>` node itself, calling `l.preferredWidth(input, ...)` directly.

`preferredWidth` had no special case for a form control at all. Falling through to its generic "Inline: max-content" fallback, it calls `collectInline` on the `<input>` node's children — but an `<input>` is a void element with NO children to collect, so the estimate came out to 0. The control's real ~170px UA-default text-input width (already correctly modelled in `formControlSize`, and already correctly applied by `appendElementInline` and `contents()`'s own form-control branches) was never consulted, because THIS path into an `<input>`'s size — recursing through element ancestors via `preferredWidth`, rather than collecting it as one item of surrounding inline content — is a third entry point those two functions don't cover.

Fixed by adding the same check `preferredWidth` already has for `<img>` (an atomic, non-recursible element) for any `isFormControlTag` element: return `formControlSize`'s width plus the node's own edges, instead of falling through to the empty-children measurement.

Confirmed with a minimal, non-GitHub-specific reproduction (`<div style="flex-shrink:0"><span style="display:flex"><input></span></div>` inside a flex row) before touching the real page: the wrapping `flex-shrink:0` div reported 2px (its own edges only) instead of ~172px. Verified live: github.com's search box and "Code" button now render side by side, matching Chrome.

**Bench: SSIM 0.551→0.550, pixdiff 30.2%→30.2% — essentially flat**, despite the fix being real and visually confirmed (screenshots before/after show the overlap resolved). Same pattern as round 39 on this identical page: a small, correctly-scoped toolbar fix doesn't move an aggregate score dominated by the rest of the page's content. This closes out the github.com header investigation chain rounds 39-41 worked through — no further known defects flagged on this page's header at this time.

Regression test `TestFormControlSizeCountsTowardPreferredWidth` (`layout/formcontrol_test.go`, alongside the file's two existing form-control-entry-point tests), confirmed to fail via genuine revert-and-rerun before the fix (2px instead of 172px — exactly the input's real size going uncounted).

## 2026-09-05 (round 40) — a floated element promoted out of an inline ancestor (round 38's BlockBreak mechanism) was placed as a plain in-flow block and excluded from its ancestor's preferred-width estimate, collapsing a flex-shrink:0 container to zero width (engine#120)

Direct continuation of round 39's own honest disclosure: github.com/golang/go's repo-header action row (Notifications/Fork/Star) still overflowed the viewport's right edge after round 39's flex-shrink fix, and the header carried a large blank gap above the Code/Issues tab bar. Traced with the same instrumentation approach as round 39 (a throwaway program driving `css.CascadeVW`/`layout.LayoutDocument` directly against the real fetched page).

github.com's action row is classic, pre-flexbox markup GitHub still ships: `<ul class="pagehead-actions ... d-md-inline">` (computed `display:inline` at this width — confirmed genuinely correct: the `.pagehead ul.pagehead-actions{float:right}` rule in GitHub's own CSS requires a `.pagehead` ancestor class that is no longer present anywhere in the current DOM, so `float:none` on the `<ul>` itself is right, not a bug) containing three `<li style="float:left">` buttons, sitting inside a `flex-shrink:0` container beside a shrinkable repo-name title.

Each `<li>` is block-level (`display:list-item` computes block-outside here) nested under the inline `<ul>`, so round 38's BlockBreak mechanism correctly promotes it out of the inline flow — but BlockBreak promotion had only ever been exercised by genuine, non-floated blocks before, and was wrong in two ways for a floated one:

- **Placement** (`placeInlineSegments`, `layout/layout.go`): every promoted BlockBreak was routed through the plain block `l.place()`, giving it a full in-flow block box. `contents()`'s own ordinary per-child dispatch already checks `Float != FloatNone` BEFORE the block-level check for exactly this reason (float participates in a completely different placement algorithm); the promotion path never had the same check.
- **Sizing** (`preferredWidth`, `layout/floats.go`): the line-sum that estimates an inline container's max-content width skipped every BlockBreak item unconditionally, on the reasoning that a promoted block gets its own line and contributes nothing to the width of the surrounding text. True for a genuine block — wrong for a float, which does NOT start a new line and DOES consume horizontal space alongside its siblings. With the action row's entire content being three floated `<li>`s and nothing else, the estimate summed to zero, and a `flex-shrink:0` item that should have kept its full natural width collapsed to nothing instead.

Fixed both: a BlockBreak item now checks its own `Style.Float` (the promotion path already stores the RAW, authoritative style for exactly this kind of non-inherited-property check, per the established convention from round 38's own Display fix) — floated, it goes through `l.placeFloat` and its own `preferredWidth` is added to the running estimate; non-floated, behaviour is unchanged from round 38.

Verified live: the Notifications/Fork/Star row now renders fully on-screen, side by side, immediately below the repo title — and the blank gap above the tab bar is gone (both were the same root cause; page height 3173px→3051px). **Measured: SSIM 0.528→0.551, pixdiff 34.0%→30.2%** — a real, visible improvement this time, unlike round 39's flat aggregate score on the same page.

The remaining, separately-flagged "Go to file" search-box/"Code"-button overlap on this same page is still not fixed — confirmed structurally unrelated (a different subtree, the newer React `OverviewContent-module` toolbar, not this classic float-based action row) and left for a future round.

Two new regression tests in `layout/blockinline_test.go`
(`TestFloatedBlockBreakGetsFloatPlacementAndCountsTowardPreferredWidth` and
its non-floated counterpart `TestNonFloatedBlockBreakStillExcludedFromPreferredWidth`, added to keep the branch covered), the floated one confirmed to fail via a genuine revert-and-rerun before the fix (0 width and an off-container child position instead of the correct 100px/340px).

## 2026-09-05 (round 39) — flex-shrink resolved in a single, non-iterative pass, so a line where one item bottoms out at its floor left the container overflowing instead of passing the shortfall to its sibling (engine#119)

github.com/golang/go re-investigated fresh (last touched round 24, 15 rounds
stale, and round 24's own diagnosis was reached through a much simpler,
synthetic markup shape than the site's CURRENT real header — worth
re-verifying from scratch rather than assuming it still applies). Live at
1024px, "Sign in" and "Sign up" were entirely missing from the top nav, and
the nav items themselves were spread across huge, uneven gaps — despite the
CSS driving both being correctly parsed and cascaded (checked directly: the
mobile-only fallback controls compute `display:none` as they should, and the
container's `flex-direction` correctly switches to row at this width; this is
NOT a repeat of the CSS Media Queries Level 4 range-syntax gap engine#61
already closed for this exact site back on 2026-08-31).

Traced it to layout, not CSS: `github.com`'s marketing header lays out a
small logo item beside a nav+search+sign-in/up group in one flex row. Once
the mobile-only controls are correctly hidden, the visible content is a
narrow logo and a much wider nav group — together wider than the 1024px
viewport. `resolveMainRow` (`layout/flex.go`) distributed the resulting
negative free space across both items in a single pass, weighted by
`flex-shrink`, then clamped each one independently to its floor (0) or
min/max-width. The logo's proportional share pushed it below 0, so it
clamped to 0 — but the portion of its share that the floor refused to take
was simply dropped, never handed to the nav group. The nav group therefore
kept ~1421px of its ~1421px hypothetical (unshrunk) width in a 1024px
container, carrying "Sign in"/"Sign up" off the right edge of the viewport
entirely.

This is CSS Flexbox §9.7 "Resolving Flexible Lengths": clamping is not a
one-shot operation. An item that clamps at a bound must be frozen and
removed from the flexible set, and the space it couldn't absorb (or give up)
redistributed among the REMAINING flexible items — repeated until nothing
new freezes. `resolveMainRow` now runs that loop (bounded to at most one
iteration per item, since each iteration freezes at least one more): recompute
the still-flexible items' combined factor and the current shortfall, share it
out, freeze whichever item's clamp changed its value, repeat. The existing
symmetric case — two items that both hit the SAME bound in the same round,
so nothing is left over to redistribute (`TestFlexShrinkClampedByMinWidth`,
already in the suite) — still passes unchanged; only the asymmetric cascade
case was ever wrong.

Not GitHub-specific: this is the general flex-shrink algorithm, so it applies
to any flex row where a narrow item's shrink share would take it negative
while a wider sibling remains room to give. Confirmed live: "Sign in" and
"Sign up" now render on-screen at the top-right of the nav on
github.com/golang/go, and the nav item spacing is no longer stretched across
artificially inflated gaps.

**Two other real, separate defects were confirmed on this same page during
investigation but are NOT fixed here** — each looks structurally independent
of the flex-shrink bug (different DOM subtrees, different symptom shape) and
is flagged rather than folded in speculatively: (1) the repo header's
Notifications/Fork/Star button row still overflows past the right edge of the
1024px viewport instead of shrinking or wrapping; (2) the "Go to file" search
input and the "Code" split-button overlap instead of sitting side by side.
Both need their own root-causing against GitHub's Primer React markup before
a fix is attempted.

Regression test `TestFlexShrinkCascadesPastAFrozenItem`
(`layout/features_test.go`) reproduces the general shape (a 10px item beside
a 300px item in a 200px container) independent of GitHub's markup, confirmed
to fail (245px instead of 200px — exactly the un-redistributed single-pass
value) via a genuine revert-and-rerun before the fix was restored.

## 2026-09-05 (round 38) — a block-level child nested under an inline-context ancestor got no real box at all, instead of the spec's anonymous-block-wrapper-plus-promotion behaviour (engine#118)

This is the architectural gap round 36 (and round 33, and round 24
before it) each independently found and explicitly declined as
"comparable in scope to Shadow DOM slot projection" — three real pages
hit the same missing mechanism, so this round did the bibliography the
project's own standing rule requires (read the reference engines'
source before implementing) and fixed it.

**WebKit** used to solve this with "continuation": `RenderInline::splitInlines`
physically split an inline box into anonymous pre/post fragments linked
by a continuation pointer, so the "same" element kept answering
geometry/hit-test queries consistently across the split. That mechanism
is gone from WebKit's own main branch as of March 2026 (`8f520cb74d`,
"[blocks-in-inline] Remove continuation code"; `506bc07aa8`, "Don't wrap
sequences of blocks in anonymous block"). Current WebKit instead builds
a flat inline-item list (`InlineItemsBuilder.cpp`) that tags a
block-level descendant in place during a depth-first walk, and
`InlineLineBuilder.cpp`'s `handleBlockContent` lays it out via a nested
formatting context and ends the current line — no tree splitting, no
persistent continuation identity.

**Gecko** still runs its historical "ib-split" mechanism:
`nsCSSFrameConstructor::ConstructInline` scans for the first block-level
child and, if found, `CreateIBSiblings` physically materialises
alternating anonymous-block/inline frame chains
(`SetFrameIsIBSplit`/`IBSplitSibling`/`IBSplitPrevSibling`,
`PseudoStyleType::mozBlockInsideInlineWrapper`). Its source comments
cite CSS 2.1 §9.2.1.1 directly and work through a nested-`<span>`
example that produces exactly this multi-fragment tree.

Neither reference engine's persistent-identity machinery is needed
here: go-webengine has no incremental DOM mutation and no persistent
render tree — every render walks the DOM fresh in one recursive pass,
and every `InlineItem`/`Box` already carries its own resolved style and
node reference computed at collection time. That is architecturally
closer to *current* WebKit's flat-item approach than to Gecko's, and
explains why WebKit itself dropped continuation: a one-shot layout
model doesn't need to keep re-answering "which fragment is this
conceptually the same element as" once the box tree is built and
thrown away per render.

Implemented the same idea as a flat sentinel: a new
`InlineItem.BlockBreak` field marks a slot in an otherwise-ordinary
inline-item slice as "this is not inline content, it's a block-level
element found while collecting inline content under an inline-context
ancestor — promote it to a real sibling box" (`layout/box.go`). A new
`placeInlineSegments` (`layout/layout.go`) consumes that flat sequence,
splitting it into ordinary-item runs (each wrapped in its own anonymous
box, matching the pre-existing block/inline mixing path) interleaved
with real placed boxes for each sentinel — CSS 2.1 §9.2.1.1's algorithm,
now firing at ANY nesting depth under an inline ancestor rather than
only at a block container's direct children (which the pre-existing
`hasBlockLevelChild` dispatch already handled). `appendElementInline`
now checks the child's own resolved style for block-level `display`
before recursing into it as inline content, emitting a `BlockBreak`
sentinel instead when it is.

That "own resolved style" check has to read the RAW `l.sm[el]` map
entry, not the `cs` parameter `appendElementInline` was already passed —
`cs` is the caller's already-substituted fallback (parent style when the
child has no map entry of its own), correct for inherited properties but
wrong for `display` (non-inherited; the true initial value for an
unstyled element is always `inline`). Using `cs` directly broke the
existing empty-style-map shadow-DOM tests, which deliberately rely on
"no map entry = not block-level" throughout the codebase — the same
convention the pre-existing out-of-flow check in the same function
already follows a few lines above. Confirmed via a genuine revert (the
tests failed with the wrong text order once every unstyled element in
that tree got incorrectly promoted) before restoring the raw-map-entry
version.

The independent bibliography research (dispatched in parallel with the
implementation, not strictly before it — an intentional partial
divergence from "biblio d'abord," logged so it doesn't set a precedent)
both validated the design against the two reference engines above and
caught a second real bug I hadn't found yet: the new `BlockBreak`
branch didn't reset the pending-whitespace state, so a promoted block
followed by more text in the same wrapper gave the resumed text a
spurious leading space. Fixed with the same two-line reset the
pre-existing `<br>` case already applies, for the identical reason (a
forced break also starts a fresh line). Confirmed via revert-and-rerun
(`TestBlockBreakResetsPendingWhitespace` failed with `SpaceBefore=10,
want 0` without the reset).

Confirmed live for two of the three originally-documented cases:
**news.ycombinator.com**'s upvote-triangle icon (a `display:block`
background-image `<div>` inside an inline `<a>`) now gets a real box and
paints (SSIM 0.564→0.579, pixdiff 15.6%→15.3%). **tailwindcss.com**'s
syntax-highlighted code demo (each line a `display:block` `<span>`
inside inline `<code>`) now renders one line per row instead of
squashed onto one (page height 13273px→13585px; SSIM 0.666→0.704,
pixdiff 14.3%→13.6%). The third case, **github.com**'s flex-container-
under-an-unstyled-custom-element pattern from round 24, could not be
re-verified live: the actual github.com/golang/go header content has
changed since round 24 and no longer shows the originally-documented
"Sign in"/"Sign up" buttons. Covered instead by a synthetic regression
test built from the originally-documented markup shape
(`TestFlexContainerUnderUnstyledInlineElementGetsRealLayout`). The small
SSIM/pixdiff movement on github.com/golang/go in this round's bench run
(0.532→0.528, 32.7%→33.9%) is unrelated to this fix — the page's current
markup has no comparable block-in-inline structure — and is consistent
with this project's already-documented live-fetch noise (page content
and network timing vary between runs).

Four regression tests in the new `layout/blockinline_test.go`, all
confirmed to fail via genuine revert-and-rerun before the fix (and
individually for the whitespace-reset sub-fix): the votearrow div, the
flex container, the multi-line code spans, and the whitespace reset.

## 2026-09-04 (round 36) — a compound combining an explicit `*` with a class ("*.line") was parsed as a LITERAL tag name "*", so it never matched any real element (engine#117)

tailwindcss.com's own hero code demo — a syntax-highlighted, multi-line
`<pre><code>` sample — rendered every line of code squashed onto a single
row instead of one line per row.

Fetched the real compiled CSS rather than guessing: Tailwind v4's
`**:[.line]:isolate **:[.line]:block **:[.line]:not-last:min-h-lh`
arbitrary-variant utilities (used to give each syntax-highlighted
`<span class="line">` its own line) compile to
`:is(.\*\*\:\[\.line\]\:block *).line{display:block}` — a compound `:is()`
selector whose argument is itself a descendant chain. This engine's
`:is()`/`:where()` handling (a general text-splicing mechanism, not true
functional matching) correctly spliced this to
`.\*\*\:\[\.line\]\:block *.line`, and correctly un-escaped the
backslash-escaped class name — both already-solid, pre-existing
mechanisms, confirmed working in isolation. The actual bug: the
resulting `*.line` COMPOUND (a bare `*` immediately followed by `.line`,
no separating space) was parsed by `scanCompound` as tag name literal
`"*"` — `scanCompound` has no notion that `*` is special, only
`parseSimple`'s narrow `if s == "*"` fast-path (for a BARE, standalone
universal selector) ever recognised it. Since `compound.matches` compares
`c.Tag` byte-for-byte against the real element's tag name, `"*" !=
"span"` unconditionally, so a `*.foo`-shaped compound never matched ANY
element — not just this one selector, but the general shape, which
`:is(X *).foo` splicing (and any literal `*.foo` compound written
directly in a stylesheet) can both produce.

Fixed by treating a leading `"*"` tag from `scanCompound` as the
universal selector (dropped, imposing no tag constraint) rather than a
literal tag name, whether it appears bare (already handled) or fused
with further qualifiers.

**Root-caused the CSS side fully — confirmed via an isolated internal
test that the fix makes the cascade correctly compute `display:block`
for the real `.line` span — but the VISIBLE symptom persists for a
different, already-known reason**: `<code>` is inline by default, and
each `.line` span is one of its direct children. A live box-tree
instrumentation confirmed the `.line` node never gets its own `Box` at
all (not even as a flattened `InlineItem` — it vanishes entirely into
its parent's text run). This is a THIRD confirmed instance of the
"inline flattens block content" architectural gap already found and
correctly declined on github.com (round 24) and news.ycombinator.com
(round 33): a genuinely block-level child nested under an inline-context
ancestor gets no real box, rather than the spec's anonymous-block-wrapper-
plus-promotion behaviour. Not fixed here either, for the same reason as
before (comparable in scope to Shadow DOM slot projection).

The CSS selector fix itself is shipped regardless — it is real,
independently confirmed, and broadly applicable (any `*.foo`-shaped
compound, from `:is()` splicing or written directly), even though its
visible effect on THIS specific page is masked by the separate,
already-declined layout gap.

Regression test (`TestUniversalCompoundWithClass`) uses tailwindcss.com's
own real selector as one fixture and a bare `.parent *.foo` compound as a
second, confirmed to fail on both via a genuine revert-and-rerun check
before the fix was restored.

## 2026-09-04 (round 35) — `:empty` was an unmodelled pseudo-class, so `.Documentation-toc:empty{display:none}` degraded to hiding a genuinely non-empty table of contents on pkg.go.dev (engine#116)

pkg.go.dev/net/http's "Overview" section was missing its own table of
contents ("Clients and Transports", "Servers", "HTTP/2") entirely — Chrome
shows all three as a short link list directly under the "Overview" heading.

Root-caused a much bigger red herring first before finding the real cause
(documented in full for anyone reading this investigation later): the
initial hypothesis was that pkg.go.dev's outer page skeleton
(`.go-Main{display:grid;grid-template-areas:"banner" "header" "aside" "nav"
"article" "footer"}`) needed a client-side JavaScript flip of a
`data-layout` attribute to reach its wide, multi-column form. Traced this
all the way through: confirmed the site's dynamically-`document.
createElement`-injected `<script src>` chain (a ResourceLoader-style
pattern this engine's `runScripts`/`RunPending` architecture is explicitly
designed to support across settle passes) DOES land in the final DOM: 5
external scripts present after a full render. But then found the
`data-layout="responsive"` attribute is already present in the STATIC SSR
HTML on `<html>` itself — no JavaScript involved at all — and the relevant
grid override requires `width >= 80rem` (1280px), well above the bench's
1024px viewport. Re-checked Chrome's OWN reference render at the same
1024px and confirmed it ALSO shows the single-column stacked layout — there
never was a layout bug here; the whole `.go-Main`/`data-layout` line of
investigation was a wrong premise about which class governed the visible
symptom.

The REAL cause, found by then re-reading the actual missing content's
markup precisely: `.Documentation-toc:empty{display:none}` (main.min.css) —
meant to hide the table of contents ONLY on package pages that have no
headings to link to. `:empty` had never been modelled anywhere in
`css/selector.go` (confirmed via grep). Per this file's own established
"reduce, don't drop" convention for an unmodelled PLAIN (non-`:not()`)
pseudo-class, the compound degrades to matching its base alone —
`.Documentation-toc:empty` became unconditionally `.Documentation-toc`,
hiding the real, non-empty `<ul>` on every package page regardless of
whether it actually had any table-of-contents entries. This is the SAME
systemic bug class as round 31's `:first-child` fix (a real,
sometimes-true structural fact wrongly treated like an always-false
dynamic pseudo), just on the "plain compound" side of the pattern instead
of the "`:not()` argument" side.

Fixed by modelling `:empty` as a genuine structural pseudo (a new
`compound.Empty` field, checked as `len(n.Children) != 0` in `matches()`)
— this engine's DOM tree has no comment-node type, so "no children at all"
is the complete, spec-correct check with no comment/PI nuance to handle.

**Verified live: "Clients and Transports", "Servers", and "HTTP/2" now
render under "Overview"**, matching Chrome exactly.

Regression test (`TestEmptyPseudo`) uses pkg.go.dev's own real rule shape
against both a non-empty and a genuinely empty `<ul class="Documentation-
toc">`, confirmed to fail (matching the non-empty list) via a genuine
revert-and-rerun check before the fix was restored.

## 2026-09-04 (round 34) — `background-color: initial`/`unset` were unrecognised, so a real `<button>`'s UA-default gray chrome survived an author's own reset unchanged (engine#115)

developer.mozilla.org's header showed the "Theme" toggle and "English (US)"
language-switcher as visible gray PILL/BUTTON boxes — Chrome renders both as
plain text links with no visible box at all.

Fetched the real markup and CSS rather than guessing: both are backed by
declarative-Shadow-DOM web components (`<mdn-color-theme>`,
`<mdn-language-switcher>`) whose light-DOM slotted content is a real
`<button>` — `.color-theme__button{background-color:initial;border:none;
color:inherit;...}`. `border:none` already worked (confirmed via an
isolated cascade check: `Border.Top.Width` correctly zeroed), but
`background-color:initial` did not — `css/parse.go`'s `background-color`
case had no handling for the CSS-wide keywords `initial`/`unset` at all
(only `inherit` is handled generically, at the very top of `apply`), so the
declaration was silently ignored like any other unrecognised value, leaving
the UA-default `<button>` background (`#efefef`) untouched underneath.

Fixed narrowly: `background-color: initial` or `:unset` (a non-inherited
property, so `unset` behaves like `initial`) now resets to `Transparent` —
the property's actual CSS-spec initial value — mirroring the SAME reset
idiom the `background` shorthand already handles for a value with no colour
token (`background:0 0`/`background:transparent`, confirmed load-bearing on
github.com's own nav buttons in an earlier round). Scoped to
`background-color` only: the fetched CSS confirmed exactly two real uses of
`initial` on this page (`.color-theme__button`, `.color-theme__option`),
both on this one property; a general per-property "initial value" table for
every CSS property would be substantially bigger scope with no confirmed
need.

**Verified live: both the "Theme" and "English (US)" controls now render as
plain text**, matching Chrome (the still-missing breadcrumb `>` separator
between "Web" and "CSS" is a separate, already-documented gap — this
engine does not synthesise `::before`/`::after` generated content at all —
correctly left untouched by this fix).

Regression test (`TestCascadeBackgroundColorInitialResetsUAButton`) uses
MDN's own real rule shape as the fixture, confirmed to fail with the exact
predicted UA-default gray value (`{239,239,239,255}`) via a genuine
revert-and-rerun check before the fix was restored.

## 2026-09-04 (round 32) — table `colspan` was entirely unmodelled — a row mixing a spanning cell with plain ones split every story on news.ycombinator.com into two misaligned side-by-side blocks (engine#112)

news.ycombinator.com's front page rendered every story split into two
columns: a narrow left block stacking each story's rank number and its
"N points by user | hide | N comments" subtext, and a wide right block
holding just the title links — visually unrelated to their own row's
subtext, with heights drifting out of sync story by story.

Fetched the real markup rather than guessing: HN's own table structure
shapes each story as TWO `<tr>`s — `<tr class=athing><td>1.</td><td
class=votelinks>...</td><td class=title>Title</td></tr>` followed by
`<tr><td colspan="2"></td><td class=subtext>...</td></tr>`. `layout/table.go`
had never modelled `colspan` at all: a cell's column index was just its
position in the row's own cell list. In the title row (3 plain cells)
that happened to line up correctly, but in the subtext row the SECOND
cell (raw position 1) is really the THIRD column (after the colspan=2
empty cell) — the old code put it in position 1's column instead,
which is the narrow vote-arrow column shared by every title row. Every
subtext line thus rendered in the wrong, narrow column, and the column-
width algorithm (which takes each column's cells' MAX natural width
across all rows) inflated that shared column to the subtext's own wide
content width, splitting the whole table into two misaligned blocks.

Fixed by tracking each cell's real starting column index and span in
`tableRow` (a new `colStart`/`colSpan` pair, populated in `makeRow` by
advancing a running column cursor by each PRECEDING cell's own colspan,
not by 1 per cell), and using those — not raw cell position — for
natural-width attribution, column-width lookup, and final cell
placement. A `colspan` attribute of 0, negative, or non-numeric defaults
to 1, per the HTML spec's own "invalid value default" for this
attribute (`cellColSpan`). A colspan>1 cell is deliberately excluded
from natural-width attribution — distributing its content need across
the columns it spans is the full spec algorithm, well past this table
layout's own documented "basic auto layout" scope — but it still
occupies those columns so a later plain cell in the same row is not
shifted out of alignment with the other rows' columns.

**Verified live: the page now renders as one flowing title+subtext
column per story**, matching Chrome's own layout exactly.

This is a general table-layout bug, not an HN-specific quirk: any table
mixing a `colspan` cell with plain ones in different rows — an invoice
line-item table, a spec-comparison table, any "label row then a spanning
detail row" shape — would trigger the identical misalignment. Confirmed
no existing test exercised any table with a `colspan` attribute at all.

Two new regression tests: `TestTableColspanAlignsFollowingCellWithLaterRow`
(HN's own real row shape, confirmed to fail with the exact predicted wrong
column position via a genuine revert-and-rerun check) and
`TestCellColSpanInvalidValueDefault` (a bogus/zero colspan must not shift
a later cell, closing a coverage gap the first test's own guard clauses
left open).

## 2026-09-04 (round 31) — `:first-child` was an unmodelled pseudo-class, so `:not(:first-child)` — the standard "hide every item but the first" idiom — degraded to matching EVERY item, hiding content that should have stayed visible (engine#111)

caniuse.com's "Did you know?" section showed only its heading, with the tip
paragraph entirely missing — Chrome shows the first tip's real text ("If a
feature you're looking for is not available on the site, you can vote to
have it included...").

Fetched the real CSS rather than guessing: `.home__section--dyk.is-static
.home__list-item:not(:first-child), .home__section--dyk.is-static
.home__next-dyk-button { display: none; }` — the progressive-enhancement,
no-JS fallback for a "Did you know?" tip rotator, meant to show only the
first `<li>` of a list and hide the rest (plus the "Next" button, until JS
takes over). `:first-child` had never been modelled as a real structural
pseudo-class (only `:root`/`:checked`/dynamic-interaction pseudos/`:not()`/
`:host` are); the generic "unmodelled `:not()` argument imposes no
constraint" rule — correct for a genuinely always-false dynamic pseudo like
`:hover` (negating an always-false condition is always true) — was silently
also applied to `:first-child`, which is a real, SOMETIMES-true structural
fact, not an always-false one. `:not(:first-child)` therefore degraded to
`.home__list-item` with no negation at all, matching every list item
including the first, so the `display:none` rule hid the ENTIRE tip list
instead of all-but-the-first.

Fixed by modelling `:first-child` as a real structural pseudo-class: a new
`compound.FirstChild` field, set when the pseudo is `:first-child`, checked
in `matches()` against the element's previous element sibling (reusing the
existing `prevElementSibling` helper `+`/`~` combinator matching already
relies on) — `:first-child` matches iff there is none. `:last-child`/
`:nth-child(...)` remain unmodelled; only `:first-child` was confirmed
live-load-bearing, and it requires no new sibling-walking machinery beyond
what already exists (`:last-child` would need a new forward-walking helper
that doesn't exist yet, a bigger step not justified by any confirmed need).

**Verified live: the "Did you know?" tip text now renders**, matching
Chrome's first-tip text exactly.

Regression test (`TestFirstChildPseudo` in `css/selector_test.go`) uses
caniuse.com's own real rule shape (`.home__list-item:not(:first-child)`)
against a real two-`<li>` DOM built via `dom.Parse`, confirmed to fail (a
bare `:first-child` refusing to even parse) via a genuine
`git diff > patch; git checkout --; go test; git apply patch` cycle. `css`
held its 99.5% coverage floor exactly.

This fix lives entirely in `css/selector.go`'s own compound-matching types —
nothing here is a general-purpose utility usable outside HTML rendering, so
no extraction to a shared package applies.

## 2026-09-04 (round 30) — a content-less `<button>` (an icon-only submit button) rendered a fabricated literal "Submit" instead of no text at all — a fallback correct for `<input type=button/submit>` but wrong when copied onto the `<button>` tag (engine#109)

pkg.go.dev/net/http's header showed a stray "Submit"/"Sub" overlapping its
search box, and the breadcrumb read "httpSubmit" instead of just "http" —
both immediately visible at full resolution.

Fetched the real markup rather than guessing: the search-submit control is
`<button aria-label="Submit search"><img ... alt="" /></button>` — an
`<img>` icon, no text content at all. Chrome (fetched and compared directly)
renders no text for it whatsoever, just the icon. This engine's
`formControlDefaultSize`/`formControlDisplayText` (layout.go, paint.go)
special-cased an EMPTY `<button>` label to fall back to the literal string
"Submit" — a rule that IS correct for `<input type=button/submit>` with no
`value` attribute (a real, cross-browser UA default, handled separately by
`controlLabel`), but was mistakenly copied onto the `<button>` TAG case too.
No major browser fabricates default text for a content-less `<button>`
element; it simply renders empty, sized to its own padding.

The existing test suite had encoded the wrong behavior as intentional
(`TestFormControlButtonTagUsesTextContent`'s "empty <button> falls back to
Submit" case, justified by a comment citing "the real-world unstyled-button
case go-aiquota's own login flow uses") — checked this claim against
go-aiquota's actual capstone test (`e2e_login_test.go`) rather than trusting
the comment, per bibliographie-avant, and found its real login button has
visible text ("Log in"), never empty; the "Submit" fallback was never
actually validated against a real downstream need.

Fixed by removing the button-tag-specific fallback in both `layout.go`
(sizing) and `paint.go` (drawing) — `<input type=button/submit>`'s own,
separate, still-correct "Submit"/"Reset" default via `controlLabel` is
untouched.

**Verified live: the "Submit" text is gone from the search button, and the
breadcrumb correctly reads just "http".**

Removing the button-tag fallback also removed the ONLY test case (an
implicitly-labeled `<button>`) exercising `paintFormControl`'s button-label
horizontal-centering draw path, dropping `paint`'s coverage below its 100%
floor; fixed by giving `TestPaintFormControlButtonBackground` an explicit
non-empty label so that path stays covered on its own terms rather than as
a side effect of the bug being fixed.

Three existing tests updated to the corrected expectation
(`TestFormControlButtonTagUsesTextContent`, `TestFormControlDisplayText`'s
button case, `TestPaintFormControlButtonBackground`), each confirmed to
fail with the exact predicted wrong value (84px / "Submit") via a genuine
revert-and-rerun check before the fix was restored.

This fallback is engine-specific glue between this codebase's own DOM/CSS
node types and its layout/paint pipeline — nothing about it is a
general-purpose utility, so no extraction to a shared package applies here.

## 2026-09-04 (round 29) — CSS logical margin/padding properties (`margin-block-start`, `margin-block-end`, `margin-inline-*`, and the `-block`/`-inline` axis shorthands) had zero support — every declaration using them was silently dropped (engine#108)

go.dev/blog's blog-listing page rendered each entry (title/date/author, then
a separate summary paragraph) with extra, unwanted vertical whitespace
between the author line and the summary — Chrome fits 6 full entries plus an
interstitial cookie-consent banner in the same ~900px of vertical space where
this engine's render fit only 5.

Fetched the real page HTML and CSS rather than guessing from the screenshot
alone: go.dev's stylesheet uses
```css
#blogindex p.blogsummary { margin-block-start: 0px; }
#blogindex p.blogtitle   { margin-block-end: 0px; }
```
to zero out the gap *between* a listing's title-block and its own summary,
while leaving the UA-default `<p>` margin on the *outer* edges (the gap
before the next entry's title). This engine's declaration-apply switch had
no case for any CSS Logical Property on margin or padding at all — direct
grep confirmed zero matches for `margin-block`, `margin-inline`,
`padding-block`, or `padding-inline` anywhere in `css/parse.go` — so both
declarations were silently dropped as unrecognised, leaving the default
`<p>` UA margin (16px, both sides) on both edges of the gap that should have
collapsed to 0.

The analogous logical *inset* properties (`inset-inline`/`inset-block`) were
already supported, via an established precedent: this engine has no
bidi/vertical writing-mode support anywhere, so a logical axis always maps
directly to the same physical edges regardless of any writing-mode or
direction context (block → top/bottom, inline → left/right). Extended that
exact precedent to margin and padding: `margin-block-start`/`-end` →
`Margin.Top`/`Margin.Bottom`, `margin-inline-start`/`-end` →
`Margin.Left`/`Margin.Right` (honouring `auto`, exactly like `margin-left`/
`margin-right` already do), plus the two-value `margin-block`/`margin-inline`
axis shorthands, and the six padding equivalents (`padding-block-start/end`,
`padding-inline-start/end`, `padding-block`, `padding-inline`) — the padding
side wasn't confirmed load-bearing on this specific page, but the
implementation cost was near-zero given how directly it mirrors the margin
case, and leaving it out would have reintroduced the exact same silent-drop
gap the moment a real page used a logical padding property instead.

Verified live: re-rendering go.dev/blog after the fix shows 7 full entries
fitting in the same 900px region the pre-fix render fit 5 into, matching
Chrome's tight per-entry spacing.

Regression tests use go.dev's own real rule shape as the fixture
(`TestCascadeMarginLogicalProperties`, `TestCascadeMarginBlockInlineShorthand`,
`TestCascadePaddingLogicalProperties` in `css/cascade_test.go`), confirmed to
fail with the exact predicted wrong values (the UA-default 16px leaking
through) via a genuine revert-and-rerun check before the fix was restored.

## 2026-09-04 (round 28) — `@media not all and (...)`, Tailwind v4's compiled form of every `max-*:` breakpoint variant, was silently un-negated — every narrow-viewport-only rule matched at every wider viewport instead (engine#107)

tailwindcss.com's own homepage rendered a stray line of grey monospace text
("text-4xl text-5xl text-white") above its hero headline, and its header's
"Docs / Blog / Showcase / Partners / Plus" navigation links were entirely
missing — both at full resolution, immediately visible.

Fetched the real compiled CSS rather than guessing: the stray text is an
`aria-hidden="true"` decorative annotation Tailwind's marketing site uses
throughout to show which literal utility class produces a given effect —
several `<span>` elements, each gated on a DIFFERENT responsive variant
(`max-sm:inline`, `sm:max-md:inline`, `lg:max-xl:inline`, `xl:inline`),
designed so exactly ONE is visible at any given viewport width. Root cause,
found by tracing the ACTUAL compiled selector rather than assuming Tailwind
still emits a plain `@media (max-width:...)`: Tailwind v4 compiles every
`max-*:` variant (and the upper bound of a compound range like
`sm:max-md:`) to **`@media not all and (min-width:...)`** — CSS's own idiom
for negating a single feature test, since `all` (a media type that always
matches) reduces `not all and (X)` to `not (X)`. `mediaMatches` had no
concept of `not` at all: it scanned the condition text for a `min-width`
feature, found one, and evaluated it completely normally — silently
ignoring the negation, so a rule meant for viewports BELOW a breakpoint
matched at every viewport AT OR ABOVE it instead, the exact opposite of
its meaning. At the bench's 1024px viewport this made BOTH `max-sm:` and
`sm:max-md:`'s narrower-than-1024px variants match simultaneously (hence
two stray class-name labels appearing together, "text-4xl" AND
"text-5xl"), and — far more consequentially — hid the header's own
responsive nav links, which use the identical `max-*:`/`sm:max-*:` idiom
to swap between a mobile menu and the desktop link row.

Fixed with a single new regex (`notAllAndPrefix`, matching a leading
`not all and` case-insensitively) checked at the very top of `mediaMatches`:
when it matches, the REST of the condition is evaluated recursively through
the same function and the result negated — no new evaluation logic needed,
since everything after "not all and" is exactly the ordinary feature-test
text `mediaMatches` already understands. A first attempt anchored the regex
to the very start of the string with no allowance for leading whitespace
(`^not\s+all\s+and\s+`) and consequently DID NOT FIRE at all — verified
live before assuming success (per this session's "verify live even after a
green test suite" discipline): `mediaMatches` receives its condition as
`lower[len("@media"):]`, which always carries the single space that
followed "@media" in the source text, so the anchored `^not` never matched
a real caller's string even though a hand-typed test omitted that leading
space and passed. Fixed by allowing (and requiring nothing of) leading
whitespace in the regex itself, confirmed against the exact real
leading-space-carrying string this time.

**Verified live: the stray class-name line now shows the single correct
label for a 1024px viewport ("text-6xl text-white text-balance"), and the
full "Docs / Blog / Showcase / Partners / Plus" navigation — plus a
previously-invisible "Get started" button — now render.** This is a
broadly-applicable fix, not tailwindcss.com-specific: Tailwind v4's
`max-*:` variants are used constantly across any site built with it (this
session's OWN earlier `min-width`/rem/range-comparison/multi-term-`calc()`
media-query fixes were ALL, similarly, first found on Tailwind-built sites
and later confirmed to matter broadly). Three regression tests
(`TestMediaMatchesNotAllAnd` — the real leading-space-carrying string, the
case-insensitive/no-space `<link media>` form, and a confirmed-unrelated
`not screen` left alone; `TestParseStylesheetNestedNotAllAnd` — the real
NESTED shape `sm:max-md:` compiles to, an outer plain `min-width` wrapping
an inner `not all and`), confirmed to fail with the exact wrong values
predicted via a real `git diff > patch; git checkout --; go test; git apply
patch` cycle. `css` held its 99.5% coverage floor.

## 2026-09-03 (cont., round 26) — a missing vendor-prefixed pseudo-element wrongly hid a `<details>`'s whole summary; the legacy `clip: rect(...)` property implemented (engine#106)

pkg.go.dev/net/http's "Details" panel (four checkmark rows: "Valid go.mod
file", "Redistributable license", "Tagged version", "Stable version") was
COMPLETELY MISSING — not misaligned, just absent — and separately, its
header's "Skip to Main Content" accessibility link rendered as literal
visible text overlapping the real search box. Two independent, unrelated
bugs, both root-caused live rather than guessed at.

**Bug 1 — `isPseudoElement` did not recognise `-webkit-details-marker`.**
Each checkmark row is `<li><details class="go-Tooltip"><summary>…icon +
"Valid go.mod file" + help icon…</summary><p role="tooltip">…</p></details>
</li>` — a real, load-bearing `<details>`/`<summary>` pair this engine
already has UA-stylesheet support for (`details:not([open]) > :not(summary)
{display:none}`, added in an earlier round specifically for this exact
tooltip pattern). Confirmed via TWO successive offline `css.CascadeVW` dumps
(the real fetched markup + the real fetched stylesheets, no live pipeline)
that this existing rule correctly does NOT match `<summary>` in isolation —
so the regression had to be elsewhere. Found it by walking every parsed
rule that matches `<summary>` and printing its declarations: the site's own
CSS carries `.go-Tooltip>summary::-webkit-details-marker,.go-Tooltip>
summary::marker{display:none}` — hiding WebKit's native disclosure-triangle
marker, paired with the standard `::marker` for cross-browser coverage, a
completely ordinary and harmless author rule in any real browser.
`isPseudoElement` recognises `"marker"` but not `"-webkit-details-marker"` —
an oversight, not a declined feature (the function already carries several
OTHER vendor-prefixed pseudo-elements: `-moz-selection`, `-webkit-input-
placeholder`, `-webkit-scrollbar` and its sub-parts). Unrecognised, it fell
through `parseSimple`'s "reduce, don't drop" default: the compound reduced
to plain `summary`, matching the REAL element and hiding its entire visible
content, not just a marker glyph this engine never draws anyway. Fixed with
a one-line addition to `isPseudoElement`'s recognised list.

**Bug 2 — the legacy `clip: rect(...)` property was not implemented at
all.** pkg.go.dev's own "skip to main content" link uses `clip:rect(0 0 0
0)` (position:absolute, no explicit small width/height) — the CSS2-era
screen-reader-only idiom that PREDATES the now-more-common `width:1px;
height:1px;overflow:hidden` version this engine already handles (see
`Overflow.Clips`'s own doc comment, which only ever mentions that variant).
Unrecognised, the link kept its natural (large, readable) auto size and
painted its real text in place, overlapping the real search box. Added
`css.Style.HasClip`/`ClipRect` (parsed by a new `parseClipRect`, accepting
both the CSS2-required comma-separated `rect(0, 0, 0, 0)` form and the
later space-separated relaxation — a bare `auto` edge or anything else
`parseLength` cannot resolve leaves the whole declaration unrecognised
rather than guessed at, since real-world usage of this property
overwhelmingly hard-codes all four edges to 0) and a new
`paint.intersectClipRect`, applied at the very top of `paintBoxContent` —
before background/border/text/children — scoped to `position:absolute`/
`fixed` per spec, matching the property's own real-world semantics exactly:
the element keeps its normal layout box and position, only its PAINTED
region shrinks to nothing.

**Verified live: pkg.go.dev's four checkmark rows now render with their
icons, and "Skip to Main Content" no longer overlaps the search box** —
both confirmed independently on the real page (each fix's effect is
localised to its own element; neither masks the other). Four regression
tests: `TestPseudoElementMatchesNothing` extended with the exact
`summary::-webkit-details-marker` shape; `TestParseClipRect` (both
separator forms, an unresolvable `auto` edge, wrong argument count);
`TestCascadeClipDeclaration` (the full cascade-apply path, not just the
parser in isolation); `TestClipRectHidesContent`/
`TestClipRectIgnoredWithoutPositioning` (paint-level, confirming the
position:absolute/fixed spec-scoping in both directions). All confirmed to
fail without their fix via a real `git diff > patch; git checkout --; go
test/vet; git apply patch` cycle — the css package failed to even BUILD
without Bug 2's fix (`cascade_test.go` references the new `HasClip` field),
the same "compile-time signal" class of confirmation as round 24's
`InlineItem.Label`. `css`/`layout`/`paint`/`dom` all held their coverage
floors (99.5%/100%/100%/98.1%); `paint.intersectClipRect`'s first draft
carried two defensive clamp branches copied from `descendantClip`'s
pattern that turned out to be dead code here (`image.Rectangle.Intersect`
already returns the zero rectangle for a non-overlapping pair, unlike
`descendantClip`'s own per-axis intersection, which really can end up
inverted on just one axis) — removed rather than covered with a forced test,
per this session's "don't add handling for scenarios that can't happen"
discipline.

Remaining visible differences on this page (the breadcrumb — "Discover
Packages / Standard library / net / http" — stacking vertically with no
">" separators between items, and the header's "Submit" text appearing
where Chrome shows only icon buttons) were investigated and confirmed to be
the SAME already-documented, deliberately-deferred gaps from round 24
(`display:inline-flex`/`inline-grid`/`inline-table` losing their "atomic
inline box, real layout inside" outer behaviour, conflated with the block-
level `flex`/`grid`/`table` values; `::before`/`::after` generated content
not synthesised at all) rather than new defects — not re-investigated from
scratch, per the existing Known Gaps entries.

## 2026-09-03 (cont., round 25) — `display:contents` implemented at layout's one shared child-iteration chokepoint; a media-query `calc()` evaluator upgraded from two terms to general arithmetic (engine#105); a genuine third-party SVG-rasteriser defect found, isolated, and documented, not fixed

developer.mozilla.org's reference-article table-of-contents ("In this
article: Beginner's tutorials, Guides, How to, ...") rendered stacked BELOW
the article body instead of beside it in its own sidebar column, as Chrome
shows. Live-fetched the real markup/CSS and found TWO independent, unrelated
bugs sharing this one page.

**Bug 1 — `display: contents` was not implemented at all.** MDN's article
layout is `<div class="layout__2-sidebars-inline"><main class="layout__content"
style="display:contents"><div class="layout__header" style="grid-area:header">
...</div></main><aside class="layout__right-sidebar" style="grid-area:sidebar">
...</aside></div>` — the grid container's actual children ARE `<main>` and
`<aside>`, but `<main>` sets `display:contents` specifically so it drops out
of the grid's item list, letting its OWN child (`.layout__header`, which
carries the real `grid-area`) take its place instead. Confirmed via an
offline `css.CascadeVW` of the exact real markup + real fetched stylesheets
(no live pipeline involved) that this engine's cascade computed the grid
container's `grid-template-columns`/`grid-template-areas` correctly, and
`<aside>`'s own `grid-area:sidebar` correctly — the bug was purely that
`<main>` (unrecognised `display:contents`, defaulting to `DisplayBlock`)
became an unnamed grid item, auto-placed into a cell of its own, while its
child's `grid-area:header` was silently ignored (a `grid-area` only matters
on a DIRECT grid item) — corrupting the whole layout's column placement, not
just hiding one element.

Fixed at the ONE existing chokepoint every layout algorithm already shares:
`renderedChildren` (`layout/shadow.go`, the same function Shadow DOM slot
projection already substitutes through) is now a `*layouter` method (needed
the style map to check each child's computed `Display`) and calls a new
`flattenContents`, which replaces any `display:contents` child with ITS OWN
rendered children, recursively — so it never itself reaches box placement,
matching CSS's real behaviour (the element generates no box at all). Adding
`css.DisplayContents` to the `Display` enum and `"contents"` to
`css/parse.go`'s display-keyword switch was the only other change needed;
every one of the 10 production call sites (block/inline layout, flex, grid,
table row/cell collection) picked up the fix automatically by construction,
without being touched individually beyond the mechanical `renderedChildren(x)`
→ `l.renderedChildren(x)` rename. A page with no `display:contents` anywhere
takes the exact same `return children` path as before, unchanged.

**Bug 2 — the media-query `calc()` evaluator only ever handled a bare
two-term expression.** Even with display:contents fixed, the sidebar STILL
didn't move — a second, unrelated live-cascade dump showed the grid
container's OWN `display` resolving to `DisplayBlock`, not `DisplayGrid`,
at the bench viewport (1024px). Root cause: MDN's own breakpoint for this
is `@media (width < calc(1rem * 2 + 15rem + 2rem + 31rem))` (mobile-only,
real width 800px) — a MULTIPLICATION by a bare scalar plus a chain of FOUR
additive terms. `mediaCalcRe`, added in an earlier round for GitHub's
`calc(48rem - .02px)` pattern, only ever matched a single `calc(A ± B)` with
exactly two operands; MDN's five-term product-and-sum expression fell
through unparsed, so `mediaWidthCmpRe` found no width feature in the
condition at all, and `mediaMatches` applied its (deliberate, otherwise
correct) "unknown feature: assume it matches" default — permanently
activating a MOBILE breakpoint's `display:block` override at every
viewport, including a 1024px desktop one.

Fixed by DELETING `mediaCalcRe` entirely and routing media-query calc()
evaluation through `resolveCalc`'s own general arithmetic evaluator
(`evalCalcExpr`, `css/calc.go`) — already used for ordinary property values
(it is what makes Tailwind v4's `calc(var(--spacing) * 48)` spacing scale
work) and already handles the full `+ - * /` grammar with nested parens and
correct length/scalar rules, so no new parser was needed once this was
noticed (a first draft duplicated a smaller version of the same evaluator
directly in `css/parse.go` before this was caught and deleted in favour of
the existing one — see "Errors and fixes" discipline: check for existing
machinery before writing new). `containerConditionMatches` (`@container`
queries, `css/container.go`) shared the exact same two-term-only regex and
was fixed the same way, since nothing about `@container`'s size features is
different from `@media`'s width feature here.

**Verified live: MDN's table-of-contents now renders in its own left-side
column, matching Chrome exactly**, both fixes confirmed necessary together
(with only Bug 1 fixed, the grid still collapsed to `display:block`; with
only Bug 2 fixed, `<main>` still corrupted the column placement). Four
regression tests (`TestDisplayContentsPromotesGridChildren`,
`TestDisplayContentsRecursesThroughNesting` — a `display:contents` nested
inside another, `TestDisplayContentsPlainBlockUnaffected` — the no-op case;
`TestMediaMatchesSimpleCalc` extended with MDN's exact five-term expression
plus the original two-term cases), confirmed to fail without the fix via a
real `git diff > patch; git checkout --; go test; git apply patch` cycle
(the layout tests failed as a BUILD error, since `renderedChildren` becoming
a method is itself part of the fix; the css test failed behaviourally, with
the exact wrong values predicted). `css`/`layout`/`paint`/`dom` all held
their coverage floors (99.5%/100%/100%/98.1%) — two new small test cases
were needed to restore `css` and `layout` to their floors after the
refactor shrank each package's total statement count slightly.

**A third, genuinely unrelated defect was found on the same page and is NOT
fixed: this engine's third-party SVG rasteriser (`github.com/srwiley/oksvg`,
pinned at its latest available commit, `be6e8873`) mis-renders MDN's own
logo**, a small (83×24 viewBox) `<svg>` whose "mdn" wordmark is drawn as a
single complex multi-subpath vector `<path>` (a common technique for brand
wordmarks, avoiding a web-font dependency for three letters) — real
letterforms render as overlapping, doubled glyph shapes. Isolated with an
INCREASING series of reproductions, each ruling out one layer of this
engine's own code: (1) the bug reproduces identically when the ENTIRE page
minus this one `<svg>` is stripped away (a bare `RenderHTML` of just the
logo markup); (2) it reproduces with EACH of the logo's three `<path>`s
rendered alone (isolating it to the "mdn" text path specifically, not the
separate "M" mark or cursor rectangle, both of which render correctly); (3)
it reproduces in a THROWAWAY Go program calling `oksvg.ReadIconStream` +
`rasterx` DIRECTLY, with no go-webengine code in the call path at all —
conclusive proof this is not a bug this engine's own `svg.go` (path
extraction, viewBox scaling, target sizing) could cause or fix. Checked
whether a newer oksvg commit already fixes it (per this session's own
"check if the real fix is smaller than it looks" discipline): the pinned
commit IS the repository's newest available commit, so no upstream fix
exists to pick up by bumping the dependency. Not attempted here: fixing (or
forking) a third-party rasteriser's own path-fill-rule/subpath handling is
outside this engine's own codebase and a materially different kind of task
than every other fix this session has scoped and shipped.

## 2026-09-03 (cont., round 24) — a `<button>`'s label no longer includes text under a `display:none` descendant (engine#104); a bigger, related layout gap found and documented, not fixed

github.com/golang/go's site-header search trigger rendered its visible text
as **"Search/"** — Chrome shows only a magnifying-glass icon at this
viewport width. Live-fetched the real markup and CSS (`github.githubassets.com`'s
`lazy-react-partial-marketing-header*.css`) rather than guessing: the button
nests `<span class="…label…">Search</span><kbd class="…kbd…">/</kbd>`, each
shown at a DIFFERENT responsive breakpoint via `display:none`/`display:block`
on the nested span/kbd (`@media (width>=63.25rem) and (width<=87.499rem)`
hides both text and the shortcut hint, showing only the icon) — never both at
once in a real browser.

Root cause: this engine treats every `<input>`/`<button>`/`<select>`/
`<textarea>` as an ATOMIC box (`layout.go`'s `isFormControlTag` branch — "a
`<button>`'s children become its LABEL, not child boxes", by design, not a
bug in itself). A `<button>`'s label was `dom.TextContent(node)` — a raw,
style-blind concatenation of every descendant text node, in both the width
calculation (`formControlDefaultSize`) and the paint step
(`formControlDisplayText`). Confirmed via an offline cascade of the real
markup + real CSS (`css.CascadeVW`, no live pipeline involved) that the
label span and kbd hint were BOTH correctly resolving to
`Display: DisplayNone` at this viewport — the cascade was never the
problem; `dom.TextContent` simply has no notion of computed style and
walks every text node regardless.

Fixed narrowly, reusing the style map layout already has (`l.sm`) rather
than threading one into `paint`, which has none: added
`layouter.buttonLabel`/`appendVisibleText` (mirrors `dom.TextContent`'s own
recursive walk in `dom/mutate.go`, just pruning a `display:none` element's
entire subtree instead of recursing into it), called at BOTH of a
button's two `InlineItem`-construction sites (`contents()`'s
`display:block` entry point and `appendElementInline`'s ordinary-inline
entry point — the codebase's own established pattern of covering both, see
the hidden-input tests). The result is stored on a new
`InlineItem.Label` field, computed once at layout time; `paint`'s
`formControlDisplayText` now takes that precomputed label instead of
re-deriving it from `dom.TextContent` itself (paint has no style map to
redo a display:none-aware walk from the DOM node alone). Verified live:
the button's rendered text is now empty of visible content (an icon-only
button, so it falls back to "Submit" — the SAME pre-existing fallback
every icon-only/empty button already had; this fix does not add or change
that fallback, it just stops "Search/" from ever reaching it).
Regression tests: `TestFormControlButtonLabelSkipsDisplayNoneDescendants`
and its `display:block` counterpart, confirmed to fail (a build failure,
since `InlineItem.Label` and `formControlDisplayText`'s new parameter are
themselves part of the fix) via a real `git diff > patch; git checkout --;
go test; git apply patch` cycle. layout and paint both hold their 100%
coverage floor.

**A bigger, related gap surfaced during this same investigation and is
NOT fixed**: two sibling `<a>` buttons ("Sign in", "Sign up") in the same
header render with **zero gap** between them ("Sign inSign up"), despite
the real CSS giving the wrapping element `margin-right: 8px` (confirmed,
via the same offline-cascade technique used above, to resolve correctly —
this is not a `var()` or cascade bug). Root-caused by instrumenting the
live layout box tree directly: GitHub wraps its entire marketing header in
`<react-partial>`, an unstyled CUSTOM ELEMENT (falls back to this engine's
default `display:inline`, since nothing in the UA stylesheet or any
fetched CSS targets it) — and this engine's inline-content collector
(`appendElementInline`'s `default:` case, `layout.go`) has NO check for a
nested element's OWN display resolving to block-level: it unconditionally
recurses via `appendInline`, flattening the `<react-partial>`'s ENTIRE
subtree — including a real `<header>`, `<nav>`, and a `display:flex` CTA
row several levels down — into one inline run of words and atomic
(image/form-control) items. A block/flex/grid box nested under an inline
ancestor this way never reaches `l.place()`/`l.flex()` at all, so it never
gets a real box, margin, or flex-layout pass; two adjacent `<a>` elements
with no text node between them in the DOM (the exact shape here) then
render with the bare "no whitespace between them" spacing inline content
would have, not their own margin. This is architecturally the same class
of gap as this engine's already-documented, deliberately-deferred ones
(Shadow DOM's slot projection, the reskin/orphaned-node case above): CSS's
real behaviour here is to generate an anonymous block wrapper splitting
the inline run around the nested block box and promote that box to a real
sibling (CSS 2.1 §9.2.1.1) — a genuine layout feature, not a narrow bug fix,
and this session's "no speculative capability" discipline argues against a
narrower special case (e.g. hardcoding `<react-partial>` by tag name) that
would only cover the one custom element anticipated in advance rather than
the general "unstyled/inline wrapper around real block content" shape a
future page could hit just as easily with a `<div>` inside a `<span>`.

news.ycombinator.com's story subtext ("N points by user … | hide | N
comments") rendered CENTERED under each title instead of left-aligned like
Chrome. Root-caused live: HN's whole page is `<html lang="en" op="news">` —
**no `<!DOCTYPE>` at all** — wrapped in `<center><table id="hnmain" ...>`,
the classic legacy pattern for horizontally centering a fixed-width table on
the page. A missing doctype triggers real browsers' "quirks mode", whose UA
stylesheet resets `table { text-align: initial; … }` — WITHOUT this, this
engine's normal (correct, standards-mode) inheritance carried `<center>`'s
centered-text-align all the way down into every `<td>`, centering content
the page never intended to center.

This engine had **zero quirks-mode concept anywhere** — `dom.Parse` (per its
own doc comment) discarded any `DoctypeNode` outright. Added the minimum
needed, scoped narrowly rather than implementing the HTML spec's full quirks
algorithm: `dom.Node` gained a `Quirks bool`, set true when the source has
NO `<!DOCTYPE ...>` at all (the single most common real-world trigger; the
spec's rarer legacy-doctype-identifier triggers are not modelled — a page
reaching this engine either has `<!DOCTYPE html>` or none at all in
practice). `css.CascadeVW` reads it once and threads it into `uaDeclarations`,
which now adds `text-align:left` to `<table>`'s UA default in quirks mode —
only that one property, of the several the spec's quirks-mode `table{...}`
rule actually resets (font-weight/style/variant/size/line-height/white-space
too), since only `text-align` has a confirmed live defect to fix; adding the
others would be guessing, not fixing.

**This interacted with an existing mechanism and needed care to get right**:
`AlignCenterBlocks` (this engine's value for legacy `<center>`/`align=center`)
already does double duty — it both centers a definite-width block/table AS
A BLOCK within its container (`layout.go`'s width-resolution switch) AND
centers INLINE TEXT like ordinary `text-align:center` (`alignOffset`). My
first attempt (a bare UA-level `text-align:left` on `<table>`) broke
`TestTableFixedWidthCentredInCenter`: overriding the table's OWN TextAlign
also destroyed the "am I inside a `<center>`, so should I be block-centered"
signal the width-resolution code read from that SAME field, un-centering
the table itself. Real browsers keep the two questions independent (a
`<table>`'s own computed `text-align` genuinely does become `start` in
quirks mode per spec, yet the table still visually centers) — so this
engine now does too: `Style.CenterAsBlock` is `TextAlign==AlignCenterBlocks`
for every element EXCEPT a quirks-mode `<table>`, which also stays true
when its PARENT (the `parent Style` cascade already passes in, unaffected
by the table's own reset) is centered. Layout's
block-centering check now reads `CenterAsBlock`, not `TextAlign` directly.

**Verified live: HN's subtext now renders left-aligned under each title,
matching Chrome**, while a table-in-`<center>` still centers correctly
(confirmed both live and via the pre-existing test that first caught the
regression). Measured: news.ycombinator.com SSIM 0.598→0.596, pixdiff
12.9%→13.1% — a small, WITHIN-NOISE move in the "wrong" direction, honestly
reported: Hacker News is a live, constantly-updating front page (its story
list, points, and comment counts differ between the two measurement runs,
confirmed by the differing page heights: 2261px vs 2280px) — this is the
SAME live-content-drift category already documented for tailwindcss.com and
others this session, not a regression from the fix itself (confirmed
separately, directly, via the two dedicated before/after screenshots this
entry is based on).

New regression tests: `TestParseQuirksMode` (`dom/dom_test.go` — a document
with no doctype is flagged `Quirks`; one with `<!DOCTYPE html>` is not) and
`TestQuirksTableCellTextNotCentered` (`layout/table_pres_test.go` — a `<td>`
inside a quirks-mode `<center><table>` must NOT have its text centered).
Both confirmed to fail with the exact wrong (missing `Quirks` field /
centered-not-left-aligned) result before this fix, via a temporary revert of
`dom/dom.go`, `css/cascade.go`, `css/ua.go`, `css/value.go`, and
`layout/layout.go` together.

## 2026-09-03 (cont.) — a list-item marker inside a flex/grid/table/float item was left at its pre-layout position (engine#102)

caniuse.com's "Most searched features" numbered list (`<ol style="list-
style:decimal">`, one of three columns in a `display:flex` section) showed
no visible "1."/"2."/… markers at all where Chrome shows a clean numbered
list. Root-caused with a live dump of the actual `Marker.X` values rather
than guessing from the screenshot: they were small, near-zero-ish numbers —
nowhere near the ~370px the list's real flex-column position should put
them at.

`attachMarker` (`layout/marker.go`) computes a marker's absolute X/Y once,
from the box's `ContentX`/first-line `Y` at the moment it runs — the SAME
absolute-coordinate convention every other field in this package uses. But
several layout algorithms lay a subtree out at a temporary, near-origin
position FIRST and only afterward call `translateBox` to shift it into its
real place: `flex.go`'s row cross-axis and column main-axis placement,
`grid.go`'s item placement, `table.go`'s cell placement, and `floats.go`'s
own `placeFloat`. `translateBox` shifted every field it knew about
(`X`/`Y`/`ContentX`/`ContentY`, every line, every inline item) but never
`Box.Marker` — so a list whose `<li>` got repositioned by ANY of these four
mechanisms AFTER `attachMarker` ran kept its marker glued to the
pre-translation coordinates, rendering it off in whatever happened to sit
at the box's OLD position (usually nothing, since the intervening space is
typically empty gutter) rather than beside its own item.

Fixed with a two-line addition to `translateBox` itself — shift
`box.Marker.X`/`.Y` by the same `(dx, dy)` as everything else — rather than
patching each of the four call sites separately, since the bug is in the
one shared function all of them go through.

**Verified live: caniuse.com's numbered list now shows "1. AVIF", "2. WebP",
"3. CSS Grid", "4. dvh (…)", matching Chrome.** Measured: caniuse.com SSIM
0.653→0.652, pixdiff 21.7%→21.8% — essentially flat, honestly reported: five
small digit-plus-period markers are a tiny fraction of the page's total ink,
and general font-rasteriser noise dominates the aggregate score either way.
The fix is broadly applicable — ANY marker-bearing list (`<ol>`/`<ul>`, or
any `display:list-item`) placed inside a flex item, grid item, table cell,
or float anywhere on any page, not specific to this one list.

New regression tests: `TestTranslateBoxMovesMarker` (a direct unit check
that `translateBox` shifts a box's `Marker` field, not just its other
fields) and `TestFlexItemListMarkerPositionedInItsOwnColumn` (an end-to-end
check — a numbered list as the SECOND item in a flex row must have its
marker positioned inside its OWN column, not left behind near the first
item's position). Both confirmed to fail with the exact wrong (pre-
translation) coordinates before this fix, via a temporary revert of
`layout/floats.go`.

## 2026-09-03 (cont.) — go.dev/blog's nav shows raw icon-ligature text ("arrow_drop_down"); root-caused as a genuine but out-of-scope gap, not fixed

Chased go.dev/blog's pixdiff (28.0%) visually, at full resolution from the
start (per round 19's own "don't trust the thumbnail" lesson). Immediately
visible: the top nav shows literal text "arrow_drop_down" three times where
Chrome shows small dropdown-caret triangles.

Root cause, confirmed against the real page's markup and CSS: this is
Google's Material Icons convention — `<i class="material-icons">
arrow_drop_down</i>`, where the element's TEXT CONTENT is a semantic
keyword a custom `@font-face` TTF (fetched from `fonts.googleapis.com`)
substitutes for a single glyph via its own OpenType GSUB ligature table,
entirely at the font level. `css/parse.go` skips `@font-face` wholesale
(like any unrecognised at-rule), so `font-family:'Material Icons'` falls
back to this engine's normal font stack, which has no matching glyph and
no ligature mechanism to invoke — the raw keyword renders literally.

**Checked whether this was smaller than it looked before writing it off**:
`go-opentype/opentype` (already a dependency) has real GSUB support
(`gsub.go`), and a sibling `go-opentype/shape` package exists specifically
to drive it — but `paint/fonts.go`'s `Measure`/`Metrics` call
`opentype.Face` directly (plain per-rune cmap+advance lookups) for every
one of this engine's existing bundled fonts; `go-opentype/shape` is not
used anywhere in this engine yet. A real fix needs BOTH a wholly new
capability (fetch and load an arbitrary `@font-face` TTF by its declared
family name, the way a browser does) AND rewiring text measurement/painting
through real shaping instead of the current no-shaping model — genuinely
comparable in scope to Shadow DOM or the reskin-orphaned-node architecture
already deferred this session, not a narrow bug fix.

**Not fixed.** Considered and rejected a narrower mitigation (hide text for
a hardcoded list of well-known icon-font family names) as the same kind of
speculative, name-guessing special case this session has avoided
elsewhere — it would only ever cover icon fonts anticipated in advance.
Documented as a new Known Gap below rather than forced into an unfitting
narrow fix.

## 2026-09-03 (cont.) — react.dev's washed-out homepage prose re-investigated: the SAME known gap, a far bigger blast radius, still not fixed

react.dev is the corpus's worst page by pixdiff (47.6%). Chased visually
rather than assuming the existing "hero h1/one button" Known Gap entry
already covered it fully: nearly every heading and paragraph on the
homepage, not just the hero, renders washed-out/low-contrast against the
dark background.

Ruled out two live hypotheses BEFORE settling on the real one, per this
session's own "verify empirically, don't stop at plausible-sounding CSS
reasoning" discipline:

- **Not a `:is()`/escaped-class-name selector bug.** react.dev's design uses
  Tailwind's `--tw-text-opacity` CSS-variable colour pattern gated by
  `:is(.dark .dark\:text-primary-dark)`-shaped dark-mode selectors. Traced
  this engine's own selector parser line by line (`splitPseudos` skips a
  backslash-escaped colon when finding pseudo boundaries; `scanCompound`
  correctly unescapes it back to a literal `:` in the stored class name) —
  the mechanism is correct. Confirmed empirically too: with `DisableJS`,
  the light-mode colour resolves exactly as expected.
- **The real cause: with JS enabled, `<html class="dark">` IS correctly
  present, but the settled DOM has ZERO `<h2>` elements at all.** The SAME
  router-failure script pass documented for the hero (engine#92's
  `reskin()` Known Gap) unmounts the ENTIRE marketing homepage content, not
  just the hero — a much bigger blast radius than that entry's own wording
  suggested. Every unmounted node keeps its pre-failure (light-mode) style
  forever, per `reskin()`'s existing, correct-as-far-as-it-goes behaviour:
  a node absent from the rejected pass's freshly-cascaded style map has
  nothing to update its `Style` pointer TO.

**Not fixed this round.** A real fix needs either a DOM diff across the
rejected pass (to tell "removed, keep last-known style" apart from "still
present, re-cascade normally") or re-cascading an orphaned subtree against
a synthetic root that reproduces its lost ancestor chain — `dom.RemoveChild`
nulls a node's own `.Parent` at the moment of removal, so that chain is not
recoverable after the fact without having snapshotted it beforehand. Either
is a real architectural addition, assessed (again) as bigger than this
session's per-round scope — but the Known Gaps entry below is corrected to
the true, now-measured scope, which is materially larger than previously
documented and makes this a stronger candidate for a dedicated future
session than the old wording implied.

## 2026-09-03 (cont.) — the `background` shorthand didn't reset colour when it had none, so engine#98's own fix survived only partway (engine#99)

Verifying engine#98 live (github.com's nav `<button>`s no longer painting a
hardcoded grey box) turned up a SECOND bug in the same spot: the grey box
was still there, just now sourced from a genuinely CASCADED value rather
than a hardcoded one — engine#98's own new UA-default background-color for
`<button>` was surviving the author's own reset unchanged.

Root cause, found by dumping the live cascade result rather than assuming
engine#98 alone was sufficient: GitHub's reset is `background:0 0;border:0`
— the `background` SHORTHAND, not `background-color`. Per CSS, a shorthand
resets every sub-property it represents to its initial value when the given
value doesn't mention it — `background-color`'s initial value is
transparent. `css/parse.go`'s `background` case never modelled this: it
only assigned `s.Background` when it found an actual colour token in the
value, and silently left whatever was there before (a prior declaration, or
now, a UA default) untouched otherwise. `background:0 0` (bare position,
GitHub's own exact value), `background:url(x) no-repeat` (image only, a
common real pattern for layering an icon over a differently-coloured
ancestor), and `background:none` all hit this — none carry a colour token,
so none ever reset a stale background-color, regardless of what author CSS
actually says.

Fixed by resetting `s.Background` to `Transparent` FIRST, unconditionally,
then overwriting it with a parsed colour token if the value has one —
matching the shorthand's real reset semantics with a two-line change.
`border`'s own shorthand handler (`applyBorderShorthand`) already had this
right (it builds a fresh zero-value `BorderSide` and blanket-assigns all
four edges, so an unrecognised `border:0` correctly ends up `BorderNone`
regardless) — confirmed by checking it before assuming the same class of
bug existed there too, rather than fixing both defensively.

**Verified live: github.com's nav buttons now render as genuinely
transparent, matching Chrome.** Measured: github.com/golang/go SSIM
0.531→0.532, pixdiff 32.9%→32.8% — essentially flat two rounds in a row now
for the SAME reason both times (a small header fraction of a long,
text-dense page dominated by font-rasteriser noise) — the fix is general
(any page whose CSS resets a background/image-only shorthand on a control
or any other element), not specific to this one page's number.

New regression test `TestBackgroundShorthandResetsColorWhenAbsent`
(`css/modern_test.go`) covers all three colour-less shorthand shapes
(`0 0`, `url(...) no-repeat`, `none`) against a PRIOR non-transparent
background-color, confirmed to fail with the exact wrong (un-reset) colour
before this fix.

**Lesson: verifying a fix live caught what the unit tests alone did not** —
engine#98's own tests all passed (they exercise `paintFormControl` reading
`Style.Background` correctly; none of them exercised the CASCADE producing
a wrong `Style.Background` in the first place from this specific shorthand
shape). A live render is what surfaced that the visible bug persisted
despite a "correct and tested" fix landing — the same discipline this
session has applied to every prior round, now caught a defect in this
session's OWN immediately-preceding work, not just in the code being
investigated.

## 2026-09-03 (cont.) — a form control's background/border now comes from its own cascaded style, not a hardcoded colour (engine#98)

github.com/golang/go's top navigation ("Platform", "Solutions", "Resources",
"Open Source", "Enterprise") rendered as solid grey pill buttons — but a raw
fetch of the real page shows these are literal `<button>` elements styled by
GitHub's own CSS with `background:0 0;border:0`, meant to look like plain
text links with a dropdown caret, not visible buttons at all.

Root cause: engine#95's new form-control paint step (`paintFormControl`)
drew every button/select's background and border from two flat, hardcoded
package-level colours (`formButtonBg`, `formBorder`), completely independent
of `it.Style` — so ANY author reset of a control's default appearance (an
extremely common pattern; virtually every professionally-designed site
restyles its buttons rather than accepting the raw UA look) was silently
overridden by fake generic chrome. This is a broadly-applicable regression,
not specific to this one page's header.

Fixed at the source rather than special-casing paint: `css/ua.go` never gave
`button`/`select`/`input`/`textarea` a background-color or border UA default
at all (only `display:inline`) — so the cascade had NOTHING for an author
rule to actually override; `Style.Background`/`Style.Border` were always
empty/transparent for these tags regardless of styling, which is why paint
had to hardcode its own colours in the first place. Added real UA defaults
(matching paint's own prior hardcoded values exactly, so an unstyled
control's appearance is unchanged): `button`/`select` get a light-grey
background + grey border, `input`/`textarea` get white + grey border,
narrowed by two new `uaDescendantRules` attribute-selector overrides —
`input[type=button/submit/reset]` gets the button look, `input[type=
checkbox/radio]` gets no generic background/border at all (paint's own
checkbox-square renderer never consults these fields for them). `paint/
paint.go`'s `paintFormControl` now reads `it.Style.Background`/`.Border`
directly instead of choosing between the two hardcoded constants, painting
nothing for a zero-alpha background or a `border:0`/`border-style:none`
side — exactly the mechanism every other element's background/border
already uses in this engine, just newly extended to form controls.

**Verified live: github.com's nav items no longer render as grey pills.**
Measured: github.com/golang/go SSIM 0.531→0.532, pixdiff 32.9%→32.8% —
barely moved in the aggregate, honestly reported rather than oversold: the
fixed header is a small fraction of a long (2989px), text-dense page whose
diff is dominated by ordinary font-rasteriser variance, the same category of
noise already documented for Wikipedia and several other pages this
session. The fix's real value is general (any styled button/select/input on
ANY page, not just this one), independently of this page's own number.

New regression tests confirmed to fail against engine#95's original
hardcoded-colour implementation before this fix (reverted via a temporary
`git checkout` of `css/ua.go`/`paint/paint.go` covering just this round's
diff): `TestPaintFormControlHonoursAuthorReset` (a zero-alpha background and
a zero-width border must paint nothing, not the UA fallback colours) is the
core new case; `TestPaintFormControlDrawsBackgroundAndBorder` and
`TestPaintFormControlButtonBackground` were updated to pass an explicit,
realistic cascaded style (matching what `css/ua.go` now actually produces)
instead of relying on paint's own hardcoded fallback, so they keep testing
the same visible-box contract through the new mechanism rather than around
it.

## 2026-09-03 (cont.) — `<select>` sizes to its widest option and shows the correct one, matching the real cross-engine pattern (engine#96)

Round engine#94 (below) fixed pkg.go.dev's garbled index block by hiding
`<option>` (`display:none`) — a real fix, but left EVERY `<select>` on any
page showing nothing at all. Asked directly whether that fix had been
checked against Firefox/WebKit source (per this project's own bibliography
discipline) — it had not been, only against this engine's own prior
`<template>` precedent and live pkg.go.dev diagnosis. Reading both engines'
source afterward (`searchfox.org`'s `nsListControlFrame`/
`nsComboboxControlFrame` for Firefox, `RenderMenuList.cpp` for WebKit — a
`RenderFlexibleBox` subclass) showed the real common pattern: a `<select>`
is a replaced, widget-backed element with a dedicated render object that (1)
measures every option's label to size the control to its widest possible
entry (WebKit's `updateOptionsWidth()`), and (2) paints only the currently
selected option's label, on one line.

**Between diagnosing this and shipping it, a different session merged
engine#95, which independently built exactly the box+paint infrastructure
this needed** — `isFormControlTag`/`formControlSize` in `layout/layout.go`
and `paintFormControl`/`selectedOptionLabel` in `paint/paint.go`, covering
`input`/`button`/`select`/`textarea` generically. Its own `select` handling
was a placeholder, though: a flat `170px` width regardless of any option
(the same constant used for a plain text `<input>`), and its
`selectedOptionLabel` picked the FIRST `<option selected>` rather than the
LAST, with no disabled-option or `<optgroup>` handling — a reasonable first
cut for the PR's own driving goal (making a login form's text fields and
buttons clickable), just not spec-correct for `<select>` specifically.
Discovered this by re-syncing with `origin/main` mid-round (`gh pr list`
showed #95 had landed) rather than by re-diagnosing from scratch, and
rebuilt this round's fix ON TOP of it instead of shipping a second,
competing atomic-box mechanism.

Implemented per the HTML standard's own normative algorithms (read at
https://html.spec.whatwg.org/multipage/form-elements.html, not reconstructed
from memory):

- **`layout/select.go`** (new): `optionLabel` (an option's `label` attribute
  if present and non-empty, else its text content) and `selectOptionLabels`
  (every option's — and each `<optgroup>`'s own — label, for sizing).
- **`layout/layout.go`**: `formControlDefaultSize`'s `"select"` case now
  measures every option's label via the existing `Measurer` and sizes to the
  widest, falling back to the flat default ONLY when there are no options to
  measure at all — not as a floor a real, narrower option set gets clamped
  up to (a `<select><option>x</option></select>` really is a tiny box in a
  real browser).
- **`paint/paint.go`**: `selectedOptionLabel` rewritten to the standard's
  option selectedness algorithm
  (https://html.spec.whatwg.org/multipage/form-elements.html#concept-option-selectedness):
  the LAST `<option selected>` wins for a non-`multiple` select; with none
  selected, the FIRST option that is not itself `disabled` and whose
  ancestor `<optgroup>` (if any) is not disabled wins instead; a new
  `optionLabel` (duplicated from layout's, matching this file's own existing
  `controlLabel` duplication convention) applies the `label`-attribute
  override.

**Verified live on pkg.go.dev/net/http**: its platform selector shows
"linux/amd64" inside a real, visible box (matching Chrome) instead of a flat
170px empty field; its version/tab-switcher `<select>` sizes to its actual
label instead of the generic text-input default.

New regression tests (`layout/formcontrol_test.go`, `paint/formcontrol_test.go`)
cover: sizing to the widest option (including through `<optgroup>` nesting
and a `label`-attribute override), staying narrower than the flat default
when real options are all short, last-selected-wins, disabled-option and
disabled-`<optgroup>`-inheritance skipping for the default case, and the
`label`-attribute override on the displayed value. Confirmed each of the
four `paint` behavioural tests and three `layout` sizing tests fails against
engine#95's original placeholder implementation (reverted via a temporary
`git checkout`/file-move covering just this round's changes) before
restoring the fix — one wrong answer per broken standard rule, not a
generic "something's off" failure.

**Deliberately not attempted**: an actual popup/listbox surface (this engine
never renders anything interactively beyond what a settle pass produces),
`<optgroup>` rendering as a visually distinct row in that non-existent
popup, and the HTML spec's exact "text" IDL attribute whitespace-collapsing
rule (approximated with a plain trim, matching this engine's existing text
handling elsewhere).

**Lesson, worth repeating alongside round 14/15's own version of it**: this
is the THIRD time in three consecutive rounds that another session merged
a PR touching the exact area under investigation between diagnosis and
shipping (#89-91 during round 14, #93 during round 15, #95 during this one)
— `gh pr list` before every `gh pr create` on this repo is no longer an
occasional courtesy check, it is load-bearing.

## 2026-09-03 — pkg.go.dev's garbled top-of-page text block root-caused: `<option>` had no user-agent styling at all

pkg.go.dev/net/http had the corpus's highest pixdiff (45.8%) after the
react.dev flex-bare-text fix. Chased visually: a dense, unbroken block of
concatenated identifier text ("DocumentationSourceFilesDirectoriesOverview
IndexExamplesConstantsVariablesFunctionsTypesCrossOriginProtectionFileServer…")
rendered near the top of the page, overlapping the sidebar's "Details/
Repository/Links" section — not the page's real, legitimate `Documentation-
index` symbol list (confirmed present and correctly positioned much further
down, at the same place a real browser puts it).

Root-caused with a live box-tree dump (a temporary white-box `_test.go` in
package `engine`, calling the same internal `renderCore` pipeline `Render`/
`RenderHTML` use, walking the real box tree for any text containing a marker
string unique to the garbled block) rather than guessing from the screenshot —
the same technique used in round 4's tailwindcss.com grid investigation. The
match traced straight to a `<select class="go-Select js-selectNav">`
(pkg.go.dev's version/tab-switcher dropdown, `go-Main-navMobile`) whose
`<option>` children include the page's entire alphabetical symbol/example
index as option text.

**This engine's user-agent stylesheet has never had any special case for
`<option>`** (`css/ua.go`) — `<select>` itself falls into the same generic
`display:inline` bucket as `<button>`/`<input>`/`<textarea>`, but unlike those
(which are void or have no meaningful child text), a `<select>`'s `<option>`
children carry real, visible text nodes. A real `<select>` is a replaced,
OS-native control that shows only the currently selected option on one line,
entirely opaque to CSS box layout; without any UA rule saying so, this
engine laid out every `<option>`'s text as ordinary inline content, wrapping
across ~19 lines/455px at the select's DOM position. Fixed with one addition
to `uaDeclarations`: `option { display: none }` — the exact same "honest
about the gap" precedent already used for `<template>`'s inert content just
above it in the same file (this engine has no native form-control rendering
at all; an `<input>`'s `value` going unshown is an existing, accepted
simplification this now matches rather than diverges from).

**Verified live:** the garbled block is completely gone from
`cmd/render`'s output; the page's real content (Overview, code examples,
Clients and Transports, Servers, …) now lines up closely with Chrome's
rendering from the very top of the page.

**Measured:** pkg.go.dev/net/http SSIM 0.530→0.616, pixdiff 45.8%→40.0%.
Confirmed via a full 10-page bench corpus re-run that nothing else regressed;
every other page's number held within its already-documented noise band.

Regression test `TestSelectOptionsDoNotLeakIntoLayout` (`layout/cover2_test.go`)
reproduces the failure shape directly (a `<select>` with two long `<option>`
texts, asserting neither string appears in ANY box's laid-out text anywhere
in the tree — the first version of this test only checked the document
root's own `Lines` field, which is always empty for a root `<html>` box, and
so passed even without the fix; walking the full tree was needed to actually
exercise the regression, caught by reverting the fix locally and watching the
corrected test fail with the predicted leaked text before restoring it).
`css/ua_test.go`'s `TestUADeclarationsAllBranches` was extended to cover the
new `option` branch, holding the `css` package's coverage-ratchet gate at
its 99.5% floor.

## 2026-09-02 (cont. 3) — a real flexbox layout bug fixed: a `display:flex` element with bare text content collapsed to zero size

react.dev held the corpus's highest pixdiff for a while: its hero `<h1>React</h1>`
and both CTA buttons ("Learn React", "API Reference") rendered as empty
space — not hidden, not the wrong colour, simply ABSENT, with none of their
layout space reserved either.

Root-caused as a genuine flexbox layout engine bug, confirmed with a live
box-tree dump then isolated to a minimal repro
(`<h1 style="display:flex">React</h1>`) BEFORE touching any code — two
compounding gaps, both in code that only ever expected a flex container's
content to be wrapped in element children:

- **`preferredWidth`'s flex-row branch summed only `Element` children's
  widths**, so a flex container whose content is bare text with no wrapping
  element at all (forming a single anonymous flex item per spec — a very
  common Tailwind/utility-CSS pattern: `display:flex` added to an element
  purely for `align-items`/`gap`, with nothing else inside) counted zero
  children and returned a bare `0`, collapsing the element's shrink-to-fit
  width to zero.
- **`flexItems` has the identical Element-only blind spot**, so `flex()`
  itself collected zero items for such a container and returned immediately
  having laid out nothing at all — collapsing height to zero too, independent
  of the width bug.

Fixed both by falling back to the SAME inline-formatting-context path
`contents()` already uses for a plain, non-flex, no-block-child element:
correct for the common single-line case (there is only ever one synthetic
item, so justify-content/align-items/gap have nothing to distribute across,
and are not modelled for it — a genuinely empty container, no element AND no
text, still correctly collapses to zero, unaffected by this fix).

**Verified live:** the "Learn React" button now renders with its real text
and pill shape at the correct size. The `<h1>` and "API Reference" button
now ALSO occupy correct, non-zero layout space with the right text content —
but remain visually invisible for a SEPARATE, already-diagnosed reason:
react.dev's client-side router failure (the same failure `reskin()`,
engine#84, already guards against) removes these specific DOM nodes from the
live tree in the same script pass that ALSO toggles dark mode; `reskin`
correctly preserves such an orphaned node's PRE-failure style (its node is
no longer in the newly-cascaded style map at all, so there's nothing for it
to update to), which for these ones predates the dark-mode class add,
leaving them stuck in `text-primary`'s light-mode colour on a dark
background. This is a real, narrower limitation of the reskin mechanism
(fixing it well would need diffing the DOM before/after a rejected pass
rather than a single before/after style map) — noted here rather than
chased into a much larger redesign this round.

**Measured:** `bench/cmd/compare`: SSIM 0.587→0.605, pixdiff 49.5%→47.6%.
Confirmed via the full 10-page bench corpus that go.dev/blog ALSO improved as
an unplanned side effect (pixdiff 31.1%→28.1%) — this bug was never specific
to react.dev, and likely affects any page using the same common
`display:flex`-on-a-text-only-element pattern.

## 2026-09-02 (cont. 2) — CSS `mask-image` implemented, and background/mask SVG sources now actually decode

en.wikipedia.org held the corpus's lowest SSIM (0.422) since long before this
week's fixes. Chased visually: the entire top toolbar's icons (hamburger
menu, search, language switcher, user/notification icons) rendered as plain
solid-coloured squares instead of their real shapes.

- **CSS `mask-image` (and `-webkit-mask-image`) was entirely unimplemented.**
  Modern MediaWiki's Vector-2022 skin — like a great many icon systems across
  the web — renders EVERY toolbar icon as an empty `<span>`, sized to a small
  square, painted with a solid `background-color`, and cut into its real
  shape by `mask-image: url(...)` (an alpha stencil over everything the
  element paints, not a second background layer) — precisely so the icon can
  recolour for dark mode from CSS alone, with no second image asset. Without
  mask support, the `<span>` simply painted as a solid square. Implemented:
  `css.Style.MaskImage` (only a single `url()` mask is modelled — a gradient
  or multi-layer mask is left unsupported, this engine's one deliberate
  simplification, matching how narrowly `transform: translate()` was scoped
  two entries above), and `paint.applyMask`, which reuses the SAME
  offscreen-group-buffer mechanism `opacity`/`filter` already use (render the
  element's subtree into an isolated buffer, then multiply its alpha by the
  mask's alpha before compositing) rather than inventing new paint
  machinery. The mask is stretched to fill the element's own border box —
  the real `mask-size`/`mask-position`/`mask-repeat` grammar is not modelled
  — and anything a child paints OUTSIDE that border box is fully masked out,
  matching the spec's "mask affects the whole element" rule.
- **Found and fixed a SECOND, deeper bug the first one exposed: `mask-image`
  (and, it turns out, ordinary `background-image`) URLs pointing at an SVG
  document never actually decoded — at all, regardless of this fix.**
  `loadOneBackground` only ever called the raster-only `codec.Decode`; the
  `<img>`/inline-`<svg>` path's own SVG rasteriser (`svgToBitmap`) was never
  wired in for background/mask sources. Worse: even the URL-based
  vector/raster BUDGET check (`srcLooksLikeSVG`) matches only a literal
  `.svg` path or an `image/svg` string in the URL — Wikipedia's icon service
  serves SVG from an extensionless, query-string-driven URL
  (`load.php?...&image=menu&format=original`, `Content-Type: image/svg+xml`
  only in the HTTP response), which that heuristic cannot see. Fixed by
  routing `loadOneBackground` through `looksLikeSVG` (the SAME helper
  `<img>` loading already uses, which falls back to sniffing an `<svg`
  tag in the fetched bytes when the URL gives no hint) before falling back
  to raster decode.
- **Verified live:** every toolbar icon (hamburger menu, search, language
  switcher, and more) now renders its real shape.
- **Honest measurement note:** the aggregate SSIM/pixdiff for
  en.wikipedia.org barely moved (0.422 unchanged, 22.4%→22.4%) despite this
  real, visually-confirmed fix — the icons affected are a tiny fraction of a
  very tall (24818px), extremely text-dense page, and the region comparison
  is dominated by inherent font-rasteriser variance between this engine and
  Chrome across that dense body text (a long-documented, expected,
  never-reaches-zero source of "difference" on prose-heavy pages — see this
  file's `Reproduce`/`Visual-fidelity protocol` sections). `mask-image` and
  SVG background/mask decoding are real, general capabilities confirmed to
  matter for at least one live site's entire icon system, independent of
  whether they moved this specific page's score.

## 2026-09-02 (cont.) — pkg.go.dev's header overlay closed: `transform: translate()` and native `<details>`/`<summary>` hiding

pkg.go.dev/net/http had the corpus's highest pixdiff (47.1%) after the
tailwindcss.com fix below. Chased visually: a mobile navigation drawer
rendered permanently expanded across the whole page, overlapping the header
and content, and a help tooltip's real text rendered permanently visible in
a bordered box at the very top-left of the page. Two separate, unrelated
root causes:

- **This engine had NO CSS `transform` support at all** (still true in
  general — see Known gaps below), but pulled out ONE well-scoped subset:
  `translate`/`translateX`/`translateY`, a pure 2D offset with no layout-flow
  effect, applied exactly like a relative-position shift. pkg.go.dev's
  mobile nav drawer hides itself off-screen with
  `transform: translate(100%)` (only brought into view by a JS-toggled
  `.is-active` class setting `translate(0)`); its OWN `display:none` is
  gated behind `@media (width >= 65rem)`, which correctly does NOT apply at
  this engine's 1024px (64rem) test viewport — matching a real browser,
  which relies on the transform, not `display`, to hide it there. Without
  transform support, the drawer's real `display:block` at this viewport
  rendered it fully in place instead of pushed off-screen. Implemented in
  `css/parse.go` (`parseTransformTranslate`) + `layout/position.go`
  (`applyTransformTranslate`, threaded through the existing relative-offset
  pass and, separately, applied AFTER an absolutely/fixed-positioned box's
  final placement — applying it any earlier gets exactly cancelled by the
  placement math, caught by a dedicated regression test). A `transform`
  naming any OTHER function (rotate, scale, skew, matrix, 3D, or a mix
  including translate) is left entirely unsupported, same as before.
- **Native `<details>`/`<summary>` disclosure semantics were entirely
  unimplemented** — a closed `<details>` (the default; no `open` attribute)
  rendered ALL its content, not just the `<summary>`. pkg.go.dev's help
  tooltips are exactly `<details class="go-Tooltip"><summary>...</summary>
  <p role="tooltip">the tooltip text</p></details>`, only ever opened by a
  click a static render never triggers. Fixed as a genuine UA-stylesheet
  rule (`details:not([open]) > :not(summary) { display: none }`, added to
  `uaDescendantRules` — expressible directly with this engine's existing
  attribute-selector and `:not()` support, no new selector feature needed),
  matching a real browser's own native rendering rule for the element,
  applied at UA precedence so any author rule targeting the hidden content
  still overrides it.

**Verified live:** the nav drawer and every tooltip are gone from the
rendered page; `bench/cmd/compare`: pixdiff 47.1%→45.4%, SSIM roughly held
(0.539→0.534, within this page's already-documented live-content-drift
noise band). The improvement is real but modest in the AGGREGATE score
because a separate, larger, NOT-yet-chased issue dominates this page's
remaining diff: its genuine "Index" section (an always-visible, real content
list of every exported symbol) appears to render at a different vertical
position than Chrome's, most likely from a deeper multi-column grid/flex
layout difference in the page shell — flagged for a future session rather
than guessed at further this round.

## 2026-09-02 — tailwindcss.com's whole layout ROOT-CAUSED and fixed: THREE independent, foundational CSS gaps (calc(), `inherit`, `inset`)

tailwindcss.com was the worst score in the bench corpus after the MDN fix
below (SSIM 0.364, pixdiff 55.2%). Chased it visually rather than guessing
from the number: a decorative avatar image rendered many times too large, a
whole row of sponsor logos rendered in plain browser-default link blue
instead of their real colours, and an unrelated feature-card photo rendered
at the very top of the page, overlapping the hero title, instead of inside
its own card far below. Three separate, unrelated root causes, each
confirmed live by dumping the actual box tree and computed styles (not
guessed from the screenshot):

- **`calc()` was entirely unimplemented — every `calc(...)` value failed to
  parse and its declaration was dropped.** Tailwind v4 compiles its ENTIRE
  numeric spacing scale this way (`--spacing: .25rem` once at `:root`, then
  every sized/spaced utility — `size-48`, `h-48`, `p-4`, `gap-4`, `mt-4`, …
  — as `calc(var(--spacing) * 48)`), so this single gap broke width, height,
  padding, margin, and gap on nearly every utility-classed element on the
  page. An `<img class="size-48">` (meant to be a 192×192px avatar) fell back
  to its raw intrinsic pixel size instead. Implemented (`css/calc.go`): a
  small recursive-descent evaluator for `+`/`-`/`*`/`/` over lengths and bare
  numbers, with nested parens — run after var() substitution, so
  `calc(var(--spacing) * 48)` is plain arithmetic by the time it evaluates. A
  calc() mixing a percentage (or `vw`/`vh`, approximated as a percentage
  elsewhere in this package) with an absolute length is deliberately left
  unresolved — percentages resolve only against a containing block at LAYOUT
  time, which this text-level pass has no access to — dropping the
  declaration exactly as before calc() was understood at all, not a
  regression for that subset.
- **The CSS-wide `inherit` keyword was not understood at all.** Tailwind's
  (and virtually every modern framework's) preflight reset ships
  `a{color:inherit}` to cancel the browser's default link-blue — but `color`
  being inherited BY DEFAULT was not enough to fix this: this engine's own
  UA stylesheet declares `a{color:#0000ee}` at lower cascade precedence, so
  by the time the author's `inherit` declaration runs, the field has already
  been overwritten and needs to be explicitly copied from the parent again.
  Every `fill="currentColor"` SVG icon inside such a reset anchor (a common
  pattern for a linked logo — confirmed live: every sponsor-logo `<svg>` on
  tailwindcss.com sits inside exactly such an anchor) inherited that wrong
  blue too. Fixed (`inheritProperty` in `css/parse.go`, threaded a `parent
  *Style` into `Style.apply`): covers the properties this engine already
  gives explicit inheritance semantics to in `inheritFrom` — `color`,
  `visibility`, `font-weight`, `text-align`, `white-space`, `line-height`,
  `list-style-type`, `list-style-position`. `inherit` on any other property
  is a no-op (unchanged from before), not a guess.
- **The `inset` shorthand (and its logical-axis siblings `inset-inline`/
  `inset-block`) was entirely unimplemented** — only the `top`/`right`/
  `bottom`/`left` longhands were. `inset-0` (`inset:0`) is Tailwind's, and
  most modern CSS's, standard way to pin an absolutely-positioned element to
  fill its containing block; without it, `top`/`right`/`bottom`/`left` all
  stayed at their initial `auto`, so the box fell back to CSS's "static
  position" rule (roughly, "where it would have landed in normal flow") —
  landing at the very top of the whole document instead of inside its
  intended card. Confirmed via `resolveContainingBlock`/`placeAbsolute`
  tracing that containing-block RESOLUTION was already correct (the nearest
  `position:relative` ancestor was found and used correctly every time) — the
  bug was purely that `inset` never populated the offset fields it resolves
  against. Fixed: the 1-to-4-value box-edge shorthand for `inset` (same
  expansion order as margin/padding, but preserving `auto`/percentage rather
  than collapsing them to 0 the way margin/padding's shorthand does — position
  resolution depends on telling them apart), plus the 1-or-2-value
  `inset-inline`/`inset-block` logical shorthands (mapped directly to
  left/right and top/bottom respectively — this engine has no bidi/vertical
  writing-mode support anywhere, matching every other physical-only
  assumption it already makes).

**Verified live:** the avatar renders at its correct size, every sponsor logo
renders in its real colour, and the previously-misplaced feature photo now
sits correctly inside its own card. `bench/cmd/compare`: SSIM 0.364→0.646,
pixdiff 55.2%→15.0%, page height 12958px vs Chrome's 13280px (up from
11858px). Confirmed via a full 10-page bench corpus re-run that nothing else
regressed — developer.mozilla.org even improved slightly further as a side
effect (0.596→0.613 SSIM), plausibly from the same `inherit`/`calc()` fixes
applying elsewhere on that page too.

## 2026-09-01 (cont. 2) — MDN's whole design-token system ROOT-CAUSED and fixed: a spec-valid empty custom-property value was being silently dropped at parse time

developer.mozilla.org was the worst score in the bench corpus after the
react.dev fix below (SSIM 0.128, pixdiff 95.9%): the page rendered as plain
unstyled HTML — no colours, no card borders, default browser typography —
even though all 17 of its external stylesheets fetched successfully and their
selectors (confirmed directly: `.page-layout{display:grid}` really did match
`<body class="page-layout">`) were being applied.

Root-caused by reading MDN's real, live CSS rather than guessing from the
screenshot. MDN's build (`postcss-preset-env`'s `light-dark()` polyfill) ships
nearly its entire colour system as a "CSS toggle": a guard custom property is
set unconditionally to `initial`, then overridden to **empty** inside
`@media (prefers-color-scheme:dark)`; an intermediate property threads the
guard through `var(--guard) var(--gray-40)`, and the real colour property
reads that intermediate with a fallback: `var(--toggle, var(--gray-60))`. This
"initial vs. empty" pair is real, common CSS — the same mechanism a plain
`color-scheme: light dark` + `light-dark()` pair compiles down to for browsers
needing the fallback path — not something exotic to MDN.

The declaration parser's `ParseDeclarations` dropped **any** declaration with
an empty value (`prop == "" || val == ""`) to skip meaningless input like
`color: ;`. That rule is correct for an ordinary property but wrong for a
**custom** property: `--guard: ;` is a real, spec-valid, load-bearing value,
different from `--guard` being unset. Dropping it left every guard variable
stuck at its unconditional (`initial`) branch forever, regardless of which
`@media`/attribute condition should have overridden it — collapsing MDN's
entire colour, border, and spacing token system at once. Fixed: an empty
value is now kept for custom properties, dropped only for ordinary ones
(engine#85).

Two smaller, genuinely real but NOT independently load-bearing for this page
were found and fixed in the same investigation:
- **`var(--a, fallback)` failed outright when `--a` existed but was itself
  invalid** (recursively referenced another unresolvable var()), instead of
  trying `fallback` — the CSS Custom Properties spec treats a
  guaranteed-invalid referenced property as equivalent to unset for this
  purpose. Real and independently useful (a different arrangement of the same
  toggle pattern depends on it), verified via `git stash`-isolated bisection
  to NOT be what fixed MDN itself, since MDN's guard resolves to *empty*
  (valid-but-blank), not invalid, once the parser fix lands.
- **The CSS Color Module 5 `light-dark(light, dark)` function was entirely
  unimplemented.** Added (always resolves to the dark branch, matching the
  same "assume dark" convention `js/window.go`'s `matchMedia` already uses for
  the bench suite's real dark-appearance reference Chromium). Also not
  load-bearing for MDN specifically: its native `light-dark()` usage sits
  behind `@supports (color: light-dark(...))`, which this engine still skips
  wholesale like every other unrecognised at-rule — a separate, larger,
  not-yet-attempted gap (see "Known gaps").

**Verified live:** developer.mozilla.org now renders MDN's real dark theme —
dark navy background, purple headings, correctly-coloured nav/breadcrumb bars
— matching the Chrome reference's overall look (residual diff is a real,
separate gap: the two-column sidebar layout collapses to one column at this
viewport width, `bench/REPORT.md` has detail). `bench/cmd/compare`: SSIM
0.128→0.596, pixdiff 95.9%→17.6% — the worst page in the corpus is now
comfortably mid-pack, with zero regressions elsewhere in the 10-page corpus
(confirmed by a full before/after run, not a single-page spot check).

## 2026-09-01 (cont.) — react.dev's white background ROOT-CAUSED and fixed: the empty-render guard was throwing away good styles along with bad geometry

react.dev was the worst score in the bench corpus (93.1% pixdiff, SSIM 0.256):
it rendered with a white/light background instead of its real dark navy theme,
even though the final DOM plainly showed `<html class="client-js dark
platform-win">` — the site's own dark-mode toggle had visibly worked.

Root cause was in the `settle()` loop's empty-render guard (added by the
engine#46 fix for react.dev's *other* bug, a client-side router failing to
mount and unmounting the whole app tree while leaving the DOM "changed"). That
guard is still correct to keep the PRE-SCRIPT geometry when a later pass
empties the page — but on this page, the SAME script batch that emptied the
tree also ran the harmless, unrelated `matchMedia('(prefers-color-scheme:
dark)')` + `classList.add('dark')` theme toggle. The guard rejected the whole
pass to protect the geometry, and in doing so also discarded that pass's
entirely correct, entirely harmless dark-theme cascade — so the page stayed on
its pre-JS light background forever, independent of what the DOM said.

Confirmed via a from-scratch `css.CascadeVW` against the final (post-wipe) DOM
that the cascade/selector logic itself has no bug: it correctly computes the
dark background. The guard's granularity — "accept the whole pass or reject
the whole pass" — was the actual defect.

Fixed (engine#84) by adding `reskin()`: when the guard fires, it now walks the
PRESERVED (good) box tree and swaps each box's, marker's, and inline text
run's `Style` to the rejected pass's freshly computed value, touching no
geometry (`X/Y/W/H/Children/Lines` untouched). Safe because DOM node identity
survives an in-place mutation like `classList.add` — only the style lookup
needs to catch up, not the tree shape. Inline text color needed its own
handling since it lives on `InlineItem.Style`, not the enclosing box's.

**Verified live:** react.dev's dark navy background and code-block panels now
render correctly end to end; `bench/cmd/compare` shows pixdiff 93.1% → 49.5%,
SSIM 0.256 → 0.587 (`bench/REPORT.md`). Remaining diff on this page is
unrelated pre-existing gaps (video thumbnail `<img>`s render blank, some
code-block syntax-highlight colour contrast) — not this bug.

## 2026-09-01 — github.com's header nav CLOSED: two independent real fixes, neither was Shadow DOM

Followed up on the correction below (github.com's permanently-expanded header
was reattributed away from Shadow DOM but left undiagnosed). Root-caused it
properly by fetching the real page's actual `<link>` tags and CSS instead of
guessing:

- **`visibility` was completely unimplemented** — no parsing, no `Style`
  field, no paint-time effect, and `getComputedStyle('visibility')` hardcoded
  `"visible"` regardless of the real CSS (engine#80). Genuine, independent
  gap: github.com's dropdown menus and full-viewport backdrop both pair
  `visibility:hidden` with `opacity:0`, so opacity alone happened to already
  cover them here, but a real site relying on `visibility` alone would have
  rendered incorrectly. Implemented properly (inherited, but overridable per
  descendant, unlike `display:none`/`opacity:0`).
- **The actual root cause: `maxExternalSheets` was 20; github.com/golang/go
  ships 38 `<link rel=stylesheet>` tags** (per-component CSS modules plus
  several mutually-exclusive colour-scheme variants), and the ONE sheet
  holding the header's hide/sr-only rules (a `visuallyHidden` utility class,
  the mega-menu's visibility/position:fixed toggle) happened to be 38th —
  dropped past the old cap. Without it, the header's raw markup — every
  dropdown menu's full text, concatenated — rendered fully unstyled and
  visible at the top of the page. Raised to 64 (engine#81).

**Verified live:** github.com/golang/go's header now renders as a compact
"Platform / Solutions / Resources / Open Source / Enterprise / Pricing" bar,
matching a real browser, instead of a multi-line wall of every menu's
contents dumped at the top of the page.

## 2026-08-31 (cont. 5) — Shadow DOM: declarative shadow roots, `<slot>` projection, `:host` scoping

Ships the largest single gap this engine had (comparable in scope to the
original JS-engine work): declarative Shadow DOM attachment, `<slot>`
distribution, and `:host`/`:host()` CSS scoping — engine#73, #75, #77, #78.

- **`dom.Node.Shadow`/`ShadowHost`** (engine#73): a `<template
  shadowrootmode="open"|"closed">` that is the first element child of its
  host is hoisted out of the light DOM at parse time (matching real browser
  parser timing — the `<template>` never becomes part of the live tree, only
  its `.content` does, as the new shadow root's children) into
  `Node.Shadow`, distinct from the host's ordinary `Children`. "open" and
  "closed" render identically (the distinction is JS-introspection-only, out
  of scope). No imperative `attachShadow()`/`customElements.define()` path —
  see below for why that's the right call for now.
- **Slot projection** (engine#75): `layout.renderedChildren(n)` is the one
  substitution point every block/inline tree walk now goes through instead
  of `n.Children` directly — a host with a shadow root renders that tree's
  content instead of its own light children (unslotted light content
  correctly renders NOWHERE, per spec, not even a fallback position); a
  `<slot>` renders its assigned light-DOM nodes (recomputed fresh every
  layout pass, so a settle-loop re-layout after a script mutation picks up
  automatically) or, absent any, its own fallback content. No separate "flat
  tree" structure was needed — a slotted node's box is simply built as part
  of laying out the shadow tree it now sits inside, so box-index
  (`getBoundingClientRect`) and paint needed zero changes.
- **`:host`/`:host()` + shadow-scoped cascade** (engine#77): a new
  `Selector.MatchesHost(n, host)` threads a host binding through the whole
  combinator chain, so `:host(:not([open])) slot {display:none}` — the exact
  idiom real custom elements use to hide slotted content until an
  interactive state toggles — actually works; `Matches(n)` is exactly
  `MatchesHost(n, nil)`, so every pre-existing call site is provably
  unaffected. Cascade now recurses into an attached shadow root with a NEW
  scope (that shadow's own `<style>` rules, host bound to it), so a shadow
  tree's stylesheet applies only within that tree — never leaking in from,
  or out to, the surrounding document — except via the sanctioned
  `:host`/`:host()` escape onto the host itself. No caching: the settle
  loop's ordinary re-cascade after a script toggles a host attribute (e.g.
  `open`) picks up the visibility change with zero additional wiring,
  verified directly (two sequential `Cascade()` calls over the same mutated
  DOM, and an end-to-end settle-loop test where a `<script>` calls
  `setAttribute('open', '')` on the host).

**Live verification, done properly** (not "it compiles" — measured against
real markup, per this repo's culture):

- **developer.mozilla.org**: confirmed via a raw fetch of the exact page
  (`curl`, no JS) that it uses declarative shadow DOM for real — 23
  `<template shadowrootmode="open">` instances, one of them an
  `<mdn-dropdown>` component whose own shadow `<style>` is *exactly*
  `:host(:not([loaded],:focus-within)) slot[name=dropdown]{display:none}`,
  i.e. the idiom this PR set out to support. Rendered the identical frozen
  HTML snapshot through the engine before and after this work (same file,
  only the code differs — controlled, not live-site noise): the page's
  "Help improve MDN" feedback module goes from showing only "Learn how to
  contribute" (before) to correctly also showing "Was this page helpful to
  you?  Yes   No" (after) — light-DOM content that requires slot
  distribution to reach its rendered position. Reproduced twice.
- **github.com**: this is where the original diagnosis (recorded in the
  previous "Known gaps" bullet below, now corrected) turned out to be
  **wrong**. A raw fetch of `github.com/golang/go`'s markup shows **zero**
  occurrences of `shadowrootmode` or `attachShadow` anywhere in the page —
  its header is plain React (`MarketingHeader-module__*` CSS-module classes)
  with the mega-menu dropdown gated by an ordinary class-conditional
  selector pair,
  `.NavDropdown-module__dropdown{visibility:hidden;position:fixed}` /
  `.NavDropdown-module__container.open .NavDropdown-module__dropdown{visibility:visible}`.
  There is no Shadow DOM anywhere in the path, so this PR set correctly does
  not change github.com's render at all (confirmed: byte-identical header
  region before/after) — and, more importantly, **could not have fixed it
  regardless of how it was written**. github.com's permanently-expanded
  header nav remains open as a real, distinct bug (see "Known gaps"), now
  correctly attributed to something else — most likely this engine's
  handling of `visibility` interacting with `position:fixed` and/or
  multiple same-specificity conflicting declarations for one class, not
  investigated further here since it is out of scope for Shadow DOM.
- Re-rendered the first 6 pages of `bench/urls.txt` (Chrome was not
  available in this environment for the full `cmd/compare` pixel-diff tool)
  to sanity-check no regression: all six still render without error, at
  materially unchanged content heights.

**Scope / known gaps, precisely**: declarative shadow DOM only, not the
imperative `attachShadow()`/`customElements.define()` JS API (both confirmed
real sites use the declarative form exclusively, so this covers the actual
need); `::slotted()` and `:host-context()` are parsed as unmodelled and
safely dropped (a compound that is ONLY `:host-context(...)` drops its whole
selector rather than ever matching too broadly — verified by test); no
`display:contents` (so a shadow host that declares `:host{display:contents}`,
as `<mdn-dropdown>` does, still generates its own wrapping box instead of
being transparent to the layout — a real, separate, undramatic gap, not
blocking the visibility mechanics above).

New tests: `dom/shadow_test.go`, `layout/shadow_test.go`,
`css/hostselector_test.go`, `css/shadowdom_test.go`,
`shadowdom_test.go` (engine-level end-to-end, including the settle-loop
attribute-toggle case). `dom` coverage 97.7% → 98.1%; `layout` stays 100%;
`css` stays 99.5% (both gate floors maintained or improved). Full suite
green throughout.

## 2026-08-31 (cont. 4) — CSS Container Queries (`@container`) implemented

Closed the "No CSS `@container` queries" gap flagged in the entry below (and
in "Known gaps"): the at-rule was previously unrecognised and fell into
`parseRules`' wholesale-skip bucket, the same class of bug `@layer` had before
engine#56 — any rule gated behind `@container` was silently dropped in full,
regardless of its condition.

Landed as two PRs. The first (engine#74) is parsing + cascade-time evaluation
only: `container-type` (`normal`/`inline-size`/`size`), `container-name`, and
the `container` shorthand parse into new `Style` fields; `@container`'s body
is always included (like `@layer`) but, unlike `@media` — resolved once
against the viewport at parse time — each inner rule carries a
`ContainerCondition` evaluated per matched element during cascade, because a
container's size is per-ancestor-element and only known once layout has run.
The size-feature syntax (width/min-width/max-width, the Level 4 range-
comparison operators, `calc()`) is reused near-verbatim from this session's
`@media` work (engine#58/#61), generalised to height features for a
`container-type: size` container (both axes; querying an axis without
containment always fails, matching spec). Named lookup walks past a nearer
ancestor container with the wrong name to find a qualifying one further out;
an unnamed query uses the nearest container of any name.

The second PR wires this into the render pipeline: a `@container` condition
cannot be resolved by a single cascade+layout pass the way `@media` can,
because the size it depends on doesn't exist until AFTER layout. This mirrors
the chicken-and-egg problem `dynamic.go`'s JS settle loop already solves for
script-driven geometry feedback — same shape, same discipline: a new
`layoutWithContainers` loop (measure every query container's real laid-out
size → re-cascade with that information → re-layout → repeat) runs after the
first real (post-image-load) layout, bounded by `maxContainerPasses` (4) so it
can never hang, converging to a fixpoint (or accepting the last pass if the
cap is hit) the same way `maxSettlePasses` does. It also re-runs inside the JS
settle loop itself (a script's class/DOM mutation can change which elements
are containers or their sizes) and after the settle loop's final image
reload. A crafted fixture in the new top-level `container_test.go`
(`TestLayoutWithContainersMultiPassConvergence`) proves this genuinely needs
more than one pass: an ancestor container's condition widens a nested
container's own measured size, which only then satisfies a SECOND condition
keyed on that nested container — deterministic, no live network or JS
involved, same pattern as `TestSettleFixpointCap` for the JS case.

**Verified live on tailwindcss.com** (1024px viewport, `DisableJS` off):
`contentHeight` went from 14113px to 11858px (−16%). Visually confirmed this
is real, not incidental — Tailwind's own homepage includes a live "container
queries" demo (a property listing card grid): before this fix it rendered
permanently in its narrow/single-column fallback state (images stacked full-
width, one per row); after, it correctly reflows into the multi-column card
layout its container's real width qualifies it for. Screenshots of both
states are in this PR's description.

Checked the other two `@container`-relevant pages `bench/urls.txt` names:
MDN's CSS index page rendered **pixel-identical** before/after (no `@container`-
gated content visible at this viewport/width, so correctly a no-op — verified
by diffing the two PNGs, not just eyeballing). github.com/golang/go's
`contentHeight` was unchanged; a pixel diff found one small, unrelated, and
strictly *positive* difference — a pre-existing ghost/overlap text-rendering
artifact on the repo's file-tab labels (README/Code of conduct/Contributing/…)
is gone after this change, most likely because a tab component's markup is
itself conditioned on a `@container` rule that previously left a stale
duplicate in place. Did not re-run the Chrome-based SSIM comparison
(`bench/cmd/compare`) this session — the `bench/` module's `go.sum` needs a
`go mod tidy` unrelated to this change (a pre-existing drift, not caused or
fixed here) and `go run` refuses to proceed without it; the tailwindcss.com
height delta and the direct pixel diffs above stand in for it.

**Known, honestly-scoped gaps** (also reflected below in "Known gaps"):
`@container style(...)` ("container style queries", a separate/newer part of
the spec) is not supported and its body is dropped wholesale, like any other
unrecognised at-rule — this was a deliberate scope cut, not an oversight.
Container query length units (`cqw`/`cqh`/`cqi`/`cqb`/…) are a different,
related CSS feature (using a container's size as a *unit basis* rather than a
*condition*) and are not implemented. `container-name` only ever compares as
a single token (no space-separated multiple names per element). The measured
container size is the element's border-box (matching how this engine already
reports geometry elsewhere, e.g. `getBoundingClientRect`); a browser measures
the content box, so a container with a non-trivial border/padding could
resolve a boundary condition slightly differently than Chrome.

## 2026-08-31 (cont. 3) — a stale THIRD-PARTY dependency caught a real number-formatting bug

Same audit as the entry below, extended past this org's own sibling
repos to the JS engine itself (`dop251/goja`, third-party, no local
checkout to read — checked its GitHub commit log for the gap since this
engine's pinned version instead). Found `ftoa`'s shortest-mode digit
rounding incrementing the current digit BEFORE checking whether it had
become `'9'` (the carry case) instead of after, corrupting the decimal
tail for specific doubles. **Confirmed live, not just from the diff:**
`(0.7016570306969449).toString()` rendered as `"0.701657030696945"`
(missing a digit) before the bump, correct after. engine#71.

Also attempted bumping `dop251/goja_nodejs` (pinned since 2021, ~4.5
years stale) alongside it — `go mod tidy` correctly dropped it entirely
instead: nothing in this engine actually imports it, so it was dead
weight in `go.sum`, not a real dependency worth the risk of such a large
jump.

## 2026-08-31 (cont. 2) — two stale sibling-org dependencies caught unclaimed perf fixes

Bumping `go-widgets/painter` from its very first tagged release (v0.2.0) to
v0.12.0 (engine#68) picked up ten releases of accumulated paint-primitive
work at once, including `FillRect fills rows, not pixels` (v0.8.0): a
per-pixel loop replaced by a row built once and doubled by `copy()`.
Similarly, `go-gfx/gfx` v0.5.0 → v0.19.0 (engine#69) picked up
`raster.FromImage` reading `*image.YCbCr` (what every JPEG decodes to) from
its own bytes instead of the generic interface-based colour-model path —
upstream measured 69ms and 3,840,002 heap allocations for a single
1200×1600 page before that fix. Neither bump changed the engine's own
code; both were caught only because pprof showed the OLD, unoptimised
implementation still running in a profile taken today. **Lesson: a
pinned sibling-org dependency can silently miss real fixes for a long
time — `go list -m -u all` after any profiling session, not just when
something looks broken.**

One expected, verified-as-a-fix visual change came with the painter bump:
`StrokeRect`/`StrokeRoundRect` previously always painted a 1px border
regardless of the requested width (v0.11.0 fixed this) — a `border: 2px
solid` now genuinely renders 2px. Regenerated the affected golden and
`testdata/renders/*.png` files rather than leaving them stale.

pkg.go.dev/net/http (the largest page in the corpus, 88659px tall, so paint
cost dominates): ~3.4s → ~2.8s across both bumps together.

## 2026-08-31 (cont.) — the grid bug from the entry below fixed, plus a sandbox-escape and two dedupe wins

Two real CSS Grid bugs, found by dumping the live box tree (a temporary
white-box test, not committed) rather than guessing from a screenshot:

- **The "Maximize Tracks" step of CSS Grid's track-sizing algorithm only grew
  `fr` tracks.** A non-flexible track with room below its max cap — e.g.
  `minmax(0,1536px)`, exactly tailwindcss.com's own page-shell content column
  (`md:grid-cols-[var(--gutter-width)_minmax(0,var(--breakpoint-2xl))_var(--gutter-width)]`)
  — never grew past its 0px base. Fixed: `growCappedTracks` in `layout/grid.go`
  now splits free space among such tracks first, then hands whatever's left to
  the `fr` step, engine#63.
- **`grid-row: 1 / -1` (Tailwind's `row-span-full`) resolved to a zero-row
  span**, because the row axis always resolved a negative end line against an
  unknown track count, even though the explicit `grid-template-rows` count is
  known before placement. The item (a decorative full-height background)
  reserved no occupancy, so the page's actual content wrapper slid into the
  gutter column instead. Fixed by threading the known row count through,
  engine#63. **Together: tailwindcss.com's content column went from 40px wide
  at ~22450px document height to 944px wide at ~12000px** — verified with the
  same box-tree dump before/after.

Separately, profiling the two slowest remaining pages (the performance half of
this session's work) surfaced a genuine sandbox-escape, not just a slowdown:

- **esbuild's module bundler used `ResolveDir: "/"` — the real filesystem
  root — everywhere.** The `webengine-http` plugin fully intercepts ordinary
  import resolution, but esbuild's bundler special-cases a *glob* dynamic
  import (`import(\`./${lang}/index.js\`)`, ordinary bundler-emitted i18n/
  route code-splitting) by resolving it directly against `ResolveDir` on the
  real filesystem, entirely bypassing the plugin. A page merely containing
  such an import made the engine recursively walk the host's entire real root
  filesystem hunting for "matches" — independent of the cost, a remote page
  should never cause this engine to read the host's real directory tree.
  Fixed with an isolated, always-empty `os.MkdirTemp`'d directory, engine#65.
  **developer.mozilla.org: ~14.7s → ~0.56s (26x).**
- **`dom.Node.Classes()` had no caching and `css.compound.matches` allocated a
  fresh map on every single selector check** — the hottest path in the
  cascade, checked once per candidate rule per element. Fixed by memoizing
  the class split and using a linear scan instead of a map (both lists are
  tiny). engine#64. **tailwindcss.com: ~5.7s → ~2.2s.**
- **A page's images were fetched twice within one render** — the settle loop
  reloads images unconditionally after any script-driven relayout, with no
  cache even though `e.ImageCache` (opt-in, nil by default) only ever governed
  cross-render reuse. Fixed with an ephemeral per-render cache threaded via
  `context.Context`. engine#66. **caniuse.com: 19 requests → 16, ~4.16s →
  ~3.64s** (its remaining ~3.6s is server-side connection pacing on caniuse.com's
  own origin, confirmed by reproducing the identical clustering with a bare
  concurrent fetch outside the engine entirely — not a client bug).

## 2026-08-30 — chaos audit: two live regressions, a live hang, two conformance gaps

This file and `bench/REPORT.md` had not been updated since Phase 2.6 (2026-08-05)
despite ~40 PRs landing in the meantime — the numbers and gap list below had gone
stale relative to the code. Re-measuring against real, live pages (not the
committed renders) surfaced five concrete, independently verified defects rather
than a vague "it's probably fine":

- **react.dev rendered fully blank** (`contentHeight=0`) — a client-side router
  failing to load its own error route unmounted the whole app tree mid-settle,
  and the settle loop accepted the empty result over the good pre-script layout.
  The bench tool's `maxheight`-capped SSIM scored the blank page a **0.729→0.903
  "win"** by comparing only its dark background against Chrome's dark hero fold
  — a regression that a naive metric read as an improvement. Fixed: engine#46.
- **pkg.go.dev/net/http took 27.8s — 5x slower than headless Chrome.** 11 small
  (16–23px) `filter`/`opacity` elements each allocated a group buffer the size
  of the whole 1024×88631 page canvas and ran the filter math over all 90M
  pixels. Fixed by bounding the buffer to each box's own ink bounds: **27.85s →
  5.38s (5.2x)**, byte-identical output verified. Fixed: engine#47.
- **developer.mozilla.org never completed rendering** (120s+, still confirmed
  stuck inside `esbuild.api.Build` by a `SIGQUIT` goroutine dump). `api.Build`
  is synchronous and uncancellable; the existing budget bounded `OnLoad` but not
  `OnResolve`, so a large import graph kept being enumerated long past the
  bundle stage's own deadline. Fixed by running the call in its own goroutine
  and abandoning the *wait* (not the call) once the budget expires: **never
  completing → 15.8s**. Fixed: engine#48.
- **`!important` was parsed and discarded.** A real site's forced state
  (`.left-sidebar{display:none!important}`, MDN's default-collapsed drawer)
  could be — and was — overridden by an unrelated, later, higher-specificity
  rule meant for a different responsive state. Now modelled as its own cascade
  tier above every non-important declaration, regardless of origin/specificity.
  Fixed: engine#50.
- **`<template>` content rendered as normal markup.** A `<template>`'s children
  are inert per the HTML spec (they live in `.content`, never the document
  tree); this engine parsed them straight into the light DOM. Concretely, MDN's
  23 `<template shadowrootmode="open">` declarative-shadow-DOM blocks (its
  header/menu web components) painted their shadow markup inline, unstyled and
  unscoped. Fixed: engine#54 (adds `template` to the tag-keyed `display:none`
  UA-default list). This does **not** implement Shadow DOM — see "Known gaps".

**Diagnosed but not fixed — the next real lever:** MDN still overlaps after all
five fixes above, because its mega-menu dropdowns (Tools/References/Learn) use
genuine Shadow DOM `<slot>` projection: light-DOM children (`<div slot="dropdown">`)
are meant to be distributed into a shadow tree and hidden by a `:host(...)
slot{display:none}` rule scoped to that shadow root. This engine has no custom-
element upgrade, no shadow-root attachment, and no slot distribution at all, so
those panels render permanently expanded rather than hidden-until-interaction.
Implementing this is a materially bigger feature (comparable in scope to Phase 2's
JS engine) than any of the five fixes above — deliberately not attempted here;
tracked as the next major gap below rather than papered over.

Refreshed corpus and numbers: [`bench/REPORT.md`](bench/REPORT.md),
`bench/urls.txt` (widened from 5 to 10 URLs the same day — see its own log).

## 2026-08-30 (cont.) — three more conformance bugs, found chasing tailwindcss.com's 0.051

The widened corpus's worst score (tailwindcss.com, 0.051 SSIM, rendering at
~2x Chrome's height) was flagged above as "not yet root-caused." Following it
through the actual cascade — not guessing from the screenshot — turned up
three independent, unrelated CSS-parsing bugs, each verified in isolation
before being confirmed live:

- **`@layer` content was dropped wholesale.** The parser recursed into
  `@media` but treated every other at-rule, `@layer` included, as opaque.
  Tailwind v4's default output puts nearly all of its CSS inside
  `@layer utilities` — on tailwindcss.com, one such block held 94% of the
  690KB stylesheet. Only 181 of 5334 real rules were ever reaching the
  cascade. Fixed: engine#56 (`@layer` bodies are now unwrapped and included,
  the same way `@media` bodies are — cross-layer cascade *priority* is not
  modelled, which is a materially smaller gap than dropping the content).
- **A common `:is()`/`:where()` idiom expanded to the wrong selector.**
  `PREFIX:where(X, X *)` — "the tested element is X itself, OR has an
  ancestor X", Tailwind's class-based dark-mode variant strategy
  (`.dark\:hidden:where(.dark,.dark *)`) — was spliced by naive text
  concatenation into `PREFIX X *` ("some element with an ancestor matching
  BOTH X and PREFIX at once"), an almost-never-matching selector. A
  light/dark caption pair rendered both halves simultaneously regardless of
  theme. Fixed: engine#57 (`stripTrailingSelfCombinator` re-roots the chain
  onto the real prefix instead of concatenating text).
- **`min-width`/`max-width` media features only recognised `px`.** Tailwind
  v4's default breakpoints are all in `rem` (`min-width:80rem` for `xl`).
  An unrecognised unit wasn't just left unconverted, it made the whole
  feature invisible to the matcher, which then defaulted to "assume it
  matches" — so every responsive breakpoint was permanently active at once,
  and the cascade fell back to picking whichever was declared last (usually
  the largest). The hero headline's `xl:text-8xl` applied at a 1024px render
  width, nowhere near its real 1280px breakpoint. Fixed: engine#58 (`rem`
  parsed and converted at the same 16px root font-size `parseLength` already
  assumes elsewhere).

**Measured result: tailwindcss.com 0.051 → 0.699 SSIM (13.7x), pixdiff 97.2%
→ 11.2%, height 26453px → 18154px (Chrome: 13280px — still ~37% taller,
residual not yet root-caused; likely `@container` queries, which Tailwind v4
also uses and this engine does not evaluate at all, or a flex/grid track-
sizing gap).** None of the three fixes are Tailwind-specific — `@layer` and
`:where()`'s self-or-descendant idiom are used by many frameworks, and any
site with rem-based breakpoints was equally affected.

**github.com/golang/go's mega-menu overlap is confirmed the same Shadow-DOM
gap as MDN**, not a hypothesis anymore: its header ("Platform", "AI",
"Enterprise", …) renders as one long permanently-expanded block for the
identical reason (light-DOM panels meant to be slotted into a shadow tree
and hidden until interaction). SSIM held flat (0.114→0.058) rather than
improving, for an unrelated, NEW, undiagnosed reason found while checking
this: **the actual repository content — file listing, README — never
renders at all.** After the header and the Code/Issues/PRs tab bar, the page
jumps straight to the footer; contentHeight is just 535px. Not yet
investigated (a heavy-SPA module-bundle gate cutting off too aggressively is
the first hypothesis, unconfirmed) — flagging honestly rather than folding
it into the Shadow-DOM gap it is very unlikely to share a cause with.

## 2026-08-31 — github.com's missing content ROOT-CAUSED and fixed; a genuine grid bug found

Chased the github.com missing-repository-content finding from the entry above
to ground instead of leaving it as an open hypothesis. Root cause, confirmed by
tracing the real cascade against github.com's actual fetched CSS: a SECOND bug
compounding the Shadow-DOM one, in a completely different part of the engine.

- **Attribute selectors (`[attr]`, `[attr=value]`, …) were unconditionally
  dropped** — "the constraint is not modelled, the compound reduces to its
  tag/class/id prefix", the same simplification applied to genuinely unmodelled
  pseudos, but wrong here because an attribute selector degrades a
  *conditional* rule into an *unconditional* one. Concretely:
  `.ContentWrapper:where([data-is-hidden-narrow=true])
  {display:none}` degraded to plain `.ContentWrapper{display:none}` once the
  attribute selector inside `:where()` was dropped — hiding the entire
  repository content unconditionally, regardless of the real
  (`data-is-hidden-narrow="false"`) value on the actual element. Fixed:
  engine#60 (presence, `=`, `^=`, `$=`, `*=`, `~=`, `|=` all modelled; also
  makes `:not([attr])` a real negation for the first time, rather than a
  no-op that happened to give the right answer only when the attribute was
  absent).
- **`min-width`/`max-width` media features only recognised the colon syntax.**
  GitHub's Primer design system expresses its PageLayout breakpoints with the
  CSS Media Queries Level 4 range-comparison syntax instead
  (`width<=48rem`/`width>=48rem`, plus a `calc(48rem - .02px)` value creating a
  hair's-width gap between adjacent breakpoints) — invisible to the matcher for
  the same reason the missing `rem` unit was (falls through to "unknown
  feature, assume it matches"). Fixed: engine#61. Verified independent of the
  attribute-selector fix (identical output built with and without it on both
  github.com and tailwindcss.com) — it closes a real gap for range-syntax sites
  that neither page in the corpus happens to need for its *current* rendering.

**Measured: github.com/golang/go's contentHeight went from 535px (header + tab
bar, then straight to the footer) to 3451px** (the full file listing, commit
count, and README now render). The header mega-menu overlap (the Shadow-DOM
gap) is unaffected by either fix and still present, as expected — this closes
the *other*, unrelated defect on the same page.

**A third, genuine finding, NOT caused by either fix above (bisected against
the commit before attribute selectors landed — identical result either way):**
tailwindcss.com's residual too-tall render is dominated by a CSS Grid bug, not
`@container` queries as earlier suspected. A `grid-cols-1` container
(`grid-template-columns: repeat(1, minmax(0, 1fr))`) laid out at 40px wide
instead of its parent's full 1024px — `css/grid.go`'s track-list parser handles
this track correctly (verified directly), so the bug is in the layout-time
fr-distribution, not parsing. Not fixed this session — it needs work in a
different subsystem (grid track sizing) than the selector/media-matching bugs
above, and is flagged precisely rather than re-guessed at.

## Phase 2.6 — line-height inheritance + mixed-size line boxes (text-overlap fix)

**Date: 2026-08-07.** A downstream consumer (the go-news-reader preview) reported
**overlapping text** on real pages. Reproduced at preview width (400px) with a page
mixing headings, wrapped body copy and inline spans of different `font-size` under a
unitless `line-height` (`scripts/compare/pages/overlap.html`). Two independent root
causes, both fixed in the pure-logic layer (no paint hack):

- **CSS — unitless `line-height` was collapsed to pixels too early.** `line-height`
  stored only a resolved `Px`, so a unitless value like `1.5` on `<body>` was
  computed to `1.5 × 16 = 24px` at the body and that *pixel* value was **inherited**
  by every descendant. A 28–40px inline span therefore got a 24px line-height
  instead of `1.5 × its own size`, so its line box was far too short and the big
  glyphs spilled into the next line. Per CSS 2.1 §10.8.1 a unitless value computes
  to the *number* and inherits as the number; each element re-multiplies by its own
  font-size. `LineHeight` now carries a `Factor` and resolves via
  `LineHeight.Resolve(fontSize)`; length/percentage values still compute to a fixed
  `Px` that inherits unchanged.
- **Layout — a line box advanced by `max(item.LineHeight)`.** On a line mixing
  metrics, the tallest ascent and the deepest descent can come from *different*
  items, so `max(LineHeight)` under-sizes the line and lets a glyph spill. The line
  box now advances by `maxAscent + maxDescentBelow`, which bounds every item's
  glyph extent exactly (`[baseline−ascent, baseline+(lineHeight−ascent)]`). For a
  single-font line this is identical to the old value (no regression); for mixed
  lines it guarantees successive lines never overlap.

Regression tests assert the geometry directly, not just "it renders":
`lineMetrics` with independent ascent/descent maxima; end-to-end non-overlap over
every laid-out line (each line top ≥ previous bottom, every item within its line
box) for the mixed-size, multi-paragraph and heading cases; and unitless
`line-height` inheriting as a factor across differently-sized descendants.

**Measured vs headless Chromium (this machine), `overlap.html` at width 400:**

| page | MAD before | MAD after |
|------|-----------:|----------:|
| `overlap` (whole page) | 0.0542 | 0.0551 |
| `overlap` (mixed-size band, y∈[315,420]) | 0.1930 | 0.1887 |

The whole-page MAD is essentially flat because this coarse metric is dominated by
font-antialiasing differences vs Chromium and the fix is a *localised* vertical-
spacing correction — before the fix the big inline glyphs merely painted **on top
of** the neighbouring line (an overlap the averaged score barely registers). The
side-by-side confirms the crash is gone, and the definitive proof is the geometric
non-overlap unit tests. `layout` and `paint` stay at 100.0% statement coverage.

## Phase 2.5 — list-item markers (`<ul>` / `<ol>` bullets and numbers)

**Date: 2026-08-05.** The one visible gap the Phase-2.4 Chromium compare recorded
was that `<li>` items laid out and painted their content but with **no marker** —
no disc, no number. This phase closes it end-to-end across `css`/`layout`/`paint`.

- **UA defaults** — `<li>` is now `display:list-item`; `<ul>` defaults to
  `list-style-type:disc`, `<ol>` to `decimal`; both keep the 40px indent. Marker
  glyph inherits, so an item picks up its container's type. Nested `<ul>` alternate
  **disc → circle → square** by depth via UA descendant rules (`ul ul`, `ul ul ul`)
  matched at UA origin, exactly like a browser's UA sheet.
- **CSS** — `list-style-type` (`disc`/`circle`/`square`/`decimal`/`none`),
  `list-style-position` (`outside`/`inside`), and the `list-style` shorthand are
  parsed and cascaded (all three inherit). A `display:list-item` box carries a new
  `ListItem` flag; other display values clear it.
- **Layout** — a list-item box gets a `Marker` positioned (for the default
  `outside`) in the indent to the LEFT of the content box, vertically centred on
  the first line (falling back to the content-box top for an empty item). `<ol>`
  runs a per-list counter honouring `<ol start>` and `<li value>`; nested lists get
  a fresh counter (so a nested `<ol>` restarts at 1). Marker coordinates are unit-
  tested to exact values relative to the content box.
- **Paint** — a disc is a full-radius `FillRoundRect`, a hollow circle a
  `StrokeRoundRect`, a square a `FillRect`; a decimal marker reuses the inline text
  painter for its ordinal ("1.", "2.", …) in the item's own face/colour. `none`
  paints nothing (no marker is even attached).

**Deferred (documented):** `list-style-position:inside` is parsed and cascaded but
rendered with the same outside geometry — reserving inline space for an inside
marker is left for a later pass. `list-style-image` (bitmap markers) and non-
decimal ordered styles (`lower-roman`, `lower-alpha`, …) are out of scope; an
unknown `list-style-type` keeps the inherited/UA default.

**Proven deterministically (offline).** `TestListMarkersGolden` renders a `<ul>`
(discs) + `<ol>` (decimals) and asserts painted ink in the gutter left of the
content plus a byte-exact golden PNG (`testdata/golden/list_markers.png`);
`TestListStyleNonePaintsNoMarker` proves an identical layout with
`list-style-type:none` paints **zero** gutter pixels. Full unit coverage of the
new code in `css`/`layout`/`paint` (each at or above its ratchet floor: layout and
paint stay at **100%**, css at 99.6% ≥ 99.5%).

**Measured vs headless Chromium (this machine).** New compare page
`scripts/compare/pages/lists.html` exercises nested `<ul>` (disc→circle→square),
nested `<ol>` (counter restart) and `<ol start="10">`. At width 640
(`scripts/compare/chromium-compare.sh`):

| page | MAD before | MAD after |
|------|-----------:|----------:|
| `lists` (this phase) | *(marker gap: no bullets/numbers)* | **0.0065** |
| `js-dom-mutation` (baseline, re-measured) | 0.0131 | 0.0132 |
| `typography` (baseline, re-measured) | 0.0113 | 0.0113 |

The list page now scores **below** the static-text baseline: the markers match
Chromium down to font-antialiasing noise, and the disc/circle/square-by-depth,
decimal counters, nested restart and `start` offset all line up glyph-for-glyph in
the side-by-side. The marker gap flagged in Phase 2.4 is **closed**. One minor
residual remains, unrelated to markers: our nested lists carry slightly more
vertical spacing than Chromium (a nested-list block-margin/collapsing nuance), a
small delta well within the page's MAD.

## Phase 2.4 — JavaScript DOM mutation on the hit-map (`RenderWithLinks`) path

**Date: 2026-08-05.** Phase 2.2 wired the layout↔JS settle loop into
`RenderDocument`, but the anchor-hit-map entry points `RenderWithLinks` /
`RenderDocumentWithLinks` (used by the wasmdesk browserproxy to turn a click into
a navigation) still ran the **old JS-free** pipeline: cascade → layout → paint
with no script pass. A page whose links (or their positions, or the whole body)
are built by JavaScript therefore produced an **empty or stale hit-map** — a click
landed nowhere. This phase makes both paths share one pipeline.

- `RenderDocument` and `RenderDocumentWithLinks` now both call a single
  `renderCore` (`engine.go`): `MarkJSEnabled` → initial cascade/layout → the
  Phase-2.2 `settle` loop → final layout. The painted image and the returned
  `[]Link` are guaranteed to describe the **same JS-settled DOM and geometry**, and
  the with-links image is now byte-identical to `Render`'s (both use `PaintFull`,
  so background images paint on the hit-map path too).
- `document.title` set by a script is now reflected in `RenderInfo.Title` on both
  paths (`renderCore` re-derives the title after the settle loop).

**Proven deterministically (offline goldens, engine package):**

- `TestJSInjectedLinkInHitMap` — a script that does
  `createElement('a')` + `setAttribute('href', …)` + `textContent` +
  `body.appendChild` yields exactly one link (`https://example.com/injected`) with a
  non-empty painted rect through `RenderDocumentWithLinks`; with `DisableJS` the
  hit-map is empty. This is the regression the phase fixes, asserted directly.
- `TestJSTitleReflectedInRenderInfo` — a script `document.title = 'new title'` is
  reflected in `RenderInfo.Title` on both the plain and the with-links paths.
- The pre-existing `enginejs_test.go` invariants (JS-injected paragraphs grow the
  laid-out page; the `client-js` signal reaches the cascade; `DisableJS` leaves the
  no-JS fallback) all still hold. `css`/`layout`/`paint`/`dom` coverage floors
  unchanged and green; the new `renderCore`/`viewportSize`/`newCanvas`/`renderInfo`
  helpers are at 100% statement coverage.

**Measured vs headless Chromium (this machine).** New compare page
`scripts/compare/pages/js-dom-mutation.html` builds all of its visible content at
parse time from JavaScript — `document.title`, `getElementById`, `createElement`,
`createTextNode`, `textContent`, `setAttribute`, `appendChild`, an `innerHTML`
list, an inline `style`, and a `querySelector` read-back-and-edit. At width 640
(`scripts/compare/chromium-compare.sh`):

| page | MAD (0 = identical) |
|------|---------------------|
| `js-dom-mutation` (this phase) | **0.0131** |
| `typography` (static baseline) | 0.0113 |

The JS page scores within noise of the static-text baseline — i.e. the DOM the
engine renders after running the scripts is the same DOM Chromium renders, down to
the `.box` CSS class applied to a JS-created element and the "(edited via
querySelector)" text on the first list item. The residual is the documented
font-antialiasing delta. (The one **pre-existing, JS-unrelated** gap noted here in
Phase 2.4 — that the engine did not paint `<li>` `list-style` bullet discs — is
**closed in Phase 2.5** above; list markers now render.) The engine reports
`title="DOM mutated by JS", contentHeight=306` for this page; with JS disabled only
the static `<h1>` remains.

## Bugfix — a childless box's own explicit width was invisible to `preferredWidth`

**Date: 2026-09-04.** Found while rendering a print report with an
HTML-table-based bar chart: a chart row was `<td>label</td><td><div
style="width:200px;height:15px">…fill…</div></td><td>9,4 k$</td>`, and the
middle column collapsed to 0 width, so the third column's cells were
positioned on top of the bar instead of after it.

**Root cause** (`layout/floats.go`, `preferredWidth`): the max-content-width
estimator only ever measured a node's *children* — the widest block child, or
the widest unwrapped inline run. A node's own definite (non-auto,
non-percentage) `width` was never consulted. That is fine for a normal element
sized by its content, but a box with no text and no image — a bar-chart fill,
a spacer, a colour swatch — has no children to measure either, so it reported
a preferred width of exactly 0. `layout/table.go`'s column-sizing feeds
`preferredWidth` straight into each column's natural width, so that column
scaled to 0 and the next column's `X` landed on top of it. The same function
backs a flex item's basis (`flex.go: mainBaseRow`), so an auto-basis flex item
with an explicit width and no text content was equally at risk of a 0 base
size.

**Fix**: `preferredWidth` now checks the node's own resolved width first — a
definite width (content-box: width + edges; border-box: width as-is) is
returned outright, exactly as CSS max-content sizing requires when the author
has already fixed the width; only an auto/percentage width falls through to
measuring children. `TestTableColumnWidthFromChildlessExplicitWidthBox`
(`layout/features_test.go`) pins a 3-column table with a childless
explicit-width middle cell and asserts all three columns land at their scaled
natural widths with no overlap, plus that the inner div keeps its own 200px
(not clamped by the column).

**A related symptom that turned out not to be a bug**: the same report also
had `<dt>`/`<dd>` pairs inside `display:flex` items rendering with the `dd`
shifted right of its `dt` instead of stacked underneath it. That is the
standard user-agent default (`dd { margin-inline-start: 40px }`) doing exactly
what a real browser does absent a `dd { margin: 0 }` reset — confirmed by
reproducing both with and without the reset
(`layout/floats_test.go`-style ad hoc cases, not committed, both matched
expectation). Flex `gap` itself, tested in isolation with plain text flex
items, was already correct. Recorded here so the same false lead isn't
re-walked.

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

## Known gaps (updated 2026-08-30 — see the note above on why this drifted)

- **`display:inline-flex`/`inline-grid`/`inline-table` lose their "inline"
  half — this engine's `Display` enum conflates them with the block-level
  `flex`/`grid`/`table` values, so such an element becomes BLOCK-LEVEL from
  its parent's point of view.** Confirmed on TWO separate real pages:
  github.com's header "Sign in"/"Sign up" buttons (round 24) and pkg.go.dev's
  breadcrumb `<li>`s (round 26, `.go-Breadcrumb li{display:inline-flex}`),
  where each list item stacks on its own line instead of flowing in a row
  with `>` separators between them (the separators are ALSO missing — a
  second, already-documented gap: `::before`/`::after` generated content is
  not synthesised at all, rank 5 in the original Phase-0 fidelity plan).
  This is the SAME architectural gap as the `<react-partial>`-flattens-
  block-content one below, from the opposite direction: there, a genuinely
  block element nested in an inline context gets wrongly flattened into
  inline text; here, a genuinely INLINE-outer element (by explicit author
  CSS) gets wrongly treated as block-level. A real fix needs the same
  "atomic inline-level box with a real box/layout inside" mechanism this
  engine has never had (already deferred in round 24, comparable in scope
  to Shadow DOM) — not attempted.
- **The third-party SVG rasteriser (`github.com/srwiley/oksvg`, pinned at its
  latest available commit) mis-renders at least one real-world complex
  multi-subpath vector `<path>`.** Found live on developer.mozilla.org
  (2026-09-03, round 25, no code change possible in THIS engine): its "mdn"
  logo wordmark, drawn as vector letterforms in a single `<path>` (a common
  technique for a short brand mark, avoiding a web-font dependency), renders
  as overlapping/doubled glyph shapes. Isolated with three independent
  reproductions, the last calling `oksvg.ReadIconStream`+`rasterx` directly
  with NO go-webengine code in the call path at all — conclusive proof this
  is not a bug in this engine's own SVG handling (`svg.go`'s path
  extraction/viewBox scaling/target sizing). The pinned commit is oksvg's
  newest available one, so there is no newer upstream fix to pick up by
  bumping the dependency. Fixing (or forking) a third-party rasteriser's own
  path-fill-rule/subpath handling is outside this engine's own codebase —
  not attempted.
- **An inline element's box decoration is painted (round 48) but not
  completely**: solid `background-color`, `border` and `padding` fragment per
  line box with `box-decoration-break: slice`, and `border-radius` is honoured
  on a complete fragment; `background-image`/gradients, `box-shadow`,
  `outline` and `filter` on an inline element are still unpainted (they live
  on the `*layout.Box` path only), `box-decoration-break: clone` is not
  parsed, and a fragment's content height is the item's LINE BOX height rather
  than the font's own em box — so a large `line-height` makes the band taller
  than a browser's.

- **A block/flex/grid-level element nested under an inline-context ancestor
  (e.g. an unstyled custom element, which defaults to `display:inline`) is
  flattened into plain inline text instead of getting a real box.** Found
  live on github.com/golang/go (2026-09-03, round 24, no code change): its
  whole marketing header sits inside `<react-partial>`, a custom element
  nothing styles — `layout.go`'s `appendElementInline` has no check for a
  nested element's OWN display resolving to block-level, so it
  unconditionally recurses via `appendInline`, losing box model/margins/flex
  layout for everything inside (see the round-24 log entry above for the
  concrete symptom: two adjacent buttons render glued together with no
  gap). The correct fix — generating an anonymous block wrapper around such
  a box and promoting it to a real sibling (CSS 2.1 §9.2.1.1) — is a real
  layout feature comparable in scope to Shadow DOM slot projection, not a
  narrow bug fix; not attempted.
  **Confirmed a SECOND, independent real-world instance (2026-09-04, round
  33): news.ycombinator.com's own upvote-triangle icon.** Its markup is
  `<a href='vote?...'><div class='votearrow'></div></a>` — a plain block
  `<div>` (styled with a `background: url("triangle.svg"), linear-
  gradient(...) no-repeat` icon, no text) nested directly inside an inline
  `<a>`. Root-caused with an isolated, network-independent reproduction
  (`RenderHTML` against a minimal fixture): a background-image `url()`
  layer on a standalone `<div id=e>{...}</div>` renders correctly (44
  non-background pixels of the expected triangle shape), and the SAME CSS
  wrapped in the SAME `<a>...</a>` nesting the real page uses renders
  ZERO — confirming the div's own box (and therefore its background paint
  step) is lost inside the inline wrapper, not a background-image/SVG-
  decoding defect. (An earlier bisection attempt wrongly suspected the
  background-image pipeline itself, tracing a red herring caused by the
  test fixture's OWN construction — an inline `style="..."` HTML attribute
  containing a `url("data:...")` value whose embedded double quotes
  collide with the attribute's own delimiter, truncating the attribute
  value at HTML-parse time; switching the reproduction to a `<style>`
  block — matching how the real page's CSS actually lives, in an external
  stylesheet — immediately showed the background-image mechanism working
  correctly in isolation, redirecting the investigation to the real cause.)
  This confirms the gap is real, recurring, and independent of the
  specific site or DOM shape that first surfaced it — not a one-off. Not
  fixed, for the same reason as before: the correct fix is the same
  bigger, deliberately-deferred layout feature, not a narrow one-off patch
  for this specific icon.
- **No `@font-face` (custom web fonts) at all — `css/parse.go` explicitly
  skips it wholesale, like any other unrecognised at-rule.** Confirmed
  load-bearing live on go.dev/blog (2026-09-03, round 20 investigation, no
  code change): its nav dropdown carets are `<i class="material-icons">
  arrow_drop_down</i>` — Google's Material Icons convention, where the
  *text content itself* is a semantic ligature keyword
  (`arrow_drop_down`, `menu`, `search`, …) that a custom `@font-face` TTF
  (loaded via `fonts.googleapis.com`) substitutes for a single icon glyph
  via its own OpenType GSUB ligature table, PURELY at the font level — the
  HTML/CSS carries no icon-drawing instruction of its own. Without
  `@font-face`, `font-family:'Material Icons'` falls back to this engine's
  normal font stack, which has no such glyph and no ligature substitution
  to perform, so the raw keyword text renders literally instead of an
  arrow. **Checked whether the fix is smaller than it looks before writing
  it off**: `go-opentype/opentype` (this engine's font-parsing dependency)
  already has real GSUB support (`gsub.go`), and a separate
  `go-opentype/shape` package exists specifically to drive it — but
  `paint/fonts.go`'s `Measure`/`Metrics` call `opentype.Face` DIRECTLY
  (simple per-rune cmap+advance lookups), never `go-opentype/shape` at all,
  for ANY of this engine's existing bundled fonts. A real fix needs BOTH a
  new capability this engine has never had (fetch and load an arbitrary
  `@font-face` TTF from a URL, keyed by its declared `font-family` name,
  the same way a browser does) AND wiring the text-measurement/painting
  pipeline through real OpenType shaping instead of its current
  no-shaping-pass model — genuinely comparable in scope to Shadow DOM or
  the reskin-orphaned-node architecture already deferred this session, not
  a narrow bug fix. A narrower, name-matching special case (hide text for
  a hardcoded list of known icon-font family names) was considered and
  rejected: it would only ever cover the specific icon fonts anticipated
  in advance, the same "no speculative capability" trap this session has
  avoided elsewhere.
- **Form controls now paint (2026-09-03, engine#95 — a general
  `input`/`button`/`select`/`textarea` atomic box + paint mechanism, not
  previously present at all) and `<select>`'s sizing/label followed up to be
  standards-correct (2026-09-03, engine#96, see the log entry below)**:
  remaining gaps are `<input>`'s value/placeholder text not being
  pixel-perfect UA chrome (close enough to read as intentional, not exact),
  and no actual popup/listbox surface for `<select>` (this engine has no
  click-driven interactive UI beyond a settle pass at all).
- **The settle loop's empty-render guard (`reskin()`, engine#84) cannot
  restyle a node that a rejected script pass REMOVED from the DOM — and this
  is a BIGGER-BLAST-RADIUS gap than earlier rounds' own write-up suggested.**
  First confirmed live on react.dev (2026-09-02, engine#92) as affecting
  "the hero `<h1>` and one CTA button". Re-measured 2026-09-03 (round 19,
  no code change) by actually counting: after settle, react.dev's DOM has
  **ZERO `<h2>` elements at all** — the SAME router-failure script pass that
  toggles dark mode unmounts the ENTIRE marketing homepage's content
  sections ("Create user interfaces from components", "Write components
  with code and markup", every subsequent section), not just the hero. Every
  one of those nodes keeps its pre-failure (light-mode) style forever, on a
  now-dark background — the washed-out, low-contrast prose visible
  throughout the whole page below the very top fold is this SAME root
  cause, just far more of the page than previously documented. A general
  fix would need to diff the DOM before/after a rejected pass (to
  distinguish "removed and needs its last-known style" from "still present,
  re-cascade normally"), or re-cascade an orphaned subtree against a
  synthetic root reproducing its lost ancestor chain (its OWN `.Parent` is
  nulled by `dom.RemoveChild` at the moment of removal, so the chain isn't
  recoverable after the fact without having snapshotted it before) — still
  not attempted: a real architectural addition, not a narrow fix, and now a
  stronger candidate for a dedicated future session given the corrected,
  much larger measured scope.

Several bullets that stood here since Phase 0/1 were flatly wrong by the time of
the 2026-08-30 audit — JavaScript, external stylesheets, `overflow` clipping,
gradients/box-shadow/border-radius and real bold/italic fonts had all since
shipped (Phase 1.5–2.0, and the font work logged in the org's project memory).
Restated against what was actually verified that day:

- **Shadow DOM: CLOSED for the declarative case** (2026-08-31, engine#73/#75/
  #77 — see the log entry above for the full account). Declarative
  `<template shadowrootmode>` attachment, `<slot>` distribution (default and
  named, including correct non-rendering of unmatched/unslotted light
  content), and `:host`/`:host()` CSS scoping all ship and are verified
  against developer.mozilla.org's real markup (which uses exactly this
  mechanism — confirmed via raw fetch, 23 declarative shadow roots). Two
  real, narrower gaps remain: no imperative `attachShadow()`/
  `customElements.define()` JS API (neither confirmed real site needed it),
  and no `display:contents` (a shadow host that sets `:host{display:contents}`
  still generates its own wrapping box rather than being layout-transparent).
  `::slotted()` and `:host-context()` are parsed as unmodelled and safely
  dropped rather than guessed at.
  **Correction to the 2026-08-30 diagnosis above, and now CLOSED (2026-09-01,
  engine#80/#81)**: github.com's permanently-expanded header nav was never a
  Shadow DOM issue — confirmed by fetching its actual markup, which contains
  no `shadowrootmode`/`attachShadow` anywhere. It was two independent bugs:
  `visibility` was completely unimplemented (fixed, engine#80 — see the
  dated log entry above), and `maxExternalSheets` (20) silently dropped the
  38th of github.com's 38 `<link rel=stylesheet>` tags, which happened to be
  the one holding the header's hide/sr-only rules (fixed, raised to 64,
  engine#81). Verified live: the header now renders as a compact nav bar,
  matching a real browser.
- **CSS `@container` queries are implemented** (2026-08-31, engine#74 +
  follow-up engine integration — see the log entry above): `container-type`/
  `container-name`/the `container` shorthand, named and unnamed lookup,
  width/height size features with the same range-comparison syntax as
  `@media`, resolved against real post-layout geometry via a bounded
  cascade↔layout convergence loop (mirroring `dynamic.go`'s JS settle loop).
  Remaining, deliberate scope cuts: `@container style(...)` ("container style
  queries", a separate/newer spec feature) is not supported and its content is
  dropped wholesale; container query length units (`cqw`/`cqh`/`cqi`/`cqb`/…)
  are a different feature and are not implemented; `container-name` compares
  as a single token only (no space-separated multiple names); the measured
  container size is the border-box, not the content box a browser uses, so a
  container with substantial border/padding could resolve a boundary
  condition slightly differently than Chrome.
- **No CSS `transform`** (2D or 3D) and no `conic-gradient`, EXCEPT
  `translate`/`translateX`/`translateY` (added 2026-09-02, engine#87, for
  pkg.go.dev's off-canvas nav drawer — see FIDELITY.md's log entry above): a
  pure 2D offset applied like a relative-position shift, with no
  layout-flow effect, is the one function pulled out on its own since it
  needs no real coordinate-transform machinery in paint. `rotate`, `scale`,
  `skew`, `matrix`, any 3D function, and a `transform` value that MIXES one
  of those in among translate calls are all still entirely unsupported — a
  carousel positioned via `transform: translateX(...) rotate(...)` renders
  at its untransformed in-flow position, same as before this carve-out.
  Also means a `transform`/
  `perspective` on an ancestor never establishes a new containing block for an
  absolutely-positioned descendant the way the spec says it should — that
  descendant's containing block resolution climbs straight past it to the
  nearest genuinely `position:`ed ancestor (or the viewport), which happens
  to be correct whenever a `relative` wrapper is also present (the common
  real-world case, confirmed live on tailwindcss.com's 3D-transforms card,
  2026-09-02) but would be wrong for a page relying on `transform` ALONE to
  scope absolute children.
- **`mask-image`/`-webkit-mask-image` only understands a single `url()`
  mask, stretched to fill the element's own border box** (added 2026-09-02,
  engine#88, confirmed load-bearing live: Wikipedia's Vector-2022 skin
  renders its ENTIRE toolbar icon set this way). The real `mask-size`/
  `mask-position`/`mask-repeat`/`mask-origin` grammar is not modelled, and a
  gradient mask or a multi-layer `mask` shorthand is left unsupported
  entirely. As a side effect of fixing this, `background-image`/`mask-image`
  URLs pointing at an SVG document now actually decode (previously they
  silently rendered nothing, regardless of mask support — `loadOneBackground`
  only ever tried a raster decoder); an SVG source is detected either by its
  URL or by sniffing the fetched bytes, matching how `<img src=*.svg>`
  already worked.
- **`calc()` only understands pure arithmetic over lengths and bare numbers**
  (`+`/`-`/`*`/`/`, nested parens — added 2026-09-02, engine#86, for
  Tailwind v4's `calc(var(--spacing) * N)` spacing scale). A `calc()` mixing
  a percentage (or `vw`/`vh`) with an absolute length cannot be resolved —
  percentages depend on a containing block only known at layout time, which
  this text-level, pre-layout pass has no access to — and drops the whole
  declaration, same as before `calc()` was understood at all. `min()`,
  `max()`, and `clamp()` are not implemented.
- **The CSS-wide `inherit` keyword only works for the properties this engine
  already gives explicit inheritance semantics to** (`color`, `visibility`,
  `font-weight`, `text-align`, `white-space`, `line-height`,
  `list-style-type`, `list-style-position` — added 2026-09-02, engine#86,
  confirmed load-bearing live: Tailwind's/most frameworks' `a{color:inherit}`
  preflight reset). `inherit` on any other property (e.g.
  `background-color:inherit`, which is not even inherited by default per
  spec) is silently dropped rather than resolved. `initial`, `unset`, and
  `revert` are not implemented at all.
- **`@supports` is entirely unimplemented — its body is always skipped**, the
  same "unrecognised at-rule" treatment as `@font-face`/`@keyframes`. Unlike
  `@layer`'s "include unconditionally" fallback, `@supports` genuinely
  *gates* content, so this can drop real declarations no other mechanism
  reaches — confirmed live on developer.mozilla.org, whose native
  `light-dark()` colours (this engine does implement the function itself,
  2026-09-01, engine#85) sit behind `@supports (color: light-dark(...))` and
  so never get parsed at all; the page instead worked from its
  unconditionally-shipped polyfill fallback path (a *different* real bug in
  that path was the one actually fixed — see the log entry above). A page
  relying on `@supports` to pick between two INCOMPATIBLE rulesets (not a
  progressive-enhancement pair with a working fallback) would render however
  the always-skipped choice happens to fall.
- **`html[data-theme]`-style CSS toggles depend on `@media
  (prefers-color-scheme:*)` matching optimistically for BOTH `light` and
  `dark`** (`mediaMatches` has no dedicated case for this feature, so it
  falls through to "assume applies" for either value) — cascade order alone
  then decides which wins, which happens to match "assume dark" only when the
  dark block is declared after the light one, not by any actual preference
  model. This has been correct on every real page measured so far (a `dark`
  block declared last is the overwhelmingly common source order for a
  progressive-enhancement dark-mode addition) but is not a genuine feature
  match — treat it as coincidental, not load-bearing, if it stops working on
  some future page.
- **Table `colspan`/`rowspan` and `border-collapse`** — not re-verified this
  audit; treat as unconfirmed rather than assume either way until measured.
- **Fonts**: bundled Inter/Lora (+ Go Mono, regular-only) with real Bold/
  Italic/BoldItalic instances (no faux-bold/upright-italic); no web font
  (`@font-face`) loading, so a page's own custom typeface always falls back to
  these.
- **`sticky` ≈ `relative`, approximate z-index stacking** — the deliberate
  simplifications from Phase 1.8, unchanged.

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
go run ./cmd/render -file testdata/inline_decoration.html -out testdata/renders/inline_decoration.png -w 400 -h 200
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
