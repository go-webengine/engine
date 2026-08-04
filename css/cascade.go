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

// Cascade computes a style for every element in the tree rooted at root,
// applying the user-agent stylesheet, author rules from every <style> element,
// and inline style="" attributes, with proper specificity and inheritance.
func Cascade(root *dom.Node) StyleMap {
	rules := collectAuthorRules(root)
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

	var cands []candidate
	add := func(decls []Declaration, prec, spec int) {
		for _, d := range decls {
			cands = append(cands, candidate{d, prec, spec, *counter})
			*counter++
		}
	}

	// User-agent defaults.
	add(uaDeclarations(n.Tag), precUA, 0)
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

	// Pass 1: resolve font-size first (its em is relative to the parent).
	for _, c := range cands {
		if c.decl.Property == "font-size" {
			st.apply(c.decl, parent.FontSize)
		}
	}
	// Pass 2: everything else, with em relative to the element's own size.
	for _, c := range cands {
		if c.decl.Property != "font-size" {
			st.apply(c.decl, st.FontSize)
		}
	}
	return st
}

// collectAuthorRules parses every <style> element's text into rules.
func collectAuthorRules(root *dom.Node) []Rule {
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
	return ParseStylesheet(sb.String())
}
