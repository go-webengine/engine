// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-opentype/opentype"
	"github.com/go-webengine/engine/css"
)

// countBuilds swaps the instantiate seam for a counting wrapper.
func countBuilds(t *testing.T) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	old := instantiate
	instantiate = func(f *opentype.Font, c map[string]float64) (*opentype.Font, error) {
		n.Add(1)
		return old(f, c)
	}
	t.Cleanup(func() { instantiate = old })
	return &n
}

func sharedVariableFont(t *testing.T) *opentype.Font {
	t.Helper()
	b, err := os.ReadFile("../testdata/fonts/Outfit-wght.ttf")
	if err != nil {
		t.Fatal(err)
	}
	ft, err := opentype.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return ft
}

// Two Fonts (one per vector page) drawing the same family at the same weight
// build the static instance once (go-webengine/engine#443).
func TestNewFontsShareInstances(t *testing.T) {
	ft := sharedVariableFont(t)
	n := countBuilds(t)
	a, b := NewFonts(), NewFonts()
	for _, f := range []*Fonts{a, b} {
		if err := f.RegisterFont("v", 100, false, ft); err != nil {
			t.Fatal(err)
		}
	}
	style := styleKey{css.FontFamily{Names: "v", Generic: css.GenericSans}, false, false}
	ia, ib := a.glyphFont(style, 700), b.glyphFont(style, 700)
	if ia != ib || ia == ft {
		t.Error("two Fonts did not share one instance")
	}
	if got := n.Load(); got != 1 {
		t.Errorf("instance built %d times, want 1", got)
	}
	// Another weight is another instance; a weight past the axis end shares
	// the clamped one.
	if b.glyphFont(style, 300) == ia {
		t.Error("two weights share one instance")
	}
	if b.glyphFont(style, 1000) != a.glyphFont(style, 900) || n.Load() != 3 {
		t.Errorf("clamped weight not shared (builds %d, want 3)", n.Load())
	}
}

// Concurrent pages wait for one build.
func TestSharedInstancesConcurrent(t *testing.T) {
	ft := sharedVariableFont(t)
	n := countBuilds(t)
	const pages = 16
	got := make([]*opentype.Font, pages)
	var wg sync.WaitGroup
	for i := range pages {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f := NewFonts()
			if err := f.RegisterFont("v", 100, false, ft); err != nil {
				t.Error(err)
				return
			}
			got[i] = f.glyphFont(styleKey{css.FontFamily{Names: "v", Generic: css.GenericSans}, false, false}, 800)
		}()
	}
	wg.Wait()
	for _, g := range got {
		if g != got[0] {
			t.Fatal("concurrent pages got different instances")
		}
	}
	if c := n.Load(); c != 1 {
		t.Errorf("instance built %d times under concurrency, want 1", c)
	}
}

// The cache is bounded, and a failed build is not kept.
func TestSharedInstancesBounded(t *testing.T) {
	ft := sharedVariableFont(t)
	resetSharedInstances()
	t.Cleanup(resetSharedInstances)
	n := countBuilds(t)
	for w := 1; w <= maxSharedInstances+10; w++ {
		if _, err := sharedInstance(ft, 100+w); err != nil {
			t.Fatal(err)
		}
	}
	sharedInstances.mu.Lock()
	size, order := len(sharedInstances.m), len(sharedInstances.order)
	sharedInstances.mu.Unlock()
	if size != maxSharedInstances || order != maxSharedInstances {
		t.Errorf("cache holds %d/%d entries, want %d", size, order, maxSharedInstances)
	}
	before := n.Load()
	if _, err := sharedInstance(ft, 100+1); err != nil || n.Load() != before+1 {
		t.Error("an evicted instance was not rebuilt")
	}
}
