// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"testing"

	"github.com/go-webengine/engine/dom"
)

func styleOf(t *testing.T, htmlSrc, tag string) *Style {
	t.Helper()
	root, err := dom.Parse(htmlSrc)
	if err != nil {
		t.Fatal(err)
	}
	sm := Cascade(root)
	n := dom.Find(root, tag)
	if n == nil {
		t.Fatalf("no <%s>", tag)
	}
	st := sm[n]
	if st == nil {
		t.Fatalf("no style for <%s>", tag)
	}
	return st
}

func TestCascadeUADefaults(t *testing.T) {
	h1 := styleOf(t, `<html><body><h1>x</h1></body></html>`, "h1")
	if h1.Display != DisplayBlock {
		t.Error("h1 not block")
	}
	if h1.FontSize != 32 {
		t.Errorf("h1 font-size = %v", h1.FontSize)
	}
	if !h1.Bold() {
		t.Error("h1 not bold")
	}
	a := styleOf(t, `<html><body><a>x</a></body></html>`, "a")
	if a.Color != (Color{0, 0, 238, 255}) {
		t.Errorf("a color = %v", a.Color)
	}
	if a.Display != DisplayInline {
		t.Error("a should be inline")
	}
	pre := styleOf(t, `<html><body><pre>x</pre></body></html>`, "pre")
	if pre.WhiteSpace != WSPre || pre.FontFamily != Mono {
		t.Errorf("pre = %+v", pre)
	}
	style := styleOf(t, `<html><body><p>x</p><style>p{color:red}</style></body></html>`, "style")
	if style.Display != DisplayNone {
		t.Error("style element should be display:none")
	}
}

func TestCascadeInheritance(t *testing.T) {
	// color and font-size inherit; background does not.
	st := styleOf(t, `<html><body style="color:green;font-size:20px;background-color:red">`+
		`<div><span>hi</span></div></body></html>`, "span")
	if st.Color != (Color{0, 128, 0, 255}) {
		t.Errorf("inherited color = %v", st.Color)
	}
	if st.FontSize != 20 {
		t.Errorf("inherited font-size = %v", st.FontSize)
	}
	if st.Background.A != 0 {
		t.Errorf("background should not inherit, got %v", st.Background)
	}
}

func TestCascadeSpecificityOrdering(t *testing.T) {
	src := `<html><head><style>
		p { color: red }
		.c { color: green }
		#i { color: blue }
	</style></head><body>
		<p id="i" class="c" style="color: black">x</p>
	</body></html>`
	// inline (black) beats id (blue) beats class beats tag.
	if st := styleOf(t, src, "p"); st.Color != (Color{0, 0, 0, 255}) {
		t.Errorf("inline should win, got %v", st.Color)
	}

	// Without inline, id wins.
	src2 := `<html><head><style>
		p { color: red } .c { color: green } #i { color: blue }
	</style></head><body><p id="i" class="c">x</p></body></html>`
	if st := styleOf(t, src2, "p"); st.Color != (Color{0, 0, 255, 255}) {
		t.Errorf("id should win, got %v", st.Color)
	}

	// Equal specificity: later source rule wins.
	src3 := `<html><head><style>.c{color:red}.c{color:green}</style></head>` +
		`<body><p class="c">x</p></body></html>`
	if st := styleOf(t, src3, "p"); st.Color != (Color{0, 128, 0, 255}) {
		t.Errorf("later rule should win, got %v", st.Color)
	}
}

func TestCascadeAuthorOverridesUA(t *testing.T) {
	// Author rule beats UA default (a's blue link colour).
	src := `<html><head><style>a{color:red}</style></head><body><a>x</a></body></html>`
	if st := styleOf(t, src, "a"); st.Color != (Color{255, 0, 0, 255}) {
		t.Errorf("author should override UA, got %v", st.Color)
	}
}

func TestCascadeFontSizeEm(t *testing.T) {
	// font-size:2em on a child is relative to the parent's font-size (20px→40px);
	// a margin in em is relative to the element's own computed font-size.
	src := `<html><body style="font-size:20px">` +
		`<p style="font-size:2em;margin:1em">x</p></body></html>`
	st := styleOf(t, src, "p")
	if st.FontSize != 40 {
		t.Errorf("font-size em = %v want 40", st.FontSize)
	}
	if st.Margin.Top != 40 {
		t.Errorf("margin em = %v want 40 (relative to own 40px)", st.Margin.Top)
	}
}

func TestCascadeSkipsNonElements(t *testing.T) {
	// A document with only text at the root still cascades without panic.
	root, _ := dom.Parse(`plain text`)
	sm := Cascade(root)
	if len(sm) == 0 {
		t.Error("expected some element styles (html/body auto-inserted)")
	}
}
