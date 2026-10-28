// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"math"
	"strconv"
	"strings"
)

// CSS counters and list markers, after CSS Lists and Counters Module Level 3
// (https://drafts.csswg.org/css-lists-3/) and CSS Counter Styles Level 3
// (https://www.w3.org/TR/css-counter-styles-3/):
//
//   - counter-reset / counter-increment / counter-set, applied in that order
//     (Lists 3 §4), on elements and pseudo-elements that generate a box —
//     display:none has no effect on counters (§4.5);
//   - counter inheritance from the parent and the preceding sibling, values
//     taken from the element just before in tree order, and instantiation
//     that replaces a counter a previous sibling created (§4.4.1, §4.4.2);
//   - the implicit list-item counter: every list item increments it by 1
//     unless counter-increment names list-item, by -1 in a reversed counter
//     (§4.6); ol/ul reset it, ol[start] and li[value] seed it (Appendix A);
//   - reversed(<name>) with no value takes its initial value from the
//     increments and sets in its scope (§4.4.2's algorithm);
//   - counter() / counters() (§4.7) and the ::marker text: the list-item
//     counter in list-style-type's counter style plus its suffix (§3.4);
//   - counter styles decimal, decimal-leading-zero, lower-/upper-alpha
//     (= -latin), lower-/upper-roman (range 1..3999, decimal outside it),
//     disc, circle, square and none (Counter Styles 3 §6).
//
// Limits (security): counter values are 32-bit; an increment that would
// overflow is not applied, which is what WPT css-lists/counter-reset-
// increment-overflow-underflow expects (2100000000 stays 2100000000). An
// element carries at most maxCounters counters — past that, a reset re-uses
// the innermost counter of that name instead of nesting deeper — and a
// reversed counter's scope scan stops after maxReversedScan elements.

const (
	maxCounters     = 128
	maxReversedScan = 100000
	// maxCounterOps bounds the counter names one declaration may list.
	maxCounterOps = 64
	// maxGeneratedText bounds the text a content with counter()/counters()
	// resolves to on one element: unlike a literal, shared by every element
	// it applies to, that text is built per element, and counters() with a
	// long separator over many nested counters multiplies the stylesheet's
	// size by the page's.
	maxGeneratedText = 4096
)

// CounterOp is one `<counter-name> <integer>?` entry of counter-reset,
// counter-increment or counter-set. Reversed marks a counter-reset
// `reversed(<name>)`; HasValue is false when no integer was given.
type CounterOp struct {
	Name     string
	Value    int32
	HasValue bool
	Reversed bool
}

// ContentKind is the kind of one component of a `content` value.
type ContentKind uint8

const (
	// ContentString is a literal <string>.
	ContentString ContentKind = iota
	// ContentCounter is counter(<name>, <counter-style>?).
	ContentCounter
	// ContentCounters is counters(<name>, <string>, <counter-style>?).
	ContentCounters
)

// ContentItem is one component of a `content` value.
type ContentItem struct {
	Kind ContentKind
	// Text is the literal (ContentString) or the separator (ContentCounters).
	Text string
	// Name is the counter name, Style the counter style ("decimal" when
	// omitted).
	Name, Style string
}

// MarkerInfo is a list item's resolved ::marker (CSS Lists 3 §3).
type MarkerInfo struct {
	// Text is the marker string, suffix included ("1. ", "• ").
	Text string
	// Style is the marker's own style: the list item's, plus any ::marker
	// rule (colour, font).
	Style *Style
	// Inside is list-style-position: inside — the marker is an inline box at
	// the start of the item's content rather than outside it.
	Inside bool
	// Glyph is set when the marker is a disc, circle or square bullet from
	// list-style-type (not a ::marker content), which a painter may draw as
	// a shape instead of the Text glyph.
	Glyph bool
	// Type is the list-style-type the marker came from.
	Type ListStyleType
}

// ListStyleTypeName is the counter-style name of a list-style-type.
func ListStyleTypeName(t ListStyleType) string {
	switch t {
	case ListDisc:
		return "disc"
	case ListCircle:
		return "circle"
	case ListSquare:
		return "square"
	case ListDecimal:
		return "decimal"
	case ListLowerAlpha:
		return "lower-alpha"
	case ListUpperAlpha:
		return "upper-alpha"
	case ListLowerRoman:
		return "lower-roman"
	case ListUpperRoman:
		return "upper-roman"
	case ListDecimalLeadingZero:
		return "decimal-leading-zero"
	}
	return "none"
}

// FormatCounter represents v in the named counter style (CSS Counter Styles
// 3 §6). An unknown style is decimal (§3.4 of Lists 3: "if the specified
// counter style does not exist, decimal is assumed"); a value outside a
// style's range falls back to decimal, the initial fallback.
func FormatCounter(v int32, style string) string {
	switch style {
	case "none":
		return ""
	case "disc":
		return "•"
	case "circle":
		return "◦"
	case "square":
		return "▪"
	case "decimal-leading-zero":
		n := int64(v)
		if n < 0 {
			return "-" + padTwo(-n)
		}
		return padTwo(n)
	case "lower-alpha", "lower-latin":
		if v >= 1 {
			return alphabetic(int64(v), 'a')
		}
	case "upper-alpha", "upper-latin":
		if v >= 1 {
			return alphabetic(int64(v), 'A')
		}
	case "lower-roman":
		if v >= 1 && v <= 3999 {
			return roman(int(v), lowerRoman)
		}
	case "upper-roman":
		if v >= 1 && v <= 3999 {
			return roman(int(v), upperRoman)
		}
	}
	return strconv.FormatInt(int64(v), 10)
}

func padTwo(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) < 2 {
		return "0" + s
	}
	return s
}

// alphabetic is the alphabetic system over 26 symbols: bijective base 26,
// "the first counter symbol is interpreted as the digit 1".
func alphabetic(n int64, first byte) string {
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		n--
		i--
		buf[i] = first + byte(n%26)
		n /= 26
	}
	return string(buf[i:])
}

type romanPair struct {
	w int
	s string
}

var lowerRoman = []romanPair{{1000, "m"}, {900, "cm"}, {500, "d"}, {400, "cd"}, {100, "c"}, {90, "xc"},
	{50, "l"}, {40, "xl"}, {10, "x"}, {9, "ix"}, {5, "v"}, {4, "iv"}, {1, "i"}}

var upperRoman = []romanPair{{1000, "M"}, {900, "CM"}, {500, "D"}, {400, "CD"}, {100, "C"}, {90, "XC"},
	{50, "L"}, {40, "XL"}, {10, "X"}, {9, "IX"}, {5, "V"}, {4, "IV"}, {1, "I"}}

// roman is the additive system with the roman weights; n is in 1..3999.
func roman(n int, pairs []romanPair) string {
	var b strings.Builder
	for _, p := range pairs {
		for n >= p.w {
			b.WriteString(p.s)
			n -= p.w
		}
	}
	return b.String()
}

// markerSuffix is a predefined counter style's suffix: " " for the cyclic
// bullets, ". " (the descriptor's initial value) for the others.
func markerSuffix(t ListStyleType) string {
	if t.Glyph() {
		return " "
	}
	return ". "
}

// parseCounterOps parses a counter-reset / counter-increment / counter-set
// value: `none`, or one or more `<counter-name> <integer>?`, with
// `reversed(<counter-name>)` allowed only when allowReversed (counter-reset).
// An omitted integer is def. Integers are clamped to 32 bits.
func parseCounterOps(v string, def int32, allowReversed bool) ([]CounterOp, bool) {
	fields := splitFieldsEscaped(v)
	if len(fields) == 0 {
		return nil, false
	}
	if len(fields) == 1 && strings.EqualFold(fields[0], "none") {
		return nil, true
	}
	var ops []CounterOp
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		op := CounterOp{Value: def}
		lf := strings.ToLower(f)
		if strings.HasPrefix(lf, "reversed(") && strings.HasSuffix(f, ")") {
			if !allowReversed {
				return nil, false
			}
			op.Reversed = true
			f = strings.TrimSpace(f[len("reversed(") : len(f)-1])
			lf = strings.ToLower(f)
		}
		if !isCounterName(f) {
			return nil, false
		}
		// none, initial, inherit, unset and default are not counter names.
		switch lf {
		case "none", "initial", "inherit", "unset", "default", "revert", "revert-layer":
			return nil, false
		}
		op.Name = unescapeIdent(f)
		if i+1 < len(fields) {
			if n, ok := parseCounterInt(fields[i+1]); ok {
				op.Value, op.HasValue = n, true
				i++
			}
		}
		if op.Reversed && !op.HasValue {
			op.Value = 0
		}
		ops = append(ops, op)
		if len(ops) > maxCounterOps {
			return nil, false
		}
	}
	return ops, true
}

// splitFieldsEscaped splits a value on top-level whitespace, keeping
// parenthesised groups whole and an escape whole — including the one
// whitespace a hex escape ends with (`\31 0` is one identifier, "10").
func splitFieldsEscaped(v string) []string {
	var out []string
	depth, start := 0, -1
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c == '\\' && validEscapeAt(v, i) {
			if start < 0 {
				start = i
			}
			_, _, next := consumeEscape(v, i+1)
			i = next - 1
			continue
		}
		switch {
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case isCSSWhitespace(c) && depth == 0:
			if start >= 0 {
				out = append(out, v[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, v[start:])
	}
	return out
}

// isCounterName reports whether f looks like a <custom-ident>: it must not
// start with a digit (that is an integer) and must not hold a '(' (a
// function, e.g. calc(), which counter values here do not resolve).
func isCounterName(f string) bool {
	if f == "" || strings.ContainsAny(f, "()\"',") {
		return false
	}
	c := f[0]
	if c >= '0' && c <= '9' || c == '+' || c == '.' {
		return false
	}
	if c == '-' && len(f) > 1 && f[1] >= '0' && f[1] <= '9' {
		return false
	}
	return true
}

// parseCounterInt parses an <integer>, clamped to the 32-bit range.
func parseCounterInt(f string) (int32, bool) {
	n, err := strconv.ParseInt(f, 10, 64)
	if err != nil {
		// A run of digits too long for int64 still is an integer; clamp it.
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			if strings.HasPrefix(f, "-") {
				return math.MinInt32, true
			}
			return math.MaxInt32, true
		}
		return 0, false
	}
	if n > math.MaxInt32 {
		n = math.MaxInt32
	} else if n < math.MinInt32 {
		n = math.MinInt32
	}
	return int32(n), true
}

func strconvItoa(n int64) string { return strconv.FormatInt(n, 10) }

// htmlListType maps an ol/li type attribute to its list-style-type (HTML
// Living Standard, rendering §15.3.7 "Lists"): a/A and i/I are
// case-sensitive, the bullet keywords are not.
func htmlListType(t string) string {
	switch strings.ToLower(t) {
	case "none", "disc", "circle", "square":
		return strings.ToLower(t)
	}
	switch t {
	case "1":
		return "decimal"
	case "a":
		return "lower-alpha"
	case "A":
		return "upper-alpha"
	case "i":
		return "lower-roman"
	case "I":
		return "upper-roman"
	}
	return ""
}

// parseContent parses a `content` value made of <string>s, counter() and
// counters(). It returns the concatenated literal when the value holds only
// strings (items nil), or the items when it needs the counters pass. Any
// other component (attr(), url(), open-quote, …) makes the value invalid.
func parseContent(v string) (literal string, items []ContentItem, ok bool) {
	var b strings.Builder
	onlyStrings := true
	for i := 0; i < len(v); {
		c := v[i]
		switch {
		case isCSSWhitespace(c):
			i++
		case c == '"' || c == '\'':
			s, end, ok := ParseString(v[i:])
			if !ok {
				return "", nil, false
			}
			b.WriteString(s)
			items = append(items, ContentItem{Kind: ContentString, Text: s})
			i += end
		default:
			open := strings.IndexByte(v[i:], '(')
			if open <= 0 {
				return "", nil, false
			}
			name := strings.ToLower(strings.TrimSpace(v[i : i+open]))
			end, ok := matchParenQuoted(v, i+open)
			if !ok {
				return "", nil, false
			}
			args := splitArgs(v[i+open+1 : end])
			it, ok := parseCounterFunc(name, args)
			if !ok {
				return "", nil, false
			}
			items = append(items, it)
			onlyStrings = false
			i = end + 1
		}
		if len(items) > maxCounterOps {
			return "", nil, false
		}
	}
	if len(items) == 0 {
		return "", nil, false
	}
	if onlyStrings {
		return b.String(), nil, true
	}
	return "", items, true
}

// parseCounterFunc parses the arguments of counter() or counters().
func parseCounterFunc(name string, args []string) (ContentItem, bool) {
	switch name {
	case "counter":
		if len(args) < 1 || len(args) > 2 || !isCounterName(args[0]) {
			return ContentItem{}, false
		}
		it := ContentItem{Kind: ContentCounter, Name: unescapeIdent(args[0]), Style: "decimal"}
		if len(args) == 2 {
			if !isCounterName(args[1]) {
				return ContentItem{}, false
			}
			it.Style = strings.ToLower(unescapeIdent(args[1]))
		}
		return it, true
	case "counters":
		if len(args) < 2 || len(args) > 3 || !isCounterName(args[0]) {
			return ContentItem{}, false
		}
		sep, end, ok := ParseString(args[1])
		if !ok || end != len(args[1]) {
			return ContentItem{}, false
		}
		it := ContentItem{Kind: ContentCounters, Name: unescapeIdent(args[0]), Text: sep, Style: "decimal"}
		if len(args) == 3 {
			if !isCounterName(args[2]) {
				return ContentItem{}, false
			}
			it.Style = strings.ToLower(unescapeIdent(args[2]))
		}
		return it, true
	}
	return ContentItem{}, false
}

// matchParenQuoted returns the index of the ')' closing the '(' at open,
// skipping strings and escapes (counters(n, ")") is one call).
func matchParenQuoted(s string, open int) (int, bool) {
	depth := 0
	var quote byte
	for i := open; i < len(s); i++ {
		c := s[i]
		if c == '\\' {
			i++
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// splitArgs splits a function's arguments on top-level commas, outside
// strings, trimming each.
func splitArgs(s string) []string {
	var out []string
	var quote byte
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' {
			i++
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case ',':
			out = append(out, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	return append(out, strings.TrimSpace(s[start:]))
}
