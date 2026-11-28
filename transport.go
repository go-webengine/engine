// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !webengine_nonet

package engine

import (
	"net/http"
	"sync"
)

// defaultMaxPerHostConcurrency bounds concurrent in-flight requests to any
// single host — independent of, and complementary to, imgWorkers' own
// overall fetch-concurrency bound (images.go), which has no per-host
// awareness at all. Confirmed necessary live (round 57): this engine's own
// concurrent per-page image fetching can fire more truly-simultaneous
// requests at one CDN host (upload.wikimedia.org, serving many thumbnails on
// a single Wikipedia article) than that host's own rate limiter tolerates,
// tripping 429s that round 57's Retry-After-honouring retry then has to pay
// for with real added latency. This cap reduces how often the limit is hit
// in the first place, rather than only recovering after it is — the two are
// complementary, not alternatives: even a capped page can occasionally
// exceed a host's own (possibly much stricter, or momentarily tightened)
// limit, so the retry stays.
//
// Round 57 shipped 2 for this constant, measured only against
// upload.wikimedia.org (re-fetching en.wikipedia.org's own infobox-heavy
// article showed 10 429s at a cap of 6, 3 at a cap of 2, and still 3 — no
// further improvement — at a cap of 1): that page's own wall-clock time is
// JS-bound, not fetch-bound (round 58), so a higher cap could never have
// shown up as a win there, and round 57 had no field to let a caller raise
// it for a DIFFERENT, fetch-bound page even if one had asked. Round 152
// found exactly that page shape on tailwindcss.com/ (every font and image
// on one origin) and measured raising the cap to 4 saving ~30% of its wall
// time, but round 153 had to first confirm — separately — that
// upload.wikimedia.org's own 429 rate does NOT depend on this cap (eight
// runs at the baseline cap of 2 produced 429s in six of them, 3-5 each —
// the earlier two-run-per-cap comparison that looked like a cap=4
// regression was a sampling artifact, not a real cap effect), before a
// GLOBAL raise could be shipped without risking a regression there; it was
// parked, not merged.
//
// Round 50 finishes it, against the official bench corpus
// (bench/cmd/compare, full 10-page run) rather than a single page: raising
// the shipped cap=2 to 8 cut go.dev/blog/'s wall time from 5.52s to 3.59s
// (speed× vs Chrome: 0.44→0.58), tailwindcss.com/'s from 4.11s to 2.91s
// (0.46→0.64), and pkg.go.dev/net/http's from 5.50s to 3.17s (0.72→1.23,
// now faster than Chrome) — zero 429 responses observed at either cap on
// any of the three, confirming (as round 153 already found for Wikimedia)
// that this was never a 429-avoidance budget for these pages either. 8 is
// now the default — raised, not replaced, because it does not touch
// upload.wikimedia.org's own measured optimum: Engine.MaxPerHostConcurrency
// (engine.go) lets either page shape be tuned independently of this
// default.
//
// A same-session live re-check of en.wikipedia.org (prompted by one
// official before/after sample showing a collapsed, much-lower-fidelity
// render at cap=2 and a full one at cap=8) found that collapse reproduces
// IDENTICALLY at cap=2 AND cap=8 (three samples each): every sample got a
// 429 on en.wikipedia.org's own top-level page GET, which (unlike
// sub-resource fetches, images.go) has no retry at all. That gap is real
// and reproducible, but independent of this cap's value — tracked
// separately, not fixed by this change.
const defaultMaxPerHostConcurrency = 8

// maxPerHostConcurrency resolves e.MaxPerHostConcurrency: zero (or
// negative) selects defaultMaxPerHostConcurrency, matching
// MaxResponseBytes' own "always bounded, configurable for a larger (or, here,
// a same-origin-heavier) real workload" convention (js/net.go).
func (e *Engine) maxPerHostConcurrency() int {
	if e.MaxPerHostConcurrency <= 0 {
		return defaultMaxPerHostConcurrency
	}
	return e.MaxPerHostConcurrency
}

// perHostLimitedTransport wraps an http.RoundTripper, bounding concurrent
// in-flight requests to any single host (req.URL.Host, which already
// includes a non-default port) to limit via a per-host semaphore created
// lazily the first time each distinct host is seen. It never rejects a
// request outright — one beyond the limit simply waits for a slot to free up,
// or aborts with the request's own context error if that context is
// cancelled first (never a silent indefinite hang). Wrapping happens once,
// in New() — a caller that replaces Engine.Client entirely (every existing
// test that redirects requests to an httptest.Server does exactly this) gets
// no limiting at all, which is correct: a same-process test server has no
// real rate limit to protect.
type perHostLimitedTransport struct {
	rt    http.RoundTripper
	limit func() int

	mu   sync.Mutex
	sems map[string]chan struct{}
}

// newPerHostLimitedTransport wraps rt, bounding concurrent requests to any
// one host to limit() — called fresh each time a new host's semaphore is
// created, never captured once up front, so a caller that sets
// Engine.MaxPerHostConcurrency on the *Engine AFTER New() has already built
// this transport still gets the value it set, not whatever was live at
// construction time. rt must be non-nil; callers needing
// http.DefaultTransport pass it explicitly, matching how http.Client itself
// requires an explicit choice rather than defaulting silently.
func newPerHostLimitedTransport(rt http.RoundTripper, limit func() int) *perHostLimitedTransport {
	return &perHostLimitedTransport{rt: rt, limit: limit, sems: make(map[string]chan struct{})}
}

// semFor returns the semaphore for host, creating it (buffered to t.limit())
// the first time host is seen. Guarded by t.mu only for the map access itself
// — the semaphore channel it returns is then used lock-free, so acquiring or
// releasing a slot never contends across different hosts.
func (t *perHostLimitedTransport) semFor(host string) chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	sem, ok := t.sems[host]
	if !ok {
		sem = make(chan struct{}, t.limit())
		t.sems[host] = sem
	}
	return sem
}

// RoundTrip acquires host's slot before delegating to t.rt, releasing it once
// that call returns (success or error alike) so a slow or failed request
// still frees its slot for the next one. Waiting for a slot honours the
// request's own context, so a cancelled render aborts a queued request
// immediately rather than leaving it blocked on a semaphore no one will ever
// signal.
func (t *perHostLimitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	sem := t.semFor(req.URL.Host)
	select {
	case sem <- struct{}{}:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	defer func() { <-sem }()
	return t.rt.RoundTrip(req)
}
