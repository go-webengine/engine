// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/go-opentype/fonts/cabin"
	"github.com/go-opentype/opentype"
	"github.com/go-webengine/engine/css"
)

// countingSource serves the bundled files and records which were asked for.
type countingSource struct {
	mu    sync.Mutex
	asked []string
	data  func(string) []byte
}

func (c *countingSource) get(name string) ([]byte, error) {
	c.mu.Lock()
	c.asked = append(c.asked, name)
	c.mu.Unlock()
	if b := c.data(name); b != nil {
		return b, nil
	}
	return nil, errors.New("no such file " + name)
}

// A face is fetched the first time it is drawn, once: text its family
// covers never fetches the fallback, a missing glyph does.
func TestBundledFacesOnDemand(t *testing.T) {
	src := &countingSource{data: testFontData}
	defer SetFontSource(fontSource())
	SetFontSource(src.get)
	defer func() { bundled = sync.OnceValue(bundled0) }()
	bundled = sync.OnceValue(bundled0) // fresh lazy faces, not the ones other tests parsed
	f := NewFonts()
	sans := FontFamilyOf(css.GenericSans)
	if src.asked != nil {
		t.Fatalf("NewFonts fetched %v", src.asked)
	}
	w := f.Measure("Hello", sans, 16, 400, false)
	f.Measure("Hello again", sans, 16, 400, false)
	NewFonts().Measure("Hello", sans, 16, 400, false) // another registry shares the face
	if w <= 0 || !slices.Equal(src.asked, []string{fileInter}) {
		t.Fatalf("width %v, fetched %v", w, src.asked)
	}
	f.Measure("☃┼", sans, 16, 400, false) // a snowman and box drawing Inter lacks: DejaVu Sans
	f.Measure("x", FontFamilyOf(css.GenericMono), 16, 700, true)
	if !slices.Equal(src.asked, []string{fileInter, fileDejaVu, fileGoMonoBoldIt}) {
		t.Fatalf("fetched %v", src.asked)
	}
}

func TestBundledFacePanics(t *testing.T) {
	defer SetFontSource(fontSource())
	SetFontSource(func(string) ([]byte, error) { return []byte("not a font"), nil })
	func() {
		defer func() {
			if r := recover(); r == nil || !strings.Contains(fmt.Sprint(r), "inter/inter.ttf failed to parse") {
				t.Errorf("a bundled file that does not parse: %v", r)
			}
		}()
		bundledFace(fileInter).get()
	}()
	SetFontSource(func(string) ([]byte, error) { return nil, errors.New("offline") })
	defer func() {
		if r := recover(); r == nil || !strings.Contains(fmt.Sprint(r), "offline") {
			t.Errorf("a source that fails: %v", r)
		}
	}()
	bundledFace(fileGoMono).get()
}

// A lazily registered face is loaded when its family is first measured, and
// one that fails to load is passed over for the next candidate.
func TestRegisterLazy(t *testing.T) {
	f := NewFonts()
	loads := 0
	if err := f.RegisterLazy("Cabin", 400, false, func() (*opentype.Font, error) {
		loads++
		return opentype.Parse(cabin.TTF)
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.RegisterLazy("Broken", 400, false, func() (*opentype.Font, error) { return nil, errors.New("no") }); err != nil {
		t.Fatal(err)
	}
	if loads != 0 || !f.Registered("cabin") {
		t.Fatalf("loads %d", loads)
	}
	cab := named("cabin", css.GenericSans)
	eager := NewFonts()
	if err := eager.Register("Cabin", 400, false, cabin.TTF); err != nil {
		t.Fatal(err)
	}
	if a, b := f.Measure("Partenaires", cab, 40, 400, false), eager.Measure("Partenaires", cab, 40, 400, false); a != b || loads != 1 {
		t.Fatalf("lazy %v, eager %v, loads %d", a, b, loads)
	}
	f.Measure("Partenaires again", cab, 40, 400, false)
	if loads != 1 {
		t.Fatalf("loaded %d times", loads)
	}
	// the broken face is passed over: the bundled sans measures instead
	broken := named("broken", css.GenericSans)
	if got, want := f.Measure("Partenaires", broken, 40, 400, false), NewFonts().Measure("Partenaires", FontFamilyOf(css.GenericSans), 40, 400, false); got != want {
		t.Fatalf("broken face measured %v, the bundled sans %v", got, want)
	}
	if f.RegisterLazy(" ", 400, false, func() (*opentype.Font, error) { return nil, nil }) == nil || f.RegisterLazy("x", 400, false, nil) == nil {
		t.Error("a lazy face with no name or no loader was registered")
	}
}

// The generic monospace has all four styles of Go Mono (go-webengine/engine#403):
// bold and italic are their own faces, not the regular one, in drawing and in
// what a vector or office consumer embeds.
func TestBundledMonospaceStyles(t *testing.T) {
	f := NewFonts()
	mono := FontFamilyOf(css.GenericMono)
	reg := f.font(styleKey{mono, false, false})
	bold := f.font(styleKey{mono, true, false})
	it := f.font(styleKey{mono, false, true})
	bi := f.font(styleKey{mono, true, true})
	if reg == bold || reg == it || reg == bi || bold == it || bold == bi || it == bi {
		t.Fatal("two monospace styles share one font")
	}
	// Every style is Go Mono: the same fixed advance, so layout does not move.
	w := f.Measure("iiii", mono, 16, 400, false)
	for _, c := range []struct {
		weight int
		italic bool
	}{{700, false}, {400, true}, {700, true}} {
		if got := f.Measure("iiii", mono, 16, c.weight, c.italic); got != w {
			t.Errorf("weight %d italic %v: width %v, regular %v", c.weight, c.italic, got, w)
		}
	}
	// The glyphs differ: a bold "m" carries more ink than the regular one, an
	// italic one is not the upright shape.
	ink := func(weight int, italic bool) (n int, sum int) {
		fc := f.styleFace(mono, 32, weight, italic)
		gid, _ := f.font(styleKey{mono, weight >= 600, italic}).GlyphIndex('m')
		b, m, _, _, ok := fc.GlyphMaskIndex(gid, 0, 0)
		if !ok || m == nil {
			t.Fatal("no glyph")
		}
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				a := int(m.AlphaAt(x-b.Min.X, y-b.Min.Y).A)
				n += a
				sum += a * x
			}
		}
		if n == 0 {
			t.Fatal("empty glyph")
		}
		return n, sum / n
	}
	nr, cr := ink(400, false)
	nb, _ := ink(700, false)
	_, ci := ink(400, true)
	if nb <= nr+nr/10 {
		t.Errorf("bold ink %d not clearly above regular %d", nb, nr)
	}
	if ci == cr {
		t.Error("the italic is the upright glyph")
	}
}
