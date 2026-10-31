// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"context"
	"hash/fnv"
	"image"
	"io"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/layout"
	"github.com/go-webengine/engine/paint"
)

// maxSettlePasses bounds how many times the settle loop re-runs
// cascade→layout→inject-scripts before the final paint. Real progressive
// enhancement (a class toggle, a nav collapse, a ResourceLoader chain) settles
// in one or two rounds; the cap guarantees termination regardless.
const maxSettlePasses = 3

// renderPass is the mutable output of one cascade+layout, threaded through the
// settle loop and consumed by the final paint.
type renderPass struct {
	sheets  []string
	sm      css.StyleMap
	box     *layout.Box
	height  float64
	imgSize map[*dom.Node][2]float64
	imgs    map[*dom.Node]image.Image
	bgImgs  map[string]image.Image
	// vecs are the vector forms of the SVG bitmaps in imgs and bgImgs, for
	// PaintVector.
	vecs paint.VectorImages
}

// loadAllImages fills the pass's image maps: the replaced elements', the
// backgrounds', and the vector forms of the SVGs among them.
func (e *Engine) loadAllImages(ctx context.Context, doc *Document, rp *renderPass, viewportW int) {
	rp.imgSize, rp.imgs, rp.vecs = e.loadImagesVec(ctx, doc, rp.sm, viewportW)
	rp.bgImgs = e.loadBackgroundImagesVec(ctx, doc, rp.sm, rp.vecs)
}

// layoutMetrics implements js.Metrics: it answers getBoundingClientRect /
// offset* / getComputedStyle from the real laid-out box tree + computed styles
// of one layout pass. Replaced each pass via Session.SetMetrics.
type layoutMetrics struct {
	idx      map[*dom.Node]layout.Rect
	sm       css.StyleMap
	vpW, vpH int
	scale    float64 // device px per CSS px; 0 means 1 (see scaled)

	box   *layout.Box               // the tree idx was built from
	boxes map[*dom.Node]*layout.Box // element → its block-level box, built on first use

	// Forced synchronous layout (see liveMetrics / Refresh): the document, the
	// signature of the DOM the current geometry was laid out from, and how to
	// lay it out again. relayout is nil for a static snapshot.
	root     *dom.Node
	sig      uint64
	relayout func() (*layout.Box, css.StyleMap)
}

func newLayoutMetrics(box *layout.Box, sm css.StyleMap, vpW, vpH int) *layoutMetrics {
	return &layoutMetrics{idx: layout.BuildIndex(box), sm: sm, vpW: vpW, vpH: vpH, box: box}
}

// liveMetrics is the Metrics a script session reads: the pass rp just laid
// out, plus the means to lay the document out again when a script has
// mutated it since — so geometry read right after a mutation (Marp's fitting
// headings wrap a heading's content in a new span and measure it at once) is
// the geometry of the mutated DOM, as CSSOM View's getBoundingClientRect /
// offset* / client* / scroll* require. The re-layout is private to the
// metrics: the settle loop still decides what the render's own pass is.
func (e *Engine) liveMetrics(doc *Document, rp *renderPass, fonts *paint.Fonts, vpW, vpH int) *layoutMetrics {
	m := newLayoutMetrics(rp.box, rp.sm, vpW, vpH).scaled(e.devScale())
	m.root, m.sig = doc.Root, treeSignature(doc.Root)
	sheets, imgSize := rp.sheets, rp.imgSize
	m.relayout = func() (*layout.Box, css.StyleMap) {
		sm := e.cascade(doc.Root, vpW, sheets, nil, fonts)
		box, h := e.layoutDoc(doc.Root, sm, vpW, vpH, fonts, imgSize)
		sm, box, _ = e.layoutWithContainers(doc.Root, sheets, vpW, vpH, fonts, imgSize, sm, box, h)
		return box, sm
	}
	return m
}

// Refresh implements js.Refresher: when the DOM differs from the one the
// current geometry describes, lay it out again first.
func (m *layoutMetrics) Refresh() {
	if m.relayout == nil {
		return
	}
	sig := treeSignature(m.root)
	if sig == m.sig {
		return
	}
	box, sm := m.relayout()
	m.idx, m.sm, m.box, m.boxes, m.sig = layout.BuildIndex(box), sm, box, nil, sig
}

// treeSignature hashes everything layout depends on in the DOM — structure,
// tags, attributes (class and style included) and text — without
// serialising it, so the check before every geometry read stays cheap. Each
// node's child count is folded in, so moving a node to another parent
// changes it even when the markup reads the same.
func treeSignature(root *dom.Node) uint64 {
	h := fnv.New64a()
	var buf [8]byte
	num := func(v uint64) {
		for i := range buf {
			buf[i] = byte(v >> (8 * i))
		}
		_, _ = h.Write(buf[:])
	}
	str := func(s string) {
		num(uint64(len(s)))
		_, _ = io.WriteString(h, s)
	}
	var walk func(n *dom.Node, depth int)
	walk = func(n *dom.Node, depth int) {
		if n == nil || depth > 512 {
			return
		}
		num(uint64(n.Type))
		str(n.Tag)
		str(n.Text)
		num(uint64(len(n.Attr)))
		if len(n.Attr) > 0 {
			keys := make([]string, 0, len(n.Attr))
			for k := range n.Attr {
				keys = append(keys, k)
			}
			sort.Strings(keys) // map order is random; the hash must not be
			for _, k := range keys {
				str(k)
				str(n.Attr[k])
			}
		}
		num(uint64(len(n.Children)))
		for _, c := range n.Children {
			walk(c, depth+1)
		}
		if n.Shadow != nil {
			for _, c := range n.Shadow.Children {
				walk(c, depth+1)
			}
		}
	}
	walk(root, 0)
	return h.Sum64()
}

// boxOf returns n's block-level box in the current tree, or nil for an
// element that is inline-level or not laid out.
func (m *layoutMetrics) boxOf(n *dom.Node) *layout.Box {
	if m.boxes == nil {
		m.boxes = map[*dom.Node]*layout.Box{}
		var visit func(b *layout.Box)
		visit = func(b *layout.Box) {
			if b == nil {
				return
			}
			if b.Node != nil && m.boxes[b.Node] == nil {
				m.boxes[b.Node] = b
			}
			for _, c := range b.Children {
				visit(c)
			}
		}
		visit(m.box)
	}
	return m.boxes[n]
}

// paddingRect is a box's padding box (border box less its borders).
func paddingRect(b *layout.Box) (x, y, w, h float64) {
	bw := css.Edges{}
	if b.Style != nil {
		bw = b.Style.Border.Widths()
	}
	return b.X + bw.Left, b.Y + bw.Top, math.Max(b.W-bw.Left-bw.Right, 0), math.Max(b.H-bw.Top-bw.Bottom, 0)
}

// ClientSize implements js.BoxMetrics: the padding box (CSSOM View
// clientWidth/clientHeight; this engine renders no scrollbars). An element
// with no box, or an inline-level one, reports 0.
func (m *layoutMetrics) ClientSize(n *dom.Node) (w, h float64, ok bool) {
	b := m.boxOf(n)
	if b == nil {
		return 0, 0, true
	}
	_, _, pw, ph := paddingRect(b)
	return m.css(pw), m.css(ph), true
}

// ScrollSize implements js.BoxMetrics: the size of the element's scrolling
// area (CSSOM View scrollWidth/scrollHeight), i.e. its scrollable overflow
// area (CSS Overflow 3 §3.3): the union of its padding box, the line boxes
// and inline content it contains, and its descendants' border boxes —
// descending into a descendant only when that descendant does not clip its
// own overflow — measured from the padding box's top-left corner. An
// inline-level element reports its border box; one with no box, 0.
func (m *layoutMetrics) ScrollSize(n *dom.Node) (w, h float64, ok bool) {
	b := m.boxOf(n)
	if b == nil {
		if r, ok := m.idx[n]; ok {
			return m.css(r.W), m.css(r.H), true
		}
		return 0, 0, true
	}
	px, py, pw, ph := paddingRect(b)
	right, bottom := px+pw, py+ph
	grow := func(r, bt float64) {
		right, bottom = math.Max(right, r), math.Max(bottom, bt)
	}
	var visit func(b *layout.Box)
	visit = func(b *layout.Box) {
		for _, ln := range b.Lines {
			grow(ln.X+ln.W, ln.Y+ln.H)
			for _, it := range ln.Items {
				grow(it.X+it.Width, it.Y+it.LineHeight)
			}
		}
		for _, c := range b.Children {
			grow(c.X+c.W, c.Y+c.H)
			if c.Style == nil || (c.Style.OverflowX == css.OverflowVisible && c.Style.OverflowY == css.OverflowVisible) {
				visit(c)
			}
		}
	}
	visit(b)
	return m.css(right - px), m.css(bottom - py), true
}

// Rect returns n's used border-box rect (document coords).
func (m *layoutMetrics) Rect(n *dom.Node) (x, y, w, h float64, ok bool) {
	r, ok := m.idx[n]
	if !ok {
		return 0, 0, 0, 0, false
	}
	return m.css(r.X), m.css(r.Y), m.css(r.W), m.css(r.H), true
}

// Computed resolves the used value of a geometry-relevant property. Unknown
// properties report ok=false (getComputedStyle then returns "").
func (m *layoutMetrics) Computed(n *dom.Node, prop string) (string, bool) {
	st := m.sm[n]
	r, hasRect := m.idx[n]
	switch prop {
	case "width":
		if hasRect && st != nil {
			return m.px(contentAxis(r.W, st.Padding.Left+st.Padding.Right, st.Border.Widths().Left+st.Border.Widths().Right)), true
		}
		if hasRect {
			return m.px(r.W), true
		}
	case "height":
		if hasRect && st != nil {
			return m.px(contentAxis(r.H, st.Padding.Top+st.Padding.Bottom, st.Border.Widths().Top+st.Border.Widths().Bottom)), true
		}
		if hasRect {
			return m.px(r.H), true
		}
	}
	if st == nil {
		return "", false
	}
	switch prop {
	case "display":
		if st.Display == css.DisplayNone || !hasRect {
			return "none", true
		}
		return displayString(st.Display), true
	case "position":
		return positionString(st.Position), true
	case "margin-top":
		return m.px(st.Margin.Top), true
	case "margin-right":
		return m.px(st.Margin.Right), true
	case "margin-bottom":
		return m.px(st.Margin.Bottom), true
	case "margin-left":
		return m.px(st.Margin.Left), true
	case "padding-top":
		return m.px(st.Padding.Top), true
	case "padding-right":
		return m.px(st.Padding.Right), true
	case "padding-bottom":
		return m.px(st.Padding.Bottom), true
	case "padding-left":
		return m.px(st.Padding.Left), true
	case "font-size":
		return m.px(st.FontSize), true
	case "font-weight":
		return strconv.Itoa(st.FontWeight), true
	case "font-style":
		if st.Italic {
			return "italic", true
		}
		return "normal", true
	case "line-height":
		if lh, ok := st.LineHeight.Resolve(st.FontSize); ok {
			return m.px(lh), true
		}
		return "normal", true
	case "color":
		return colorString(st.Color), true
	case "background-color":
		return colorString(st.Background), true
	case "text-align":
		return textAlignString(st.TextAlign), true
	case "box-sizing":
		if st.BoxSizing == css.BorderBox {
			return "border-box", true
		}
		return "content-box", true
	case "opacity":
		if st.HasOpacity {
			return strconv.FormatFloat(st.Opacity, 'f', -1, 64), true
		}
		return "1", true
	case "z-index":
		if st.ZIndexAuto {
			return "auto", true
		}
		return strconv.Itoa(st.ZIndex), true
	case "visibility":
		switch st.Visibility {
		case css.VisibilityHidden:
			return "hidden", true
		case css.VisibilityCollapse:
			return "collapse", true
		default:
			return "visible", true
		}
	case "top":
		return m.lengthUsed(st.Top), true
	case "right":
		return m.lengthUsed(st.Right), true
	case "bottom":
		return m.lengthUsed(st.Bottom), true
	case "left":
		return m.lengthUsed(st.Left), true
	}
	return "", false
}

// scaled returns m reporting at a device scale: the layout and computed
// lengths it reads are in device px (Engine.DeviceScale), and a script must
// see CSS px, so every length it answers is divided by scale.
func (m *layoutMetrics) scaled(scale float64) *layoutMetrics {
	m.scale = scale
	return m
}

// css converts a device-px length to CSS px.
func (m *layoutMetrics) css(v float64) float64 {
	if m.scale > 0 {
		return v / m.scale
	}
	return v
}

func (m *layoutMetrics) px(v float64) string { return pxStr(m.css(v)) }

func (m *layoutMetrics) lengthUsed(l css.Length) string {
	if !l.Auto && !l.IsPercent {
		l.Px = m.css(l.Px)
	}
	return lengthUsed(l)
}

// contentAxis subtracts padding+border from a border-box extent, clamped at 0.
func contentAxis(border, padding, borderW float64) float64 {
	v := border - padding - borderW
	if v < 0 {
		return 0
	}
	return v
}

func pxStr(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) + "px" }

func lengthUsed(l css.Length) string {
	if l.Auto {
		return "auto"
	}
	if l.IsPercent {
		return strconv.FormatFloat(l.Percent*100, 'f', -1, 64) + "%"
	}
	return pxStr(l.Px)
}

func colorString(c css.Color) string {
	if c.A == 0 {
		return "rgba(0, 0, 0, 0)"
	}
	if c.A == 255 {
		return "rgb(" + strconv.Itoa(int(c.R)) + ", " + strconv.Itoa(int(c.G)) + ", " + strconv.Itoa(int(c.B)) + ")"
	}
	a := strconv.FormatFloat(float64(c.A)/255, 'f', -1, 64)
	return "rgba(" + strconv.Itoa(int(c.R)) + ", " + strconv.Itoa(int(c.G)) + ", " + strconv.Itoa(int(c.B)) + ", " + a + ")"
}

func displayString(d css.Display) string {
	switch d {
	case css.DisplayBlock:
		return "block"
	case css.DisplayNone:
		return "none"
	case css.DisplayInlineBlock:
		return "inline-block"
	case css.DisplayFlex:
		return "flex"
	case css.DisplayGrid:
		return "grid"
	case css.DisplayTable:
		return "table"
	case css.DisplayTableRow:
		return "table-row"
	case css.DisplayTableCell:
		return "table-cell"
	case css.DisplayTableRowGroup:
		return "table-row-group"
	}
	return "inline"
}

func positionString(p css.Position) string {
	switch p {
	case css.PositionRelative:
		return "relative"
	case css.PositionAbsolute:
		return "absolute"
	case css.PositionFixed:
		return "fixed"
	case css.PositionSticky:
		return "sticky"
	}
	return "static"
}

func textAlignString(a css.TextAlign) string {
	switch a {
	case css.AlignCenter:
		return "center"
	case css.AlignRight:
		return "right"
	}
	return "left"
}

// domSignature is a structural+attribute+text digest of the document, used to
// detect whether a script pass changed anything the cascade/layout depends on.
// It is stable given the deterministic (sorted-attribute) serialization, so an
// unchanged signature across a pass means the geometry is unchanged too (layout
// is a pure function of DOM + styles + viewport) — the settle loop's fixpoint.
func domSignature(root *dom.Node) uint64 {
	h := fnv.New64a()
	if el := dom.Find(root, "html"); el != nil {
		_, _ = h.Write([]byte(dom.OuterHTML(el)))
	}
	return h.Sum64()
}

// settleState is what the settle loop carries between passes: the DOM
// signature the current layout was computed from, the stylesheet links the
// sheets were fetched for, the deadline budget, and whether a pass re-laid
// the document out. Scripts (settle, settle_js.go) and Engine.Adjust
// (adjust.go) settle through it alike.
type settleState struct {
	sig        uint64
	prevLinks  uint64
	layoutDur  time.Duration
	relaidOut  bool
	overBudget func() bool
}

// newSettleState captures the DOM before the scripts (or Adjust) run.
func newSettleState(ctx context.Context, doc *Document, initialLayout time.Duration) *settleState {
	s := &settleState{sig: domSignature(doc.Root), prevLinks: linkKey(doc.Root), layoutDur: initialLayout}
	deadline, hasDeadline := ctx.Deadline()
	s.overBudget = func() bool { return hasDeadline && time.Until(deadline) < 2*s.layoutDur }
	return s
}

// relayoutPass is one pass of the settle loop: when the DOM changed since
// the last layout, re-cascade and re-lay it out into rp. It returns false
// when the loop must stop: nothing changed, the deadline is near, or the
// pass was rejected for emptying the page.
func (e *Engine) relayoutPass(ctx context.Context, doc *Document, vpW, vpH int, fonts *paint.Fonts, rp *renderPass, s *settleState, onStage func(stage string, rp *renderPass)) bool {
	newSig := domSignature(doc.Root)
	if newSig == s.sig {
		return false // scripts changed nothing layout-relevant: initial layout stands
	}
	// Bound the added cost: never start a re-layout that risks blowing a
	// caller's deadline (a very expensive page renders its pre-settle layout
	// rather than timing out).
	if s.overBudget() {
		e.jslog("settle: re-layout skipped to respect deadline")
		return false
	}
	s.sig = newSig

	// Re-collect the cascade from the MUTATED DOM: injected <style> text is
	// picked up by the cascade's own <style> walk; injected <link> stylesheets
	// are refetched only when the link set actually changed (bounded network).
	if k := linkKey(doc.Root); k != s.prevLinks {
		rp.sheets = e.fetchExternalSheets(ctx, doc, float64(vpW))
		s.prevLinks = k
	}
	newSm := e.cascade(doc.Root, vpW, rp.sheets, nil, fonts)
	start := time.Now()
	newBox, newHeight := e.layoutDoc(doc.Root, newSm, vpW, vpH, fonts, rp.imgSize)
	// A script's DOM/class mutation can change which elements are query
	// containers, or their sizes, so @container queries need the same
	// bounded re-resolution here that the initial (pre-settle) layout gets
	// in renderCoreStaged.
	newSm, newBox, newHeight = e.layoutWithContainers(doc.Root, rp.sheets, vpW, vpH, fonts, rp.imgSize, newSm, newBox, newHeight)
	s.layoutDur = time.Since(start)

	// Never let a script pass erase an already-good render: a client-side
	// router that fails to load its own error route (observed on react.dev)
	// can unmount the whole app tree while still leaving the DOM "changed"
	// (so the signature check above does not save us). If the page had real
	// content before this pass and has essentially none after it, the pass is
	// almost certainly a broken script re-render, not an intentional wipe —
	// keep the last good layout and stop, rather than settling on blank.
	if !renderedEmpty(rp.box, rp.height) && renderedEmpty(newBox, newHeight) {
		// Keeping the prior GEOMETRY is right (see above), but discarding this
		// pass wholesale also throws away any harmless, unrelated style-only
		// mutation that happened in the SAME script batch as whatever emptied
		// the render — observed live on react.dev: its dark-mode toggle script
		// (an ordinary matchMedia('(prefers-color-scheme: dark)') + classList.
		// add('dark') on <html>, and nothing to do with hydration) runs in the
		// same pass as a client-side router that fails to mount and empties the
		// tree. Both mutations land in one signature check; rejecting the pass
		// rejected the good class-toggle style change along with the bad
		// content wipe, so the page silently stayed in its pre-JS light theme
		// forever even though <html class="dark"> was correctly present in the
		// final DOM. Node identity survives a script pass (mutation is in
		// place, not a replacement), so re-skinning the PRESERVED box tree with
		// the freshly cascaded styles is safe: geometry (X/Y/W/H/children)
		// stays exactly as before, only each box's Style pointer is swapped to
		// its up-to-date computed value.
		e.jslog("settle: script pass emptied an already-rendered page; keeping prior layout, re-skinning with its styles")
		reskin(rp.box, newSm)
		rp.sm = newSm
		return false
	}
	rp.sm, rp.box, rp.height = newSm, newBox, newHeight
	s.relaidOut = true

	// Progressive: emit an intermediate frame for this re-laid-out pass (the
	// interruptible-reflow analogue). The caller dedups a pass whose geometry
	// did not visibly change from the previous emitted frame.
	if onStage != nil {
		onStage("settle", rp)
	}
	return true
}

// finishSettle is the settle loop's final resource refresh: the settled
// DOM/CSS can reference theme-specific <img>/background bitmaps (e.g. a
// dark-theme set the initial light layout never fetched) or newly-injected
// images. When a pass actually changed the layout, reload the
// images/backgrounds against the settled styles and lay out once more so the
// final paint matches — budget-gated so an expensive page is never pushed
// over its deadline.
func (e *Engine) finishSettle(ctx context.Context, doc *Document, vpW, vpH int, fonts *paint.Fonts, rp *renderPass, s *settleState) {
	if s.relaidOut && !s.overBudget() {
		e.loadAllImages(ctx, doc, rp, e.deviceW(vpW))
		rp.box, rp.height = e.layoutDoc(doc.Root, rp.sm, vpW, vpH, fonts, rp.imgSize)
		rp.sm, rp.box, rp.height = e.layoutWithContainers(doc.Root, rp.sheets, vpW, vpH, fonts, rp.imgSize, rp.sm, rp.box, rp.height)
	}
}

// reskin walks a PRESERVED box tree (geometry already final and not to be
// touched) and swaps each box's, marker's, and inline text run's Style to its
// up-to-date computed value from sm, leaving X/Y/W/H/Children/Lines/Items
// themselves untouched. It is the settle loop's way of adopting a rejected
// pass's style-only changes (see the renderedEmpty guard above) without
// adopting that pass's broken geometry. Inline text color (and other
// per-InlineItem style) is carried on InlineItem.Style rather than the
// enclosing box's Style, so line items need the same treatment as boxes for a
// dark-mode class toggle to actually recolor rendered text. A node with no
// entry in sm (e.g. an anonymous box, or a synthetic item with no originating
// element) keeps its current Style.
func reskin(box *layout.Box, sm css.StyleMap) {
	if box == nil {
		return
	}
	if box.Node != nil {
		if st, ok := sm[box.Node]; ok {
			box.Style = st
			if box.Marker != nil {
				box.Marker.Style = st
			}
		}
	}
	for _, line := range box.Lines {
		for _, item := range line.Items {
			if item.Node == nil {
				continue
			}
			if st, ok := sm[item.Node]; ok {
				item.Style = st
			}
		}
	}
	for _, child := range box.Children {
		reskin(child, sm)
	}
}

// linkKey is a digest of the document's stylesheet <link> hrefs (in order), used
// to decide whether external sheets must be refetched after a script pass.
func linkKey(root *dom.Node) uint64 {
	h := fnv.New64a()
	for _, ln := range css.StylesheetLinks(root) {
		_, _ = h.Write([]byte(ln.Href))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}

// jslog routes an engine diagnostic to JSLog if set.
func (e *Engine) jslog(msg string) {
	if e.JSLog != nil {
		e.JSLog("engine: " + msg)
	}
}
