// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"testing"
	"unicode/utf8"

	"github.com/go-webengine/engine/dom"
)

// Golden cases for CSS Syntax 3 §4.3.7 (consume an escaped code point) and
// §4.3.5 (consume a string token), through ParseString.
func TestParseStringGolden(t *testing.T) {
	cases := []struct {
		in, want string
		end      int
		ok       bool
	}{
		{`"abc"`, "abc", 5, true},
		{`'abc' x`, "abc", 5, true},
		{`"\2022\00a0"`, "\u2022\u00a0", 12, true},
		{`"\2022 x"`, "\u2022x", 9, true},            // one whitespace ends the escape and is consumed
		{`"\2022  x"`, "\u2022 x", 10, true},         // only one
		{"\"\\2022\tx\"", "\u2022x", 9, true},        // tab is whitespace
		{"\"\\2022\r\nx\"", "\u2022x", 10, true},     // CRLF counts as one
		{`"\1F600"`, "\U0001F600", 8, true},          // six digits
		{`"\01F6000"`, "\U0001F600" + "0", 10, true}, // a seventh digit is text
		{`"\0"`, "\uFFFD", 4, true},                  // zero
		{`"\D800"`, "\uFFFD", 7, true},               // a surrogate
		{`"\110000"`, "\uFFFD", 9, true},             // past U+10FFFF
		{`"\FFFFFF"`, "\uFFFD", 9, true},
		{`"a\"b"`, `a"b`, 6, true}, // escaped quote
		{`"a\\b"`, `a\b`, 6, true}, // escaped backslash
		{`"\g"`, "g", 4, true},     // a non-hex code point stands for itself
		{`"\é"`, "é", 5, true},
		{"\"a\\\nb\"", "ab", 6, true}, // escaped newline: line continuation
		{"\"a\\\r\nb\"", "ab", 7, true},
		{"\"a\nb\"", "", 2, false}, // an unescaped newline: bad string
		{`"abc`, "abc", 4, true},   // EOF ends the string
		{`"abc\`, "abc", 5, true},  // backslash then EOF: nothing
		{`"\41`, "A", 4, true},     // escape then EOF
		{`x`, "", 0, false},
		{``, "", 0, false},
	}
	for _, c := range cases {
		got, end, ok := ParseString(c.in)
		if got != c.want || end != c.end || ok != c.ok {
			t.Errorf("ParseString(%q) = %q, %d, %v; want %q, %d, %v", c.in, got, end, ok, c.want, c.end, c.ok)
		}
	}
}

func TestConsumeEscapeEOF(t *testing.T) {
	if r, _, next := consumeEscape(`\`, 1); r != utf8.RuneError || next != 1 {
		t.Errorf("EOF escape = %q, %d", r, next)
	}
}

func TestUnescapeIdent(t *testing.T) {
	cases := map[string]string{
		`item`:      "item",
		`\31 0`:     "10",
		`a\:b`:      "a:b",
		`\66 oo`:    "foo",
		`x\` + "\n": "x\\\n", // not a valid escape: kept
		`\000041`:   "A",
		`\0000041`:  "\x041", // six digits max: \000004 then "1"
	}
	for in, want := range cases {
		if got := unescapeIdent(in); got != want {
			t.Errorf("unescapeIdent(%q) = %q, want %q", in, got, want)
		}
	}
}

// normalizeEscapes: the canonical form, golden.
func TestNormalizeEscapesGolden(t *testing.T) {
	cases := map[string]string{
		`"plain"`:           `"plain"`,
		`"\2022\00a0"`:      "\"\u2022\u00a0\"",
		`"\22"`:             `"\""`,
		`"\5c"`:             `"\\"`,
		`'\27'`:             `'\''`,
		`"\31 2"`:           `"\31 2"`, // a digit stays escaped
		`"\3b"`:             `"\;"`,
		`"a\` + "\n" + `b"`: `"ab"`,
		`\64 isc`:           `disc`,
		`\:hover`:           `\:hover`,
		`\g`:                `g`,
		`\0`:                "\uFFFD",
		`"\a"`:              `"\a "`,
		`url(a\29 b)`:       `url(a\)b)`,
		`"tail\`:            `"tail`,
	}
	for in, want := range cases {
		if got := normalizeEscapes(in); got != want {
			t.Errorf("normalizeEscapes(%q) = %q, want %q", in, got, want)
		}
	}
}

// The reported defect, end to end through the cascade: a ::before whose
// content is `"\2022\00a0"` is a bullet and a no-break space, not the
// literal escape text; and the escapes are decoded for every property, not
// only content — a font-family and a counter name too.
func TestEscapesThroughCascade(t *testing.T) {
	src := `<style>
li::before { content: "\2022\00a0" }
li { font-family: "My\20 Font"; counter-reset: \69 tem 4 }
</style><ul><li>a</li></ul>`
	root, err := dom.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	sm := Cascade(root)
	li := sm[dom.Find(root, "li")]
	if li.Before == nil || li.Before.Content != "\u2022\u00a0" {
		t.Fatalf("::before content = %+v", li.Before)
	}
	if len(li.CounterReset) != 1 || li.CounterReset[0].Name != "item" || li.CounterReset[0].Value != 4 {
		t.Errorf("counter-reset with an escaped name = %+v", li.CounterReset)
	}
	if li.FontFamily.Names != "my font" {
		t.Errorf("font-family with an escaped space = %+v", li.FontFamily)
	}
}

// Selectors: a hex escape's trailing space belongs to the escape, not a
// descendant combinator; quoted attribute values decode their escapes.
func TestSelectorHexEscapes(t *testing.T) {
	src := `<style>
.\31 0 { color: red }
[data-x="\41 b"] { color: blue }
#\66 oo { color: lime }
</style><p class="10">a</p><i data-x="Ab">b</i><b id="foo">c</b>`
	root, err := dom.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	sm := Cascade(root)
	if c := sm[dom.Find(root, "p")].Color; c != (Color{R: 255, A: 255}) {
		t.Errorf(".\\31 0 did not match class 10: %+v", c)
	}
	if c := sm[dom.Find(root, "i")].Color; c != (Color{B: 255, A: 255}) {
		t.Errorf(`[data-x="\41 b"] did not match "Ab": %+v`, c)
	}
	if c := sm[dom.Find(root, "b")].Color; c != (Color{G: 255, A: 255}) {
		t.Errorf("#\\66 oo did not match id foo: %+v", c)
	}
}

// A brace or a semicolon inside a string no longer ends a block or a
// declaration.
func TestStringsHideBlockDelimiters(t *testing.T) {
	src := `<style>p::before { content: "}"; color: red } p::after { content: "a;\"b" }</style><p>x</p>`
	root, _ := dom.Parse(src)
	sm := Cascade(root)
	p := sm[dom.Find(root, "p")]
	if p.Before == nil || p.Before.Content != "}" || p.Before.Color != (Color{R: 255, A: 255}) {
		t.Errorf("::before = %+v", p.Before)
	}
	if p.After == nil || p.After.Content != `a;"b` {
		t.Errorf("::after = %+v", p.After)
	}
}

// FuzzNormalizeEscapes: the canonical form is a fixed point, never grows a
// value past a small factor, and ParseString never panics or reads out of
// bounds on it.
func FuzzNormalizeEscapes(f *testing.F) {
	for _, s := range []string{
		`"\2022\00a0"`, `\31 0`, `"a\` + "\n" + `b"`, `\`, `"\`, `'\27'`, `\FFFFFF`, `\0`,
		`counter(\69 tem) ". "`, `"\D800"`, "\"\\\r\n\"", `url(a\29 b)`, `\:hover`, `"unterminated`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n := normalizeEscapes(s)
		if n2 := normalizeEscapes(n); n2 != n {
			t.Fatalf("not a fixed point: %q -> %q -> %q", s, n, n2)
		}
		// Each escape is at least 2 bytes and becomes at most 4 bytes of
		// UTF-8 or a 5-byte hex form: never more than 3x the input.
		if len(n) > 3*len(s)+3 {
			t.Fatalf("grew %d -> %d", len(s), len(n))
		}
		for i := 0; i < len(n); i++ {
			if n[i] == '"' || n[i] == '\'' {
				if _, end, _ := ParseString(n[i:]); end < 0 || end > len(n)-i {
					t.Fatalf("ParseString end %d out of range", end)
				}
			}
		}
		_ = unescapeIdent(s)
		_ = normalizeSelectorEscapes(s)
		_, _, _ = parseContent(n)
		_, _ = parseCounterOps(n, 0, true)
	})
}
