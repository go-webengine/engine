// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !webengine_nojs

package engine

import (
	"bytes"
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// JSNumberString and JSParseFloat answer as the JavaScript engine does,
// over values a fit computes and the edges of the notations.
func TestJSNumbersMatchGoja(t *testing.T) {
	vm := goja.New()
	str, err := vm.RunString(`(v) => String(v)`)
	if err != nil {
		t.Fatal(err)
	}
	toString, _ := goja.AssertFunction(str)
	pf, err := vm.RunString(`(s) => parseFloat(s)`)
	if err != nil {
		t.Fatal(err)
	}
	parseFloat, _ := goja.AssertFunction(pf)
	r := rand.New(rand.NewPCG(1, 2))
	vals := []float64{0, 1, -1, 1e21, 1e20, 999999999999999900000, 1e-6, 1e-7, 9.999999999999999e-7, 5e-324, math.MaxFloat64}
	for range 20000 {
		switch r.IntN(3) {
		case 0:
			vals = append(vals, r.Float64()*200) // font sizes
		case 1:
			vals = append(vals, math.Float64frombits(r.Uint64()))
		default:
			vals = append(vals, float64(r.IntN(2000))*r.Float64()/float64(1+r.IntN(2000)))
		}
	}
	for _, v := range vals {
		if math.IsNaN(v) {
			continue
		}
		js, err := toString(goja.Undefined(), vm.ToValue(v))
		if err != nil {
			t.Fatal(err)
		}
		got := JSNumberString(v)
		if got != js.String() {
			t.Fatalf("JSNumberString(%v) = %q, JavaScript %q", v, got, js.String())
		}
		for _, s := range []string{got + "px", " " + got, got + "e", got + ".5"} {
			pv, err := parseFloat(goja.Undefined(), vm.ToValue(s))
			if err != nil {
				t.Fatal(err)
			}
			want := pv.ToFloat()
			if g := JSParseFloat(s); g != want && !(math.IsNaN(g) && math.IsNaN(want)) {
				t.Fatalf("JSParseFloat(%q) = %v, JavaScript %v", s, g, want)
			}
		}
	}
}

// FuzzJSParseFloat checks JSParseFloat against the JavaScript engine on
// any string, and that it never panics.
func FuzzJSParseFloat(f *testing.F) {
	// white space JavaScript skips: no-break space, ZWNBSP, line separator
	spaces := string([]rune{0xa0, 0xfeff, 0x2028})
	for _, s := range []string{"64px", " -1.5e-3x", "Infinity", "-Infinityx", ".5", "5.", "e5", "", spaces + "7", "1e400", "0x10", "--1"} {
		f.Add(s)
	}
	vm := goja.New()
	pf, err := vm.RunString(`(s) => parseFloat(s)`)
	if err != nil {
		f.Fatal(err)
	}
	parseFloat, _ := goja.AssertFunction(pf)
	f.Fuzz(func(t *testing.T, s string) {
		got := JSParseFloat(s)
		v, err := parseFloat(goja.Undefined(), vm.ToValue(s))
		if err != nil {
			t.Fatal(err)
		}
		want := v.ToFloat()
		if got != want && !(math.IsNaN(got) && math.IsNaN(want)) {
			t.Fatalf("JSParseFloat(%q) = %v, JavaScript %v", s, got, want)
		}
		_ = JSNumberString(got)
	})
}

// A native Adjust that does what a script does renders what the script
// renders, pixel for pixel: the fitting headings of fitDoc, fitted by
// fitScript in a JavaScript-enabled engine and by nativeFit in one with
// DisableJS.
func TestAdjustMatchesScript(t *testing.T) {
	scripted := New()
	native := New()
	native.DisableJS = true
	native.Adjust = nativeFit
	js := paintHTML(t, scripted, strings.Replace(fitDoc, "</body>", "<script>"+fitScript+"</script></body>", 1))
	gn := paintHTML(t, native, fitDoc)
	if js.Bounds() != gn.Bounds() || !bytes.Equal(js.Pix, gn.Pix) {
		t.Fatal("the native fit is not the script's")
	}
	plain := New()
	plain.DisableJS = true
	if bytes.Equal(paintHTML(t, plain, fitDoc).Pix, js.Pix) {
		t.Fatal("the script changed nothing: the comparison proves nothing")
	}
}
