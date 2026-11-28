// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !webengine_nonet

package engine

import (
	"context"
	"image"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-webengine/engine/dom"
)

// TestIsBotChallenge covers the interstitial detector's narrow gate.
func TestIsBotChallenge(t *testing.T) {
	cases := []struct {
		name   string
		status int
		hdr    http.Header
		body   string
		want   bool
	}{
		{"403 with marker", 403, nil, "Performing security verification", true},
		{"503 just a moment", 503, nil, "<title>Just a moment...</title>", true},
		{"403 cf-mitigated header", 403, http.Header{"Cf-Mitigated": {"challenge"}}, "whatever", true},
		{"200 mentioning verification is NOT a challenge", 200, nil, "our security verification process", false},
		{"403 without any marker", 403, nil, "plain forbidden", false},
		{"404 with marker text", 404, nil, "just a moment", false},
	}
	for _, c := range cases {
		if got := isBotChallenge(c.status, c.hdr, []byte(c.body)); got != c.want {
			t.Errorf("%s: isBotChallenge = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestCrossOriginIsolatedFromHeaders covers the HTML Standard's own
// COOP/COEP rule directly: both headers must be present with an accepted
// value, case-insensitively, or isolation is denied — confirmed as the
// correct rule (not a process-model shortcut) by go-wasmcontainers, who use
// this engine as a test lane beside real browsers and need a page missing
// these headers to see SAB denied here exactly as it would be there.
func TestCrossOriginIsolatedFromHeaders(t *testing.T) {
	h := func(coop, coep string) http.Header {
		hdr := http.Header{}
		if coop != "" {
			hdr.Set("Cross-Origin-Opener-Policy", coop)
		}
		if coep != "" {
			hdr.Set("Cross-Origin-Embedder-Policy", coep)
		}
		return hdr
	}
	cases := []struct {
		name string
		hdr  http.Header
		want bool
	}{
		{"both correct", h("same-origin", "require-corp"), true},
		{"credentialless also accepted", h("same-origin", "credentialless"), true},
		{"case-insensitive", h("Same-Origin", "Require-Corp"), true},
		{"missing COEP", h("same-origin", ""), false},
		{"missing COOP", h("", "require-corp"), false},
		{"neither header", h("", ""), false},
		{"nil headers", nil, false},
		{"wrong COOP value", h("unsafe-none", "require-corp"), false},
		{"wrong COEP value", h("same-origin", "unsafe-none"), false},
	}
	for _, c := range cases {
		if got := crossOriginIsolatedFromHeaders(c.hdr); got != c.want {
			t.Errorf("%s: crossOriginIsolatedFromHeaders = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestFetchCrossOriginIsolatedEndToEnd proves the real header path end to
// end: a Document fetched from a response that actually carries COOP/COEP
// reports CrossOriginIsolated, one from an otherwise-identical response
// missing them does not — and that flows through RenderDocument into a real
// page's own window.crossOriginIsolated and SharedArrayBuffer availability,
// with no Engine-level override involved.
func TestFetchCrossOriginIsolatedEndToEnd(t *testing.T) {
	isolated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
		w.Write([]byte(`<html><head><title>t</title></head><body><script>
			document.body.setAttribute('data-coi', String(crossOriginIsolated));
			document.body.setAttribute('data-sab', typeof SharedArrayBuffer);
		</script></body></html>`))
	}))
	defer isolated.Close()
	notIsolated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><head><title>t</title></head><body><script>
			document.body.setAttribute('data-coi', String(crossOriginIsolated));
			document.body.setAttribute('data-sab', typeof SharedArrayBuffer);
		</script></body></html>`))
	}))
	defer notIsolated.Close()

	e := New()
	vp := image.Rect(0, 0, 200, 100)

	isoDoc, err := e.Fetch(context.Background(), isolated.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !isoDoc.CrossOriginIsolated {
		t.Fatal("Document.CrossOriginIsolated should be true for a response with correct COOP/COEP headers")
	}
	if _, _, err := e.RenderDocument(context.Background(), isoDoc, vp); err != nil {
		t.Fatal(err)
	}
	body := dom.Find(isoDoc.Root, "body")
	if got, _ := body.Attribute("data-coi"); got != "true" {
		t.Fatalf("isolated page: window.crossOriginIsolated = %q, want %q", got, "true")
	}
	if got, _ := body.Attribute("data-sab"); got != "function" {
		t.Fatalf("isolated page: typeof SharedArrayBuffer = %q, want %q", got, "function")
	}

	plainDoc, err := e.Fetch(context.Background(), notIsolated.URL)
	if err != nil {
		t.Fatal(err)
	}
	if plainDoc.CrossOriginIsolated {
		t.Fatal("Document.CrossOriginIsolated should be false for a response missing COOP/COEP headers")
	}
	if _, _, err := e.RenderDocument(context.Background(), plainDoc, vp); err != nil {
		t.Fatal(err)
	}
	body2 := dom.Find(plainDoc.Root, "body")
	if got, _ := body2.Attribute("data-coi"); got != "false" {
		t.Fatalf("non-isolated page: window.crossOriginIsolated = %q, want %q", got, "false")
	}
	if got, _ := body2.Attribute("data-sab"); got != "undefined" {
		t.Fatalf("non-isolated page: typeof SharedArrayBuffer = %q, want %q (real browsers hide it entirely)", got, "undefined")
	}
}

// TestGetRetriesOnBotChallenge covers the fetch fallback: a server that
// challenges the browser-like primary client but serves the real page to the
// plain fallback client yields the real content.
func TestGetRetriesOnBotChallenge(t *testing.T) {
	const real = `<html><head><title>Real</title></head><body>real content</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "BrowserUA" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<html><body>Performing security verification. Browser not supported.</body></html>`))
			return
		}
		_, _ = w.Write([]byte(real))
	}))
	defer srv.Close()

	e := &Engine{Client: &http.Client{}, UserAgent: "BrowserUA"}
	body, _, _, _, err := e.get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "real content") {
		t.Errorf("get returned %q, want the real content via the fallback client", body)
	}
}

// TestGetNoRetryOnNormalPage covers the common path: a normal 200 response is
// returned as-is, with no fallback request.
func TestGetNoRetryOnNormalPage(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`<html><body>ok</body></html>`))
	}))
	defer srv.Close()

	e := &Engine{Client: &http.Client{}, UserAgent: "BrowserUA"}
	body, _, _, _, err := e.get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "ok") {
		t.Errorf("body = %q, want ok", body)
	}
	if hits != 1 {
		t.Errorf("server hit %d times, want 1 (no fallback retry on a normal page)", hits)
	}
}

// TestGetKeepsChallengeWhenFallbackAlsoBlocked covers the case where the
// fallback is itself challenged: the original response is returned (best effort)
// rather than an error.
func TestGetKeepsChallengeWhenFallbackAlsoBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<html><body>Just a moment...</body></html>`))
	}))
	defer srv.Close()

	e := &Engine{Client: &http.Client{}, UserAgent: "BrowserUA"}
	body, _, _, _, err := e.get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Just a moment") {
		t.Errorf("body = %q, want the original challenge (fallback also blocked)", body)
	}
}

// TestGetRequestError covers the transport-error path (an unroutable URL).
func TestGetRequestError(t *testing.T) {
	e := New()
	if _, _, _, _, err := e.get(context.Background(), "http://127.0.0.1:0"); err == nil {
		t.Error("expected a transport error for an unroutable URL")
	}
}

// TestFetchBytesRequestError covers httpBytes' own transport-error path
// (an unroutable URL): doWithRateLimitRetry returns a non-nil err, and
// httpBytes must hand that back as (nil, false) rather than going on to
// dereference a nil resp.
func TestFetchBytesRequestError(t *testing.T) {
	e := New()
	data, ok := e.fetchBytes(context.Background(), "http://127.0.0.1:0", 1<<20)
	if ok {
		t.Error("expected ok = false for an unroutable URL")
	}
	if data != nil {
		t.Errorf("data = %v, want nil", data)
	}
}

// TestGetRetriesOn429 covers the top-level page GET's own 429 handling
// (net_http.go's doGet, reached through Engine.get → httpGet): before this,
// doGet had no retry logic at all, unlike httpBytes' sub-resource fetch —
// the exact gap go-webengine/engine#450's own FIDELITY.md/README entries
// flagged but deliberately left unfixed. A page fetch that lands on a
// 429-rate-limited window must not hand the rate-limit response body back as
// if it were the real page; it must wait out Retry-After (via the shared
// doWithRateLimitRetry, same as httpBytes) and retry.
func TestGetRetriesOn429(t *testing.T) {
	const real = `<html><head><title>Real</title></head><body>real page content</body></html>`
	var mu sync.Mutex
	var first time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if first.IsZero() {
			first = time.Now()
		}
		if time.Since(first) < time.Second {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(real))
	}))
	defer srv.Close()

	e := &Engine{Client: &http.Client{}, UserAgent: "BrowserUA"}
	start := time.Now()
	body, _, _, _, err := e.get(context.Background(), srv.URL)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "real page content") {
		t.Errorf("get returned %q, want the real page after riding out the 429", body)
	}
	if elapsed < 900*time.Millisecond {
		t.Errorf("retried after %v, want to wait for Retry-After (about 1s)", elapsed)
	}
}

// TestGetGivesUpAfterMaxRetriesStill429 covers the terminal case: a server
// that never stops answering 429 must not retry forever — the caller gets
// back the last (still-429) response rather than hanging or erroring, the
// same contract httpBytes already relies on.
func TestGetGivesUpAfterMaxRetriesStill429(t *testing.T) {
	var hits int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	e := &Engine{Client: &http.Client{}, UserAgent: "BrowserUA"}
	_, _, _, status, err := doGetStatus(e, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d (still rate-limited after exhausting retries)", status, http.StatusTooManyRequests)
	}
	mu.Lock()
	got := hits
	mu.Unlock()
	if want := maxRateLimitRetries + 1; got != want {
		t.Errorf("server hit %d times, want %d (1 initial + %d retries)", got, want, maxRateLimitRetries)
	}
}

// doGetStatus is a small shim so TestGetGivesUpAfterMaxRetriesStill429 can
// see the response's status code, which Engine.get does not expose.
func doGetStatus(e *Engine, rawurl string) (body []byte, final, ctype string, status int, err error) {
	body, final, ctype, status, _, err = doGet(context.Background(), e.Client, rawurl, e.UserAgent)
	return
}
