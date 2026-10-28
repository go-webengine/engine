// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"math"
	"strings"

	"github.com/go-webengine/engine/dom"
)

// counter is one CSS counter of an element's counters set (CSS Lists 3
// §4.4): a name, the element that instantiated it, and its value. A set is
// ordered outermost first, so the innermost counter of a name is the last.
type counter struct {
	name     string
	origin   *dom.Node
	value    int32
	reversed bool
}

// counterSet is an element's CSS counters set. Sets are immutable once an
// element is done: a child or a sibling copies before it changes anything,
// so one set is shared by every element that changes nothing — which is
// almost every element of a page.
type counterSet []counter

func (s counterSet) innermost(name string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i].name == name {
			return i
		}
	}
	return -1
}

// counterWalker runs the counters pass over a styled tree.
type counterWalker struct {
	sm StyleMap
	// prev is the counters set of the element or pseudo-element immediately
	// preceding the current one in tree order: the "value source" of §4.4.1.
	prev counterSet
}

// resolveCounters walks the styled tree in tree order, maintaining CSS
// counters, and resolves what depends on them: a ::before/::after whose
// content holds counter()/counters() gets its Content text, and every list
// item gets its Marker. A tree with no list item and no counter property —
// most of them — is walked once and changes nothing.
//
// The walk's depth is the cascade's: an element past maxTreeDepth has no
// style, so it — and everything under it — is skipped like display:none.
func resolveCounters(root *dom.Node, sm StyleMap) {
	w := &counterWalker{sm: sm}
	w.children(root, nil, nil)
}

// children walks n's element children (its shadow tree's when it hosts one,
// like layout's renderedChildren) with parent as their parent's set, the
// preceding-sibling chain starting at first. It returns the last child's set.
func (w *counterWalker) children(n *dom.Node, parent, first counterSet) counterSet {
	sib := first
	for _, c := range renderedKids(n) {
		if c.Type != dom.Element {
			continue
		}
		if s, ok := w.element(c, parent, sib); ok {
			sib = s
		}
	}
	return sib
}

// renderedKids is n's shadow tree's children when it hosts one, else its own.
func renderedKids(n *dom.Node) []*dom.Node {
	if n.Shadow != nil {
		return n.Shadow.Children
	}
	return n.Children
}

// element processes one element: inherit, reset, increment, set (§4), then
// its ::marker, ::before, children and ::after in tree order. ok is false
// for an element that generates no box (display:none, §4.5): it neither
// changes counters nor takes part in the sibling chain.
func (w *counterWalker) element(n *dom.Node, parent, sib counterSet) (counterSet, bool) {
	st := w.sm[n]
	if st == nil || st.Display == DisplayNone {
		return nil, false
	}
	set := w.inherit(parent, sib)
	if st.Display != DisplayContents { // no box, no counter changes (§4.5)
		set = w.applyOps(n, set, st, st.ListItem)
	}
	w.prev = set

	// ::marker is the element's first child in tree order, then ::before.
	if st.ListItem {
		w.marker(n, st, set)
	}
	var first counterSet
	if st.Before != nil {
		first = w.pseudo(n, st.Before, set, nil)
	}
	last := w.children(n, set, first)
	if st.After != nil {
		w.pseudo(n, st.After, set, last)
	}
	return set, true
}

// pseudo processes a ::before/::after: a child of host, with its own counter
// properties, whose content is resolved against its own counters set.
func (w *counterWalker) pseudo(host *dom.Node, ps *Style, parent, sib counterSet) counterSet {
	set := w.inherit(parent, sib)
	if len(ps.CounterReset)+len(ps.CounterIncrement)+len(ps.CounterSet) > 0 || ps.ContentItems != nil {
		self := &dom.Node{Type: dom.Element, Tag: "#pseudo", Parent: host}
		set = w.applyOps(self, set, ps, false)
		if ps.ContentItems != nil {
			ps.Content, set = w.contentText(ps.ContentItems, set, self)
		}
	}
	w.prev = set
	return set
}

// inherit builds an element's counters set from its parent's and its
// preceding sibling's, with the values of the element just before it in
// tree order (§4.4.1). It shares parent whenever nothing differs.
func (w *counterWalker) inherit(parent, sib counterSet) counterSet {
	set := parent
	copied := false
	for _, c := range sib {
		if !hasNamed(set, c.name) {
			if !copied {
				set = append(counterSet(nil), set...)
				copied = true
			}
			set = append(set, c)
		}
	}
	if len(set) == 0 || len(w.prev) == 0 {
		return set
	}
	// Values come from the preceding element in tree order, matched by name
	// and originating element. Sets are short in practice; a long one (deep
	// nesting) is matched through a map so this stays linear.
	if len(set)*len(w.prev) > 256 {
		type key struct {
			name   string
			origin *dom.Node
		}
		vals := make(map[key]int32, len(w.prev))
		for _, p := range w.prev {
			vals[key{p.name, p.origin}] = p.value
		}
		for i := range set {
			if v, ok := vals[key{set[i].name, set[i].origin}]; ok && v != set[i].value {
				if !copied {
					set = append(counterSet(nil), set...)
					copied = true
				}
				set[i].value = v
			}
		}
		return set
	}
	for i := range set {
		for _, p := range w.prev {
			if p.name == set[i].name && p.origin == set[i].origin {
				if p.value != set[i].value {
					if !copied {
						set = append(counterSet(nil), set...)
						copied = true
					}
					set[i].value = p.value
				}
				break
			}
		}
	}
	return set
}

func hasNamed(s counterSet, name string) bool { return s.innermost(name) >= 0 }

// applyOps applies counter-reset, then counter-increment (with a list
// item's implicit list-item increment), then counter-set (§4). The set is
// copied before its first change.
func (w *counterWalker) applyOps(n *dom.Node, set counterSet, st *Style, listItem bool) counterSet {
	if len(st.CounterReset) == 0 && len(st.CounterIncrement) == 0 && len(st.CounterSet) == 0 && !listItem {
		return set
	}
	set = append(counterSet(nil), set...)
	for _, op := range st.CounterReset {
		v := op.Value
		if op.Reversed && !op.HasValue {
			v = w.reversedInitial(n, op.Name)
		}
		set = instantiate(set, n, op.Name, v, op.Reversed)
	}
	explicitListItem := false
	for _, op := range st.CounterIncrement {
		if op.Name == "list-item" {
			explicitListItem = true
		}
		set = increment(set, n, op.Name, op.Value)
	}
	if listItem && !explicitListItem {
		step := int32(1)
		if i := set.innermost("list-item"); i >= 0 && set[i].reversed {
			step = -1
		}
		set = increment(set, n, "list-item", step)
	}
	for _, op := range st.CounterSet {
		i := set.innermost(op.Name)
		if i < 0 {
			set = instantiate(set, n, op.Name, 0, false)
			i = len(set) - 1
		}
		set[i].value = op.Value
	}
	return set
}

// instantiate adds a counter on n (§4.4.2): the innermost counter of that
// name is dropped first when n or a previous sibling of n created it. Past
// maxCounters, the innermost counter of the name is reset instead of a new
// one nested — the bound on nesting depth.
func instantiate(set counterSet, n *dom.Node, name string, v int32, reversed bool) counterSet {
	if i := set.innermost(name); i >= 0 {
		o := set[i].origin
		if o == n || (o != nil && o.Parent == n.Parent && n.Parent != nil) {
			set = append(set[:i], set[i+1:]...)
		}
	}
	if len(set) >= maxCounters {
		if i := set.innermost(name); i >= 0 {
			set[i].value, set[i].origin, set[i].reversed = v, n, reversed
			return set
		}
		return set
	}
	return append(set, counter{name: name, origin: n, value: v, reversed: reversed})
}

// increment adds by to the innermost counter of name, instantiating one at
// 0 on n when there is none. An increment that would overflow 32 bits is not
// applied (WPT counter-reset-increment-overflow-underflow).
func increment(set counterSet, n *dom.Node, name string, by int32) counterSet {
	i := set.innermost(name)
	if i < 0 {
		set = instantiate(set, n, name, 0, false)
		i = set.innermost(name)
		if i < 0 {
			return set
		}
	}
	sum := int64(set[i].value) + int64(by)
	if sum <= math.MaxInt32 && sum >= math.MinInt32 {
		set[i].value = int32(sum)
	}
	return set
}

// reversedInitial is the initial value of a counter-reset reversed(name)
// with no integer, by §4.4.2's algorithm: walking the elements of the
// counter's scope (n and its descendants, outside nested resets of the same
// name) that increment or set it, num accumulates the negated increments up
// to the first set (whose value it adds before stopping), then adds the last
// non-zero negated increment. A list item with no explicit list-item
// increment counts -1, as it will in a reversed counter (§4.6).
func (w *counterWalker) reversedInitial(n *dom.Node, name string) int32 {
	var num, lastNeg int64
	budget := maxReversedScan
	stop := false
	var visit func(e *dom.Node, root bool)
	visit = func(e *dom.Node, root bool) {
		if stop || budget <= 0 {
			return
		}
		budget--
		st := w.sm[e]
		if st == nil || st.Display == DisplayNone {
			return
		}
		if !root {
			for _, op := range st.CounterReset {
				if op.Name == name {
					return // a nested scope of the same name
				}
			}
		}
		inc, incs := int64(0), false
		for _, op := range st.CounterIncrement {
			if op.Name == name {
				inc, incs = int64(op.Value), true
			}
		}
		if !incs && name == "list-item" && st.ListItem {
			inc, incs = -1, true
		}
		set, sets := int64(0), false
		for _, op := range st.CounterSet {
			if op.Name == name {
				set, sets = int64(op.Value), true
			}
		}
		if incs || sets {
			neg := -inc
			if neg != 0 {
				lastNeg = neg
			}
			if sets {
				num += set
				stop = true
				return
			}
			num += neg
		}
		for _, c := range renderedKids(e) {
			if c.Type == dom.Element {
				visit(c, false)
			}
		}
	}
	visit(n, true)
	num += lastNeg
	if num > math.MaxInt32 {
		num = math.MaxInt32
	} else if num < math.MinInt32 {
		num = math.MinInt32
	}
	return int32(num)
}

// contentText resolves a content value's counter()/counters() against set.
// A counter() of a name the element has no counter for instantiates one at
// 0 on it (§4.7), which then also reaches its following siblings.
func (w *counterWalker) contentText(items []ContentItem, set counterSet, self *dom.Node) (string, counterSet) {
	var b strings.Builder
	for _, it := range items {
		switch it.Kind {
		case ContentString:
			b.WriteString(it.Text)
		case ContentCounter, ContentCounters:
			i := set.innermost(it.Name)
			if i < 0 {
				set = instantiate(append(counterSet(nil), set...), self, it.Name, 0, false)
				i = set.innermost(it.Name)
			}
			if it.Kind == ContentCounter {
				if i >= 0 {
					b.WriteString(FormatCounter(set[i].value, it.Style))
				}
				continue
			}
			sep := false
			for _, c := range set {
				if c.name != it.Name {
					continue
				}
				if sep {
					b.WriteString(it.Text)
				}
				b.WriteString(FormatCounter(c.value, it.Style))
				sep = true
			}
		}
	}
	return b.String(), set
}

// marker resolves a list item's ::marker (§3): its content when a ::marker
// rule sets one (none suppresses it), else list-style-type — a <string> as
// is, a counter style as the list-item counter plus the style's suffix.
func (w *counterWalker) marker(n *dom.Node, st *Style, set counterSet) {
	ms := st.Marker
	var mst *Style
	if ms != nil {
		mst = ms.Style
	}
	info := &MarkerInfo{Inside: st.ListStylePosition == ListInside, Type: st.ListStyleType}
	switch {
	case mst != nil && mst.ContentNone:
		st.Marker = nil
		return
	case mst != nil && mst.ContentItems != nil:
		info.Text, _ = w.contentText(mst.ContentItems, set, n)
	case mst != nil && mst.ContentSet:
		info.Text = mst.Content
	case st.ListStyleType == ListNone:
		st.Marker = nil
		return
	case st.ListStyleType == ListString:
		info.Text = st.ListStyleString
	default:
		v := int32(0)
		if i := set.innermost("list-item"); i >= 0 {
			v = set[i].value
		}
		info.Text = FormatCounter(v, ListStyleTypeName(st.ListStyleType)) + markerSuffix(st.ListStyleType)
		info.Glyph = st.ListStyleType.Glyph()
	}
	if info.Text == "" {
		st.Marker = nil
		return
	}
	if mst == nil {
		m := inheritFrom(*st)
		mst = &m
	}
	// The UA ::marker rule: white-space: pre, so "1. " keeps its space
	// before the item's text (Lists 3 Appendix A).
	mst.WhiteSpace = WSPre
	mst.Display = DisplayInline
	mst.Content, mst.ContentSet = info.Text, true
	info.Style = mst
	st.Marker = info
}
