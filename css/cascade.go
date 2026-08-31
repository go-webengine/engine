// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"sort"
	"strings"

	"github.com/go-webengine/engine/dom"
)

// StyleMap maps each element node to its fully-computed style.
type StyleMap map[*dom.Node]*Style

// precedence levels: the origin of a declaration dominates specificity.
const (
	precUA     = 0
	precAuthor = 1
	precInline = 2
)

type candidate struct {
	decl        Declaration
	precedence  int
	specificity int
	order       int
}

// Cascade computes styles at the default viewport width. See CascadeVW.
func Cascade(root *dom.Node) StyleMap {
	return CascadeVW(root, DefaultViewportWidth, nil)
}

// CascadeVW computes a style for every element in the tree rooted at root,
// applying the user-agent stylesheet, author rules from every <style> element
// plus any externalSheets (already-fetched CSS text, e.g. <link> stylesheets),
// and inline style="" attributes, with proper specificity and inheritance.
// @media width queries are evaluated against viewport width vw. Every
// @container rule is treated as not matching (see CascadeVWContainers) since
// no container sizes are known yet — the caller re-cascades with
// CascadeVWContainers once a layout pass has measured them.
func CascadeVW(root *dom.Node, vw float64, externalSheets []string) StyleMap {
	return CascadeVWContainers(root, vw, externalSheets, nil)
}

// CascadeVWContainers is CascadeVW extended with container-query support:
// containers maps a query-container element (one whose computed style sets
// container-type) to its size as measured by the most recent layout pass.
// A nil/empty containers (as CascadeVW passes) makes every @container rule
// evaluate to "not matching" — the correct, conservative behaviour before any
// layout has run, and the reason this is a separate entry point rather than a
// parameter CascadeVW always threads through: most callers (tests, the first
// cascade of a render) have no layout yet and want exactly today's CascadeVW
// behaviour with zero conceptual overhead.
//
// Resolving an @container condition against real geometry is inherently a
// multi-pass affair — this single call only ever consults the containers map
// it is given; iterating cascade→layout→re-measure to a fixpoint is the
// caller's job (see engine's layoutWithContainers, which plays the same role
// for container queries that dynamic.go's settle loop plays for JavaScript:
// same shape, bounded passes, deterministic termination).
func CascadeVWContainers(root *dom.Node, vw float64, externalSheets []string, containers map[*dom.Node]ContainerSize) StyleMap {
	// External <link> stylesheets precede in-document <style> rules (they load in
	// the head), so they take lower precedence at equal specificity.
	var rules []Rule
	for _, sheet := range externalSheets {
		rules = append(rules, ParseStylesheetVW(sheet, vw)...)
	}
	rules = append(rules, collectAuthorRules(root, vw)...)
	sm := StyleMap{}
	var counter int
	var walk func(n *dom.Node, parent Style, containerStack []containerFrame)
	walk = func(n *dom.Node, parent Style, containerStack []containerFrame) {
		if n.Type != dom.Element {
			// Text/Document nodes have no computed style; recurse with parent.
			for _, c := range n.Children {
				walk(c, parent, containerStack)
			}
			return
		}
		st := computeElement(n, parent, rules, &counter, containerStack, containers)
		sm[n] = &st
		childStack := containerStack
		if st.ContainerType != ContainerNormal {
			// A fresh backing array, not an append onto containerStack: two
			// sibling subtrees must never share (and risk overwriting each
			// other's view of) the same underlying array.
			childStack = make([]containerFrame, len(containerStack)+1)
			copy(childStack, containerStack)
			childStack[len(containerStack)] = containerFrame{node: n, name: st.ContainerName, typ: st.ContainerType}
		}
		for _, c := range n.Children {
			walk(c, st, childStack)
		}
	}
	// The synthetic Document root has no style; seed children with initial.
	walk(root, initialStyle(), nil)
	return sm
}

// computeElement resolves the style of a single element. containerStack is
// the chain of ancestor query containers (nearest last) available to any
// @container rule matched below; containers supplies their measured sizes.
func computeElement(n *dom.Node, parent Style, rules []Rule, counter *int, containerStack []containerFrame, containers map[*dom.Node]ContainerSize) Style {
	st := inheritFrom(parent)
	// ownProps tracks whether st.CustomProps is this element's own (already
	// cloned) map versus the parent's shared one, so we clone at most once.
	ownProps := false

	var cands []candidate
	add := func(decls []Declaration, prec, spec int) {
		for _, d := range decls {
			cands = append(cands, candidate{d, prec, spec, *counter})
			*counter++
		}
	}

	// User-agent defaults: the per-tag declarations (specificity 0) plus the few
	// descendant UA rules (matched at their real specificity, still at UA origin)
	// that alternate the nested-list marker glyph disc→circle→square by depth.
	add(uaDeclarations(n.Tag), precUA, 0)
	// The HTML `hidden` attribute maps to `display:none` via the UA rule
	// `[hidden]{display:none}`. It is added at UA origin so any author `display`
	// (even a low-specificity or normal one) still wins per the cascade — matching
	// a browser. The modern `hidden="until-found"` value is revealable content and
	// is NOT hidden here.
	if v, ok := n.Attribute("hidden"); ok && !strings.EqualFold(strings.TrimSpace(v), "until-found") {
		add([]Declaration{{Property: "display", Value: "none"}}, precUA, 0)
	}
	for _, r := range uaDescendantRules {
		spec := -1
		for _, sel := range r.Selectors {
			if sel.Matches(n) {
				if s := sel.Specificity(); s > spec {
					spec = s
				}
			}
		}
		if spec >= 0 {
			add(r.Declarations, precUA, spec)
		}
	}
	// Legacy HTML presentational attributes (width, bgcolor, align, <font> …) map
	// to declarations at author origin with zero specificity, placed BEFORE any
	// author stylesheet — so a real CSS rule always overrides them, a UA default
	// does not. They are added here (ahead of the author-rule loop) so their
	// cascade order precedes every matched author rule.
	add(presentationalHints(n), precAuthor, 0)
	// Author rules whose selectors match, each at its selector's specificity.
	for _, r := range rules {
		spec := -1
		for _, sel := range r.Selectors {
			if sel.Matches(n) {
				if s := sel.Specificity(); s > spec {
					spec = s
				}
			}
		}
		if spec >= 0 {
			if r.Container != nil && !r.Container.satisfied(containerStack, containers) {
				continue // selector matched, but its @container condition does not (yet, or ever)
			}
			add(r.Declarations, precAuthor, spec)
		}
	}
	// Inline style attribute (highest origin).
	if inline, ok := n.Attribute("style"); ok {
		add(ParseDeclarations(inline), precInline, 0)
	}

	// Sort ascending so the last-applied declaration wins.
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		// !important forms its own cascade tier above every normal declaration,
		// regardless of origin or specificity: an author `!important` beats an
		// inline non-important style, which otherwise would always win. Within
		// each tier (important or not), origin/specificity/order still apply
		// exactly as before — this is the one addition to an otherwise-unchanged
		// comparator, so a stylesheet with no `!important` sorts identically.
		if a.decl.Important != b.decl.Important {
			return !a.decl.Important
		}
		if a.precedence != b.precedence {
			return a.precedence < b.precedence
		}
		if a.specificity != b.specificity {
			return a.specificity < b.specificity
		}
		return a.order < b.order
	})

	// Pass 0: resolve custom properties into the element's map (in cascade order,
	// last wins). They inherit from the parent (copied on first write so the
	// parent's shared map is never mutated) and are consulted by var().
	for _, c := range cands {
		if isCustomProperty(c.decl.Property) {
			if st.CustomProps == nil {
				st.CustomProps = map[string]string{}
			} else if !ownProps {
				st.CustomProps = cloneProps(st.CustomProps)
				ownProps = true
			}
			st.CustomProps[c.decl.Property] = c.decl.Value
		}
	}

	// applyResolved substitutes var() in a declaration's value before applying
	// it; a var() that cannot resolve (and has no fallback) drops the whole
	// declaration, leaving the inherited/initial value in place.
	applyResolved := func(d Declaration, emRef float64) {
		v, ok := resolveDeclValue(d.Value, st.CustomProps)
		if !ok {
			return
		}
		d.Value = v
		st.apply(d, emRef)
	}

	// Pass 1: resolve font-size first (its em is relative to the parent).
	for _, c := range cands {
		if c.decl.Property == "font-size" {
			applyResolved(c.decl, parent.FontSize)
		}
	}
	// Pass 2: everything else, with em relative to the element's own size.
	for _, c := range cands {
		if c.decl.Property != "font-size" && !isCustomProperty(c.decl.Property) {
			applyResolved(c.decl, st.FontSize)
		}
	}
	return st
}

// cloneProps returns a shallow copy of a custom-property map, used for
// copy-on-write so an element's overrides never mutate the parent's inherited
// map.
func cloneProps(m map[string]string) map[string]string {
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// collectAuthorRules parses every <style> element's text into rules, evaluating
// @media width queries against viewport width vw.
func collectAuthorRules(root *dom.Node, vw float64) []Rule {
	var sb strings.Builder
	var walk func(n *dom.Node)
	walk = func(n *dom.Node) {
		if n.Type == dom.Element && n.Tag == "style" {
			for _, c := range n.Children {
				if c.Type == dom.Text {
					sb.WriteString(c.Text)
					sb.WriteByte('\n')
				}
			}
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(root)
	return ParseStylesheetVW(sb.String(), vw)
}
