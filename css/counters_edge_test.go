// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

// Deep nesting: past maxCounters counters on one element, a reset of a new
// name and a counter() of a new name can no longer add one — the bound holds
// and nothing panics; values from a long preceding set still flow through
// (the map path of inherit).
func TestCountersAtTheBound(t *testing.T) {
	depth := maxCounters + 4
	src := `<style>
div { counter-reset: n 1 }
.full { counter-reset: m 5; counter-increment: k 2 }
.full::before { content: "[" counter(m) counter(k) counter(q) "]" }
i { counter-increment: n }
i::before { content: counter(n) }
</style>` + strings.Repeat("<div>", depth) + `<p class="full"></p><i></i><i></i>` + strings.Repeat("</div>", depth)
	got := generated(t, src, "")
	want := []string{"[]", "2", "3"}
	if len(got) != len(want) {
		t.Fatalf("generated %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("generated[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// reversed(): a display:none item and a nested list are not in the scope, an
// explicit increment counts with its own step, and a huge sum is clamped.
func TestReversedCounterScope(t *testing.T) {
	src := `<style>
li::before { content: counter(list-item) }
ol { list-style: none }
.x { counter-reset: reversed(x) }
.x span { counter-increment: x -3 }
.x span::before { content: counter(x) }
.big { counter-reset: reversed(y) }
.big span { counter-increment: y -2147483647 }
.big span::before { content: counter(y) }
</style><body id="b">
<ol reversed><li>a</li><li style="display:none">gone</li><li>b<ol><li>c</li><li>d</li></ol></li></ol>
<div class="x"><span></span><span></span></div>
<div class="big"><span></span><span></span><span></span></div>
</body>`
	wantGenerated(t, "reversed scope", generated(t, src, "b"),
		[]string{"2", "1", "1", "2", "6", "3",
			// clamped to MaxInt32, then each -2147483647 that fits
			"0", "-2147483647", "-2147483647"})
}

// Generated text is built per element, so it is bounded: a long counters()
// separator over nested counters stops at maxGeneratedText, cut on a rune
// boundary.
func TestGeneratedTextBound(t *testing.T) {
	sep := strings.Repeat("é", 150) // 300 bytes, 2-byte runes
	src := `<style>div { counter-reset: n } p::before { content: counters(n, "` + sep + `") "tail" }</style>` +
		strings.Repeat("<div>", 20) + `<p></p>` + strings.Repeat("</div>", 20)
	got := generated(t, src, "")
	if len(got) != 1 {
		t.Fatalf("generated %d", len(got))
	}
	s := got[0]
	if len(s) > maxGeneratedText || len(s) < maxGeneratedText-300 {
		t.Errorf("generated %d bytes, want just under %d", len(s), maxGeneratedText)
	}
	if !utf8.ValidString(s) || strings.Contains(s, "tail") {
		t.Errorf("cut badly: valid=%v tail=%v", utf8.ValidString(s), strings.Contains(s, "tail"))
	}
}

// A reversed counter counting up past the 32-bit floor is clamped too.
func TestReversedCounterClampsLow(t *testing.T) {
	src := `<style>.z { counter-reset: reversed(z) } .z b { counter-increment: z 2147483647 }
.z b::before { content: counter(z) }</style><div class="z" id="z"><b></b><b></b><b></b></div>`
	wantGenerated(t, "reversed low", generated(t, src, "z"), []string{"-1", "2147483646", "2147483646"})
}

// ::marker content forms: a plain string, an empty string (no marker).
func TestMarkerContentForms(t *testing.T) {
	src := `<style>
li.s::marker { content: "→ " }
li.e::marker { content: "" }
li.n::marker { content: normal }
</style><body id="b"><ul><li class="s">a</li><li class="e">b</li><li class="n">c</li></ul></body>`
	wantGenerated(t, "marker content", generated(t, src, "b"), []string{"M:→ ", "M:\u2022 "})
}

// HTML list attributes (rendering §15.3.7): type on ul/ol/li, an invalid
// start or value ignored.
func TestHTMLListAttributes(t *testing.T) {
	src := `<body id="b">
<ul type="square"><li>a</li><li type="CIRCLE">b</li></ul>
<ol type="a"><li>a</li><li type="I">b</li><li type="1">c</li><li type="i">d</li><li type="A">e</li><li type="bogus">f</li></ol>
<ol start="x"><li value="y">a</li></ol>
<ol reversed start="10"><li>a</li></ol>
</body>`
	wantGenerated(t, "html attrs", generated(t, src, "b"), []string{
		"M:\u25aa ", "M:\u25e6 ",
		"M:a. ", "M:II. ", "M:3. ", "M:iv. ", "M:E. ", "M:f. ",
		"M:1. ",
		"M:10. ",
	})
}

func TestListStyleShorthandStrings(t *testing.T) {
	cases := []struct {
		v    string
		typ  ListStyleType
		str  string
		pos  ListStylePosition
		same bool // the declaration is invalid: the inherited disc stays
	}{
		{`"- " inside`, ListString, "- ", ListInside, false},
		{`none "»"`, ListString, "»", ListOutside, false},
		{`square "x"`, 0, "", 0, true},
		{"\"a\nb\"", 0, "", 0, true},
	}
	for _, c := range cases {
		st := styleOf(t, `<ul style='list-style: `+c.v+`'><li>x</li></ul>`, "li")
		if c.same {
			if st.ListStyleType != ListDisc {
				t.Errorf("list-style: %s applied: %v", c.v, st.ListStyleType)
			}
			continue
		}
		if st.ListStyleType != c.typ || st.ListStyleString != c.str || st.ListStylePosition != c.pos {
			t.Errorf("list-style: %s = %v %q %v", c.v, st.ListStyleType, st.ListStyleString, st.ListStylePosition)
		}
	}
	if got := ListStyleTypeName(ListString); got != "none" {
		t.Errorf("ListStyleTypeName(ListString) = %q", got)
	}
	if got := ListStyleTypeName(ListNone); got != "none" {
		t.Errorf("ListStyleTypeName(ListNone) = %q", got)
	}
}

func TestParseCounterEdges(t *testing.T) {
	if _, ok := parseCounterOps("", 0, false); ok {
		t.Error("an empty counter value parsed")
	}
	if _, ok := parseCounterOps("-5", 0, false); ok {
		t.Error("-5 parsed as a counter name")
	}
	for in, want := range map[string]int32{
		"-99999999999":             math.MinInt32,
		"99999999999999999999999":  math.MaxInt32,
		"-99999999999999999999999": math.MinInt32,
		"12":                       12,
	} {
		if n, ok := parseCounterInt(in); !ok || n != want {
			t.Errorf("parseCounterInt(%q) = %d, %v", in, n, ok)
		}
	}
	if _, ok := parseCounterInt("x"); ok {
		t.Error("parseCounterInt accepted x")
	}
	for _, v := range []string{"", `counter(a, "x")`, `counters(a, b)`, `counters(a, "."  "x")`, `counters(a, ".", "x")`,
		strings.Repeat(`"x" `, maxCounterOps+1)} {
		if _, _, ok := parseContent(v); ok {
			t.Errorf("parseContent(%q) accepted", v)
		}
	}
	if got := unquoteAttrValue(`"a"b"`); got != `a"b` {
		t.Errorf("unquoteAttrValue fallback = %q", got)
	}
	if got := unquoteAttrValue(`a\:b`); got != "a:b" {
		t.Errorf("unquoteAttrValue ident = %q", got)
	}
	if got := normalizeEscapes("\"a\nb\\41\""); got != "\"a\nbA\"" {
		t.Errorf("a newline ends a bad string: %q", got)
	}
}
