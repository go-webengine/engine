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
// @media width queries are evaluated against viewport width vw.
func CascadeVW(root *dom.Node, vw float64, externalSheets []string) StyleMap {
	// External <link> stylesheets precede in-document <style> rules (they load in
	// the head), so they take lower precedence at equal specificity.
	var rules []Rule
	for _, sheet := range externalSheets {
		rules = append(rules, ParseStylesheetVW(sheet, vw)...)
	}
	rules = append(rules, collectAuthorRules(root, vw)...)
	sm := StyleMap{}
	var counter int
	var walk func(n *dom.Node, parent Style)
	walk = func(n *dom.Node, parent Style) {
		if n.Type != dom.Element {
			// Text/Document nodes have no computed style; recurse with parent.
			for _, c := range n.Children {
				walk(c, parent)
			}
			return
		}
		st := computeElement(n, parent, rules, &counter)
		sm[n] = &st
		for _, c := range n.Children {
			walk(c, st)
		}
	}
	// The synthetic Document root has no style; seed children with initial.
	walk(root, initialStyle())
	return sm
}

// computeElement resolves the style of a single element.
func computeElement(n *dom.Node, parent Style, rules []Rule, counter *int) Style {
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
