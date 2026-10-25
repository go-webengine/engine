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
	if !slices.Equal(src.asked, []string{fileInter, fileDejaVu, fileGoMono}) {
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
