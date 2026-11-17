// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"sync"

	"github.com/go-opentype/opentype"
)

// maxSharedInstances bounds the process-wide instance cache. An instance is a
// whole re-serialised font, so the bound is small; a deck draws a handful of
// (family, weight) pairs, and the entries beyond it are the oldest ones.
const maxSharedInstances = 64

// instanceKey names a static instance of a variable font: the parsed font
// (identity: the bundled fonts, and a font registered once, are shared across
// every Fonts) and the weight, already clamped to the font's own axis.
type instanceKey struct {
	font *opentype.Font
	wght int
}

// instanceEntry is one cached build; once makes concurrent renders wait for
// a single build instead of each running Instance.
type instanceEntry struct {
	once sync.Once
	font *opentype.Font
	err  error
}

// sharedInstances is the process-wide cache of static instances, shared by
// every Fonts so a vector export (one Fonts per page) builds each instance
// once instead of once per page. A failed build is not kept: the next caller
// retries, as a per-render cache would have.
var sharedInstances = struct {
	mu    sync.Mutex
	m     map[instanceKey]*instanceEntry
	order []instanceKey // insertion order, oldest first
}{m: map[instanceKey]*instanceEntry{}}

// sharedInstance returns the static instance of ft at weight w, building it
// on first use.
func sharedInstance(ft *opentype.Font, w int) (*opentype.Font, error) {
	key := instanceKey{ft, w}
	s := &sharedInstances
	s.mu.Lock()
	e, ok := s.m[key]
	if !ok {
		if len(s.order) >= maxSharedInstances {
			delete(s.m, s.order[0])
			s.order = s.order[1:]
		}
		e = &instanceEntry{}
		s.m[key] = e
		s.order = append(s.order, key)
	}
	s.mu.Unlock()

	e.once.Do(func() {
		e.font, e.err = instantiate(ft, map[string]float64{"wght": float64(w)})
	})
	if e.err != nil {
		s.mu.Lock()
		if s.m[key] == e {
			delete(s.m, key)
			for i, k := range s.order {
				if k == key {
					s.order = append(s.order[:i], s.order[i+1:]...)
					break
				}
			}
		}
		s.mu.Unlock()
		return nil, e.err
	}
	return e.font, nil
}

// resetSharedInstances empties the cache (tests).
func resetSharedInstances() {
	s := &sharedInstances
	s.mu.Lock()
	s.m, s.order = map[instanceKey]*instanceEntry{}, nil
	s.mu.Unlock()
}
