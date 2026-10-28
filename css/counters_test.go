// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/go-webengine/engine/dom"
)

// generated walks a cascaded tree in tree order and returns, for every
// element under the first element with id=scope (the whole document when
// scope is ""), its ::marker text ("M:"-prefixed), its ::before content and
// its ::after content — the generated text a reftest compares.
func generated(t *testing.T, src, scope string) []string {
	t.Helper()
	root, err := dom.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	sm := Cascade(root)
	start := root
	if scope != "" {
		start = findID(root, scope)
		if start == nil {
			t.Fatalf("no #%s", scope)
		}
	}
	var out []string
	var walk func(n *dom.Node)
	walk = func(n *dom.Node) {
		st := sm[n]
		if n.Type == dom.Element && st != nil && st.Display != DisplayNone {
			if st.Marker != nil {
				out = append(out, "M:"+st.Marker.Text)
			}
			if st.Before != nil {
				out = append(out, st.Before.Content)
			}
		}
		for _, c := range n.Children {
			walk(c)
		}
		if n.Type == dom.Element && st != nil && st.Display != DisplayNone && st.After != nil {
			out = append(out, st.After.Content)
		}
	}
	walk(start)
	return out
}

func findID(n *dom.Node, id string) *dom.Node {
	if n.Type == dom.Element {
		if v, ok := n.Attribute("id"); ok && v == id {
			return n
		}
	}
	for _, c := range n.Children {
		if f := findID(c, id); f != nil {
			return f
		}
	}
	return nil
}

func wantGenerated(t *testing.T, name string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got %q\nwant %q", name, got, want)
	}
}

// WPT css/css-lists/counter-001.html: counter-reset on a sibling replaces
// the counter a previous sibling created, and decimal-leading-zero.
func TestWPTCounter001(t *testing.T) {
	src := `<!doctype html><style>
#test { counter-reset: c; }
#test span { counter-increment: c; }
#test span::before { content: counter(c, decimal-leading-zero); }
</style><div id="test">` + strings.Repeat("<span></span>", 12) +
		`<span style="counter-reset: c 98"></span><span></span><span></span></div>`
	wantGenerated(t, "counter-001", generated(t, src, "test"),
		[]string{"01", "02", "03", "04", "05", "06", "07", "08", "09", "10", "11", "12", "99", "13", "14"})
}

// WPT css/css-lists/counters-scope-001.html: a counter is inherited from the
// parent in preference to the one a preceding sibling (or its ::before)
// instantiated.
func TestWPTCountersScope001(t *testing.T) {
	src := `<!doctype html><style>
.scope { counter-reset: c 1; }
.scope::before, .scope::after { content: counter(c); }
.c::before { content: counter(c); }
.one::before { counter-reset: c 2; }
.two { counter-reset: c 3; }
</style><div id="t">
  <span class="scope"><span class="one c"><span class="c"></span></span><span class="c"></span></span>
  <span class="c"></span>
  <span class="scope"><span class="two c"><span class="c"></span></span><span class="c"></span></span>
  <span class="c"></span>
</div>`
	wantGenerated(t, "counters-scope-001", generated(t, src, "t"),
		strings.Fields("1 2 1 1 1 1 1 3 3 1 1 1"))
}

// WPT css/css-lists/counter-set-001.html: counter-set after
// counter-increment, its default 0, the last of repeated names, nesting.
func TestWPTCounterSet001(t *testing.T) {
	src := `<!doctype html><style>span::before { content: counters(n, '.'); }</style><body id="b">
<span style="counter-set: n 7"></span>
<span style="counter-set: n"></span>
<span style="counter-set: n 8 n 7"></span>
<span style="counter-set: n 6; counter-increment: n 2"></span>
<span style="counter-set: n; counter-increment: n 2"></span>
<x style="counter-reset: n 9"><span style="counter-set: n 2"></span></x>
<span style="counter-increment: n"></span>
<x style="counter-reset: n 9">
  <span style="counter-set: n"></span>
  <span style="counter-set: n 2"></span>
  <x style="counter-reset: n 1"><span style="counter-set: n 5"></span></x>
</x>
<span style="counter-increment: n"></span>
</body>`
	wantGenerated(t, "counter-set-001", generated(t, src, "b"),
		[]string{"7", "0", "7", "6", "0", "2", "3", "0", "2", "2.5", "3"})
}

// WPT css/css-lists/counter-reset-increment-set-display-none.html: an
// element with display:none does not touch counters.
func TestWPTCounterDisplayNone(t *testing.T) {
	src := `<!doctype html><style>
  .inc { counter-increment: x }
  .reset-6 { counter-reset: x 6 }
  .reset-666 { counter-reset: x 666 }
  .set-666 { counter-set: x 666 }
  .none { display: none }
  .result::before { content: counter(x) }
</style><div id="t">
  <span class="reset-6"></span>
  <span class="none reset-666 inc"></span>
  <span class="none set-666"></span>
  <span class="inc result"></span>
</div>`
	wantGenerated(t, "display-none", generated(t, src, "t"), []string{"7"})
}

// WPT css/css-lists/counter-reset-increment-overflow-underflow.html: an
// increment past the 32-bit range is not applied.
func TestWPTCounterOverflowUnderflow(t *testing.T) {
	src := `<!doctype html><style>
.overflow { counter-reset: over-counter 2000000000; }
.overflow h3:before { counter-increment: over-counter 50000000; }
.overflow h3::before { content: counter(over-counter); }
.underflow { counter-reset: under-counter -2000000000; }
.underflow h3:before { counter-increment: under-counter -50000000; }
.underflow h3::before { content: counter(under-counter); }
</style><div id="t"><div class="overflow"><h3></h3><h3></h3><h3></h3></div>
<div class="underflow"><h3></h3><h3></h3><h3></h3></div></div>`
	wantGenerated(t, "overflow-underflow", generated(t, src, "t"),
		[]string{"2050000000", "2100000000", "2100000000", "-2050000000", "-2100000000", "-2100000000"})
}

// WPT css/css-lists/li-value-counter-reset-001.html: an author counter-set
// beats li[value]; counter-increment on an item replaces its implicit 1.
func TestWPTLiValueCounterReset001(t *testing.T) {
	src := `<!doctype html><style>li.set { counter-set: list-item 99; }</style><body id="b">
<ol><li>a<li value=4 class=set>b</ol>
<ol><li>a<li value=4 class=set style="counter-increment:list-item 50">b</ol>
<ol><li>a<li value=4 style="counter-increment:list-item 50">b</ol>
<ol><li>a<li class=set style="counter-increment:list-item 50">b</ol>
<ol><li>a<li style="counter-increment:list-item 50">b</ol>
<ol><li>a<li value=4 class=set style="counter-set:list-item 88">b</ol>
</body>`
	var want []string
	for _, v := range []string{"99", "99", "4", "99", "51", "88"} {
		want = append(want, "M:1. ", "M:"+v+". ")
	}
	wantGenerated(t, "li-value-counter-reset-001", generated(t, src, "b"), want)
}

// WPT css/css-lists/counter-list-item.html: counter(list-item) in ::before
// and ::after, with ol[start], li[value], ol[reversed] and ul.
func TestWPTCounterListItem(t *testing.T) {
	src := `<!doctype html><style>
li:before { content: counter(list-item) " "; }
li:after { content: " " counter(list-item); }
ol, ul { list-style: none }
</style><body id="b">
<ol><li>a</li><li>b</li><li>c</li></ol>
<ol start="30"><li>a</li><li>b</li><li>c</li></ol>
<ol><li>a</li><li>b</li><li value="30">c</li><li>d</li><li value="35">e</li><li>f</li></ol>
<ol reversed><li>a</li><li>b</li><li>c</li></ol>
<ol start="30" reversed><li>a</li><li>b</li><li>c</li></ol>
<ol reversed><li>a</li><li>b</li><li value="30">c</li><li>d</li><li value="35">e</li><li>f</li></ol>
<ul><li>a</li><li>b</li><li>c</li></ul>
<ul><li>a</li><li>b</li><li value="30">c</li><li>d</li><li value="35">e</li><li>f</li></ul>
</body>`
	var want []string
	for _, list := range [][]int{{1, 2, 3}, {30, 31, 32}, {1, 2, 30, 31, 35, 36},
		{3, 2, 1}, {30, 29, 28}, {32, 31, 30, 29, 35, 34},
		{1, 2, 3}, {1, 2, 30, 31, 35, 36}} {
		for _, v := range list {
			s := FormatCounter(int32(v), "decimal")
			want = append(want, s+" ", " "+s)
		}
	}
	wantGenerated(t, "counter-list-item", generated(t, src, "b"), want)
}

// The three reported defects, at the cascade: list markers in every list
// style, string escapes in content, counter() in ::before.
func TestMarkersEscapesCountersReported(t *testing.T) {
	src := `<!doctype html><style>
.esc li::before { content: "\2022\00a0" }
ol.c { counter-reset: item; list-style: none }
ol.c > li::before { content: counter(item) ". "; counter-increment: item }
</style><body id="b">
<ul><li>a</li></ul>
<ol start="3"><li>a</li><li>b</li></ol>
<ol style="list-style-type: lower-roman"><li>a</li><li>b</li><li>c</li><li>d</li></ol>
<ol style="list-style-type: upper-alpha" start="26"><li>a</li><li>b</li></ol>
<ul style="list-style-type: square"><li>a</li></ul>
<ul style="list-style-type: '- '"><li>a</li></ul>
<ul style="list-style: none" class="esc"><li>a</li></ul>
<ol class="c"><li>a</li><li>b</li></ol>
</body>`
	want := []string{"M:\u2022 ", "M:3. ", "M:4. ", "M:i. ", "M:ii. ", "M:iii. ", "M:iv. ",
		"M:Z. ", "M:AA. ", "M:\u25aa ", "M:- ", "\u2022\u00a0", "1. ", "2. "}
	wantGenerated(t, "reported", generated(t, src, "b"), want)
}

// Nested lists: counters() joins every list-item counter in scope.
func TestCountersNestedLists(t *testing.T) {
	src := `<!doctype html><style>li::before { content: counters(list-item, ".") ") " } ol { list-style: none }</style>
<body id="b"><ol><li>a<ol><li>b</li><li>c<ol start="5"><li>d</li></ol></li></ol></li><li>e</li></ol></body>`
	wantGenerated(t, "nested", generated(t, src, "b"),
		[]string{"1) ", "1.1) ", "1.2) ", "1.2.5) ", "2) "})
}

// ::marker: content replaces list-style-type, none removes the marker, and
// its colour reaches the marker style.
func TestMarkerPseudoElement(t *testing.T) {
	src := `<!doctype html><style>
li.a::marker { content: "[" counter(list-item, upper-roman) "]"; color: red }
li.b::marker { content: none }
li.c::marker { color: blue }
</style><body id="b"><ol><li class="a">a</li><li class="b">b</li><li class="c">c</li></ol></body>`
	root, _ := dom.Parse(src)
	sm := Cascade(root)
	var lis []*Style
	var walk func(n *dom.Node)
	walk = func(n *dom.Node) {
		if n.Tag == "li" {
			lis = append(lis, sm[n])
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(root)
	if len(lis) != 3 {
		t.Fatalf("%d items", len(lis))
	}
	if m := lis[0].Marker; m == nil || m.Text != "[I]" || m.Style.Color != (Color{R: 255, A: 255}) || m.Glyph {
		t.Errorf("::marker content: %+v", m)
	}
	if lis[1].Marker != nil {
		t.Errorf("content:none kept a marker: %+v", lis[1].Marker)
	}
	if m := lis[2].Marker; m == nil || m.Text != "3. " || m.Style.Color != (Color{B: 255, A: 255}) {
		t.Errorf("::marker colour: %+v", m)
	}
}

func TestListStylePositionInsideMarker(t *testing.T) {
	st := styleOf(t, `<ul style="list-style-position: inside"><li>a</li></ul>`, "li")
	if st.Marker == nil || !st.Marker.Inside || st.Marker.Text != "\u2022 " || !st.Marker.Glyph {
		t.Fatalf("inside marker = %+v", st.Marker)
	}
	if st.Marker.Style.Content != "\u2022 " || st.Marker.Style.WhiteSpace != WSPre {
		t.Errorf("inside marker style: content %q ws %v", st.Marker.Style.Content, st.Marker.Style.WhiteSpace)
	}
}

// Counter Styles 3 §6: each predefined style and its range fallback.
func TestFormatCounter(t *testing.T) {
	cases := []struct {
		v     int32
		style string
		want  string
	}{
		{1, "decimal", "1"}, {-7, "decimal", "-7"}, {0, "decimal", "0"},
		{3, "decimal-leading-zero", "03"}, {12, "decimal-leading-zero", "12"}, {-3, "decimal-leading-zero", "-03"},
		{1, "lower-alpha", "a"}, {26, "lower-alpha", "z"}, {27, "lower-alpha", "aa"}, {702, "lower-alpha", "zz"},
		{703, "lower-latin", "aaa"}, {28, "upper-alpha", "AB"}, {0, "lower-alpha", "0"}, {-1, "upper-alpha", "-1"},
		{math.MaxInt32, "lower-alpha", "fxshrxw"},
		{1, "lower-roman", "i"}, {4, "lower-roman", "iv"}, {1994, "upper-roman", "MCMXCIV"},
		{3999, "upper-roman", "MMMCMXCIX"}, {4000, "upper-roman", "4000"}, {0, "lower-roman", "0"},
		{-5, "upper-roman", "-5"},
		{1, "disc", "\u2022"}, {2, "circle", "\u25e6"}, {3, "square", "\u25aa"},
		{9, "none", ""}, {9, "no-such-style", "9"}, {math.MinInt32, "decimal", "-2147483648"},
		{math.MinInt32, "decimal-leading-zero", "-2147483648"},
	}
	for _, c := range cases {
		if got := FormatCounter(c.v, c.style); got != c.want {
			t.Errorf("FormatCounter(%d, %s) = %q, want %q", c.v, c.style, got, c.want)
		}
	}
}

func TestParseCounterOps(t *testing.T) {
	cases := []struct {
		in   string
		def  int32
		rev  bool
		want []CounterOp
		ok   bool
	}{
		{"none", 0, true, nil, true},
		{"c", 1, false, []CounterOp{{Name: "c", Value: 1}}, true},
		{"c 3 d -2", 0, false, []CounterOp{{Name: "c", Value: 3, HasValue: true}, {Name: "d", Value: -2, HasValue: true}}, true},
		{"reversed(list-item)", 0, true, []CounterOp{{Name: "list-item", Reversed: true}}, true},
		{"reversed(list-item) 4", 0, true, []CounterOp{{Name: "list-item", Value: 4, HasValue: true, Reversed: true}}, true},
		{"reversed(x)", 1, false, nil, false},
		{"c 99999999999", 0, false, []CounterOp{{Name: "c", Value: math.MaxInt32, HasValue: true}}, true},
		{"c -99999999999999999999999", 0, false, []CounterOp{{Name: "c", Value: math.MinInt32, HasValue: true}}, true},
		{"3", 0, false, nil, false},
		{"c none", 0, false, nil, false},
		{"\\31 0x 2", 0, false, []CounterOp{{Name: "10x", Value: 2, HasValue: true}}, true},
		{strings.Repeat("c ", maxCounterOps+1), 0, false, nil, false},
	}
	for _, c := range cases {
		got, ok := parseCounterOps(normalizeEscapes(c.in), c.def, c.rev)
		if ok != c.ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseCounterOps(%q) = %+v, %v; want %+v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseContent(t *testing.T) {
	cases := []struct {
		in    string
		lit   string
		items []ContentItem
		ok    bool
	}{
		{`"a" "b"`, "ab", nil, true},
		{`"\2022\00a0"`, "\u2022\u00a0", nil, true},
		{`counter(item) ". "`, "", []ContentItem{{Kind: ContentCounter, Name: "item", Style: "decimal"}, {Kind: ContentString, Text: ". "}}, true},
		{`counters(x, ".", upper-roman)`, "", []ContentItem{{Kind: ContentCounters, Name: "x", Text: ".", Style: "upper-roman"}}, true},
		{`counters(x, ")")`, "", []ContentItem{{Kind: ContentCounters, Name: "x", Text: ")", Style: "decimal"}}, true},
		{`attr(title)`, "", nil, false},
		{`counter()`, "", nil, false},
		{`counter(3)`, "", nil, false},
		{`counters(x)`, "", nil, false},
		{`counter(x`, "", nil, false},
		{`open-quote`, "", nil, false},
		{"\"a\nb\"", "", nil, false},
	}
	for _, c := range cases {
		lit, items, ok := parseContent(normalizeEscapes(c.in))
		if ok != c.ok || lit != c.lit || !reflect.DeepEqual(items, c.items) {
			t.Errorf("parseContent(%q) = %q %+v %v; want %q %+v %v", c.in, lit, items, ok, c.lit, c.items, c.ok)
		}
	}
}

// FuzzCounters drives the counters pass with arbitrary counter rules over a
// fixed tree of nested lists: it must neither panic nor produce a counters()
// longer than its bound.
func FuzzCounters(f *testing.F) {
	for _, s := range []string{
		`li::before { content: counters(list-item, ".") } ol { counter-reset: reversed(list-item) }`,
		`* { counter-reset: a 2147483647; counter-increment: a 2147483647 } *::after { content: counter(a, upper-roman) }`,
		`li { counter-set: list-item -2147483648 } li::marker { content: counter(list-item, lower-alpha) "\2022" }`,
		`ul { list-style: "\0" inside } li::before { counter-reset: x; content: counters(x, "\"") }`,
	} {
		f.Add(s)
	}
	const tree = `<ol start="2147483647"><li value="-5">a<ol reversed><li>b</li><li value="9">c<ul><li>d</li></ul></li></ol></li>` +
		`<li style="display:none">e</li><li>f</li></ol><ol reversed start="-2147483648"><li>g</li></ol>`
	f.Fuzz(func(t *testing.T, sheet string) {
		root, err := dom.Parse("<style>" + sheet + "</style>" + tree)
		if err != nil {
			return
		}
		sm := Cascade(root)
		for _, st := range sm {
			for _, ps := range []*Style{st.Before, st.After} {
				if ps != nil && ps.ContentItems != nil && len(ps.Content) > maxGeneratedText {
					t.Fatalf("content of %d bytes", len(ps.Content))
				}
			}
		}
	})
}

// Bounds: deeply nested counter-resets stop nesting at maxCounters, and a
// pathological reversed scan is bounded — neither may blow up.
func TestCounterNestingBound(t *testing.T) {
	depth := maxCounters * 3
	src := `<style>div { counter-reset: n 1 } div::before { content: counters(n, ".") }</style>` +
		strings.Repeat("<div>", depth) + strings.Repeat("</div>", depth)
	got := generated(t, src, "")
	if len(got) == 0 {
		t.Fatal("no generated content")
	}
	deepest := got[len(got)-1]
	if n := strings.Count(deepest, ".") + 1; n > maxCounters {
		t.Errorf("counters() joined %d counters, want at most %d", n, maxCounters)
	}
}
