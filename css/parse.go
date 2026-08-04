// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import "strings"

// Declaration is a single property: value pair.
type Declaration struct {
	Property string
	Value    string
}

// Rule is a parsed style rule: a list of selectors sharing a declaration block.
type Rule struct {
	Selectors    []Selector
	Declarations []Declaration
}

// stripComments removes /* ... */ comment spans.
func stripComments(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, "/*")
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		j := strings.Index(s[i+2:], "*/")
		if j < 0 {
			break // unterminated comment: drop the rest
		}
		s = s[i+2+j+2:]
	}
	return b.String()
}

// ParseStylesheet parses a full stylesheet into rules. At-rules (@media,
// @font-face, ...) are skipped wholesale — their nested block is consumed and
// ignored. Malformed rules are skipped defensively.
func ParseStylesheet(src string) []Rule {
	src = stripComments(src)
	var rules []Rule
	i := 0
	for i < len(src) {
		// Find the next block opener.
		brace := strings.IndexByte(src[i:], '{')
		if brace < 0 {
			break
		}
		prelude := strings.TrimSpace(src[i : i+brace])
		// Consume the balanced block starting at the brace.
		blockStart := i + brace
		blockEnd, ok := matchBrace(src, blockStart)
		if !ok {
			break
		}
		body := src[blockStart+1 : blockEnd]
		if strings.HasPrefix(prelude, "@") {
			// Skip at-rules entirely (Phase 0 does not honour them).
			i = blockEnd + 1
			continue
		}
		sels := ParseSelectorList(prelude)
		if len(sels) > 0 {
			decls := ParseDeclarations(body)
			if len(decls) > 0 {
				rules = append(rules, Rule{Selectors: sels, Declarations: decls})
			}
		}
		i = blockEnd + 1
	}
	return rules
}

// matchBrace returns the index of the '}' matching the '{' at open.
func matchBrace(s string, open int) (int, bool) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// ParseDeclarations parses a declaration block body ("a: b; c: d") into
// declarations, lowercasing property names and trimming values.
func ParseDeclarations(body string) []Declaration {
	var out []Declaration
	for _, chunk := range strings.Split(body, ";") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		colon := strings.IndexByte(chunk, ':')
		if colon < 0 {
			continue
		}
		prop := strings.ToLower(strings.TrimSpace(chunk[:colon]))
		val := strings.TrimSpace(chunk[colon+1:])
		// Strip a trailing !important marker (its precedence is not modelled).
		if idx := strings.LastIndex(strings.ToLower(val), "!important"); idx >= 0 {
			val = strings.TrimSpace(val[:idx])
		}
		if prop == "" || val == "" {
			continue
		}
		out = append(out, Declaration{Property: prop, Value: val})
	}
	return out
}

// apply mutates s by applying one declaration. Unknown properties and
// unparseable values are ignored. emRef is the font-size used to resolve em
// lengths (the element's own computed font-size).
func (s *Style) apply(d Declaration, emRef float64) {
	v := strings.TrimSpace(d.Value)
	lv := strings.ToLower(v)
	switch d.Property {
	case "display":
		switch lv {
		case "block", "list-item", "flex", "grid":
			s.Display = DisplayBlock // flex/grid degrade to block flow
		case "inline", "inline-block":
			s.Display = DisplayInline
		case "none":
			s.Display = DisplayNone
		}
	case "color":
		if c, ok := parseColor(v); ok {
			s.Color = c
		}
	case "background-color", "background":
		// For "background" we only pick up a bare colour token.
		if c, ok := parseColor(firstToken(v)); ok {
			s.Background = c
		}
	case "font-size":
		if l, ok := parseLength(v, emRef); ok && !l.Auto {
			if l.IsPercent {
				s.FontSize = l.Percent * emRef
			} else {
				s.FontSize = l.Px
			}
		}
	case "font-weight":
		switch lv {
		case "bold", "bolder":
			s.FontWeight = 700
		case "normal", "lighter":
			s.FontWeight = 400
		default:
			if n, ok := atoiClamp(lv); ok {
				s.FontWeight = n
			}
		}
	case "font-family":
		s.FontFamily = parseFontFamily(lv)
	case "text-align":
		switch lv {
		case "left", "start":
			s.TextAlign = AlignLeft
		case "center":
			s.TextAlign = AlignCenter
		case "right", "end":
			s.TextAlign = AlignRight
		}
	case "white-space":
		switch lv {
		case "pre", "pre-wrap", "pre-line":
			s.WhiteSpace = WSPre
		case "normal", "nowrap":
			s.WhiteSpace = WSNormal
		}
	case "width":
		if l, ok := parseLength(v, emRef); ok {
			s.Width = l
		}
	case "margin":
		if e, ok := parseEdges(v, emRef); ok {
			s.Margin = e
		}
	case "margin-top":
		applyEdge(&s.Margin.Top, v, emRef)
	case "margin-right":
		applyEdge(&s.Margin.Right, v, emRef)
	case "margin-bottom":
		applyEdge(&s.Margin.Bottom, v, emRef)
	case "margin-left":
		applyEdge(&s.Margin.Left, v, emRef)
	case "padding":
		if e, ok := parseEdges(v, emRef); ok {
			s.Padding = e
		}
	case "padding-top":
		applyEdge(&s.Padding.Top, v, emRef)
	case "padding-right":
		applyEdge(&s.Padding.Right, v, emRef)
	case "padding-bottom":
		applyEdge(&s.Padding.Bottom, v, emRef)
	case "padding-left":
		applyEdge(&s.Padding.Left, v, emRef)
	}
}

func applyEdge(dst *float64, v string, emRef float64) {
	if l, ok := parseLength(v, emRef); ok && !l.Auto && !l.IsPercent {
		*dst = l.Px
	}
}

// parseEdges parses the 1-to-4 value shorthand for margin/padding. Percentages
// and auto collapse to 0 (Phase 0 does not resolve them for the shorthand).
func parseEdges(v string, emRef float64) (Edges, bool) {
	fields := strings.Fields(v)
	px := make([]float64, 0, len(fields))
	for _, f := range fields {
		l, ok := parseLength(f, emRef)
		if !ok {
			return Edges{}, false
		}
		if l.Auto || l.IsPercent {
			px = append(px, 0)
		} else {
			px = append(px, l.Px)
		}
	}
	switch len(px) {
	case 1:
		return Edges{px[0], px[0], px[0], px[0]}, true
	case 2:
		return Edges{px[0], px[1], px[0], px[1]}, true
	case 3:
		return Edges{px[0], px[1], px[2], px[1]}, true
	case 4:
		return Edges{px[0], px[1], px[2], px[3]}, true
	}
	return Edges{}, false
}

func parseFontFamily(lv string) FontFamily {
	lv = strings.ToLower(lv)
	// Inspect each comma-separated family; the first recognised generic wins.
	for _, fam := range strings.Split(lv, ",") {
		fam = strings.TrimSpace(strings.Trim(strings.TrimSpace(fam), `"'`))
		switch {
		case fam == "monospace" || strings.Contains(fam, "mono") ||
			strings.Contains(fam, "courier") || strings.Contains(fam, "consolas"):
			return Mono
		case fam == "serif" || strings.Contains(fam, "times") ||
			strings.Contains(fam, "georgia") || strings.Contains(fam, "lora"):
			return Serif
		case fam == "sans-serif" || strings.Contains(fam, "sans") ||
			strings.Contains(fam, "arial") || strings.Contains(fam, "helvetica") ||
			strings.Contains(fam, "inter") || strings.Contains(fam, "roboto"):
			return Sans
		}
	}
	return Sans
}

func firstToken(v string) string {
	f := strings.Fields(v)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

func atoiClamp(s string) (int, bool) {
	n := 0
	if s == "" {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}
