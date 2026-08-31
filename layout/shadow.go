// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import "github.com/go-webengine/engine/dom"

// renderedChildren returns the nodes layout should treat as n's children when
// walking the tree to build boxes: n's attached shadow tree's top-level
// content when n hosts one (a host's own light-DOM Children are then never
// visited directly — they render only if pulled in through a <slot>, see
// below), a <slot>'s assigned light-DOM nodes when it has any (else the
// slot's own Children, its fallback content), or n's Children unchanged
// otherwise.
//
// This is the ONE substitution point every shadow-aware render walk goes
// through instead of reading n.Children directly, so slot projection is
// transparent to the rest of the block/inline layout algorithms (contents,
// hasBlockLevelChild, appendInline) without duplicating any of them: a page
// with no Shadow DOM at all takes the final `return n.Children` on every
// call, byte-identical to before this existed.
func renderedChildren(n *dom.Node) []*dom.Node {
	if n.Shadow != nil {
		return n.Shadow.Children
	}
	if n.Tag == "slot" {
		if assigned := assignedSlotNodes(n); len(assigned) > 0 {
			return assigned
		}
	}
	return n.Children
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
