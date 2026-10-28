// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import (
	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

// renderedChildren returns the nodes layout should treat as n's children when
// walking the tree to build boxes: n's attached shadow tree's top-level
// content when n hosts one (a host's own light-DOM Children are then never
// visited directly — they render only if pulled in through a <slot>, see
// below), a <slot>'s assigned light-DOM nodes when it has any (else the
// slot's own Children, its fallback content), or n's Children unchanged
// otherwise — with any `display:contents` element in that list replaced by
// ITS OWN rendered children (recursively), per flattenContents — and, when
// the cascade generated one, a synthetic ::before/::after element prepended/
// appended (see pseudoElementNode), exactly the way a real leading/trailing
// child would appear.
//
// This is the ONE substitution point every shadow-aware render walk goes
// through instead of reading n.Children directly, so slot projection AND
// display:contents are transparent to the rest of the block/inline layout
// algorithms (contents, hasBlockLevelChild, appendInline, flexItems,
// gridItems, table row/cell collection) without duplicating either of them: a
// page with neither Shadow DOM nor display:contents anywhere takes the final
// `return n.Children` on every call, byte-identical to before either existed.
// ::before/::after piggybacks on the SAME guarantee: a page with no rule
// setting `content` on either pseudo-element takes that same unchanged path.
func (l *layouter) renderedChildren(n *dom.Node) []*dom.Node {
	var kids []*dom.Node
	switch {
	case n.Shadow != nil:
		kids = l.flattenContents(n.Shadow.Children)
	case n.Tag == "slot":
		if assigned := assignedSlotNodes(n); len(assigned) > 0 {
			kids = l.flattenContents(assigned)
		} else {
			kids = l.flattenContents(n.Children)
		}
	default:
		kids = l.flattenContents(n.Children)
	}
	st := l.sm[n]
	inside := st != nil && st.ListItem && st.Marker != nil && st.Marker.Inside && st.Marker.Style != nil
	if st == nil || (st.Before == nil && st.After == nil && !inside) {
		return kids
	}
	// A fresh slice, never an in-place append onto kids: when neither branch
	// above copied (the common display:contents-free, Shadow-free case),
	// kids still aliases n.Children's own backing array, and appending to it
	// directly would risk writing past n.Children's own length into
	// whatever memory follows it.
	out := make([]*dom.Node, 0, len(kids)+3)
	if inside {
		// An inside ::marker is an inline box at the very start of the
		// item's content, before ::before (CSS Lists 3 §3.5).
		out = append(out, l.pseudoElementNode(n, st.Marker.Style))
	}
	if st.Before != nil {
		out = append(out, l.pseudoElementNode(n, st.Before))
	}
	out = append(out, kids...)
	if st.After != nil {
		out = append(out, l.pseudoElementNode(n, st.After))
	}
	return out
}

// pseudoElementNode returns the synthetic element renderedChildren injects
// for one of host's ::before/::after pseudo-elements (st is n's own
// css.Style.Before or .After — see its doc comment), building it once and
// caching it by st's own pointer identity so every later call for the same
// host/pseudo-element pair (layout visits a node more than once) returns the
// identical *dom.Node.
//
// Scope: Content is the ONLY generated-content source modelled (a literal
// `content:"<string>"`, the confirmed real trigger — pkg.go.dev's breadcrumb
// separators). A background-image/mask-image on a ::before/::after rule is
// NOT fetched: engine's own pre-layout image-loading walk (images.go) reads
// the real DOM's n.Children directly, before layout (and this synthesis)
// ever runs, so a pseudo-element's own background-image would need that walk
// taught about Before/After too — a real, separate follow-up (the same shape
// Shadow DOM needed, round 55), not attempted here. The synthetic element's
// Tag ("#pseudo") is deliberately unlike any real HTML tag so nothing
// mistakes it for one; it carries no Attr, matching an anonymous box having
// no attributes to look up.
func (l *layouter) pseudoElementNode(host *dom.Node, st *css.Style) *dom.Node {
	if n, ok := l.pseudoNodes[st]; ok {
		return n
	}
	n := &dom.Node{Type: dom.Element, Tag: "#pseudo", Parent: host}
	if st.Content != "" {
		n.Children = []*dom.Node{{Type: dom.Text, Text: st.Content, Parent: n}}
	}
	l.sm[n] = st
	if l.pseudoNodes == nil {
		l.pseudoNodes = map[*css.Style]*dom.Node{}
	}
	l.pseudoNodes[st] = n
	return n
}

// flattenContents replaces any `display:contents` element in children with
// its own rendered children (recursively via renderedChildren, so a
// display:contents element nested inside another is handled too), so it
// never itself reaches box placement — CSS's real behaviour: the element
// generates no box, its children act as if they were direct children of ITS
// parent instead. Confirmed load-bearing live on developer.mozilla.org,
// whose reference-article layout wraps the actual header/body content in
// `<main class="layout__content">` with exactly `display:contents`, solely
// to keep `<main>` out of the enclosing CSS Grid's item list while letting
// its children still receive their OWN `grid-area` placement — without this,
// `<main>` (which has no grid-area of its own) became an unnamed grid item,
// auto-placed into the wrong cell, while its real children's `grid-area`
// assignments were silently ignored (grid-area only matters on a direct
// grid item), corrupting the whole layout's column placement.
//
// The common case (no display:contents anywhere in children) returns the
// slice unchanged — no allocation, no scan cost beyond the one Display check
// per element.
func (l *layouter) flattenContents(children []*dom.Node) []*dom.Node {
	hasContents := false
	for _, c := range children {
		if c.Type == dom.Element {
			if cs := l.sm[c]; cs != nil && cs.Display == css.DisplayContents {
				hasContents = true
				break
			}
		}
	}
	if !hasContents {
		return children
	}
	out := make([]*dom.Node, 0, len(children))
	for _, c := range children {
		if c.Type == dom.Element {
			if cs := l.sm[c]; cs != nil && cs.Display == css.DisplayContents {
				out = append(out, l.renderedChildren(c)...)
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// assignedSlotNodes returns the host's light-DOM children assigned to slot (a
// <slot> element inside an attached shadow tree), in document order: an
// unnamed slot ("slot" with no "name", or name="") receives every light child
// with no "slot" attribute, INCLUDING every Text node (a text node can never
// carry a "slot" attribute, so it can only ever go to the default slot); a
// named slot receives exactly the Element children whose own "slot"
// attribute equals its name. A light child whose "slot" attribute names a
// slot that does not exist in the shadow tree is assigned to nothing and so
// is never returned by any call here — per spec it simply does not render,
// matching a real browser rather than falling back to the default slot.
//
// Recomputed on every call (not cached anywhere) so a script mutating the
// host's children, or a slot's "name"/an element's "slot" attribute, between
// layout passes is picked up automatically by the settle loop's re-layout,
// with no separate invalidation path to keep in sync.
func assignedSlotNodes(slot *dom.Node) []*dom.Node {
	host := slot.ShadowHost
	if host == nil {
		return nil // a <slot> outside any attached shadow tree assigns nothing
	}
	name, _ := slot.Attribute("name")
	var out []*dom.Node
	for _, c := range host.Children {
		switch c.Type {
		case dom.Text:
			if name == "" {
				out = append(out, c)
			}
		case dom.Element:
			slotAttr, _ := c.Attribute("slot")
			if slotAttr == name {
				out = append(out, c)
			}
		}
	}
	return out
}
