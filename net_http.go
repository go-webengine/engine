// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !webengine_nonet

package engine

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"

	"github.com/go-browserhttp/browserhttp"
)

// defaultUserAgent is the User-Agent New gives an Engine.
const defaultUserAgent = browserhttp.DefaultUserAgent

// newHTTPClient is the client New gives an Engine: browser-like (Chrome TLS
// fingerprint, cookie jar, redirect following), at most
// maxPerHostConcurrency requests in flight per host.
func newHTTPClient() *http.Client {
	client := browserhttp.NewClient(30 * time.Second)
	client.Transport = newPerHostLimitedTransport(client.Transport, maxPerHostConcurrency)
	return client
}

// clientDo sends req through client: every request the engine makes goes
// through here.
func clientDo(client *http.Client, req *http.Request) (*http.Response, error) {
	return client.Do(req)
}

// fallbackUserAgent identifies the engine honestly for the plain-client retry
// used when the browser-mimicking fetch is bounced by a bot interstitial.
const fallbackUserAgent = "go-webengine/1.0 (+https://github.com/go-webengine)"

// plainClient is a stock net/http client (Go's own TLS fingerprint, no cookie
// jar) used only for the bot-challenge fallback: a public page fronted by a
// Cloudflare-style check that challenges the browser-like primary client is
// commonly served in full to a plain, non-browser client (the content curl
// gets). Its timeout matches the browser client's default.
var plainClient = &http.Client{Timeout: 30 * time.Second}

// httpGet performs the HTTP GET of a page, returning body, final URL and
// content-type. If the primary (browser-mimicking) client is served a
// bot-challenge interstitial — a false positive that renders as a "security
// verification / browser not supported" page instead of the real content — it
// retries once with a plain, honestly-identified client, which such checks
// routinely serve the real public page. The retry is used only when it
// actually yields non-challenge content.
func (e *Engine) httpGet(ctx context.Context, rawurl string) ([]byte, string, string, error) {
	body, final, ctype, status, hdr, err := doGet(ctx, e.Client, rawurl, e.UserAgent)
	if err != nil {
		return nil, "", "", err
	}
	if isBotChallenge(status, hdr, body) {
		if b2, f2, c2, s2, h2, e2 := doGet(ctx, plainClient, rawurl, fallbackUserAgent); e2 == nil && s2 < 400 && !isBotChallenge(s2, h2, b2) {
			return b2, f2, c2, nil
		}
	}
	return body, final, ctype, nil
}

// doGet issues one GET through client, returning the (size-limited) body, the
// post-redirect URL, the content-type, the status code and the response headers.
func doGet(ctx context.Context, client *http.Client, rawurl, userAgent string) (body []byte, final, ctype string, status int, hdr http.Header, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return nil, "", "", 0, nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	resp, err := clientDo(client, req)
	if err != nil {
		return nil, "", "", 0, nil, err
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return nil, "", "", 0, nil, err
	}
	final = rawurl
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	return body, final, resp.Header.Get("Content-Type"), resp.StatusCode, resp.Header, nil
}

// botChallengeMarkers are body fragments (lower-case) that identify a
// Cloudflare-style anti-bot interstitial rather than real page content.
var botChallengeMarkers = [][]byte{
	[]byte("performing security verification"),
	[]byte("just a moment"),
	[]byte("cf-browser-verification"),
	[]byte("challenge-platform"),
	[]byte("browser not supported"),
	[]byte("attention required"),
	[]byte("enable javascript and cookies to continue"),
}

// isBotChallenge reports whether a response is an anti-bot interstitial (so the
// caller can retry with a plain client). It is deliberately narrow: only a 403
// or 503 whose headers or body carry a known challenge marker qualifies, so a
// normal 200 page that merely mentions "security verification" in its text is
// never mistaken for one.
func isBotChallenge(status int, hdr http.Header, body []byte) bool {
	if status != http.StatusForbidden && status != http.StatusServiceUnavailable {
		return false
	}
	if hdr != nil && hdr.Get("Cf-Mitigated") != "" {
		return true
	}
	lb := bytes.ToLower(body)
	for _, m := range botChallengeMarkers {
		if bytes.Contains(lb, m) {
			return true
		}
	}
	return false
}

// httpBytes fetches one sub-resource (an image, a font) through the client:
// its body when the server answers 200, at most limit bytes.
func (e *Engine) httpBytes(ctx context.Context, abs string, limit int64) ([]byte, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, abs, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", e.UserAgent)
	resp, ok := e.doWithRateLimitRetry(ctx, req)
	if !ok {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, false
	}
	return data, true
}

// doWithRateLimitRetry runs req via e.Client, retrying up to
// maxRateLimitRetries times when the response is 429 Too Many Requests —
// honouring the server's own Retry-After (delay-seconds form) when present,
// falling back to defaultRateLimitBackoff otherwise. Any other status (or
// response, including a real success) is returned immediately on the first
// attempt: only a 429 triggers a wait-and-retry. req's body is nil (every
// caller here is a bodyless GET), so the same *http.Request is safe to reuse
// across attempts. ok is false only for a request-level error (network
// failure, context cancelled) or exhausting all retries still 429 — never
// for an ordinary non-2xx status, which the caller already handles.
func (e *Engine) doWithRateLimitRetry(ctx context.Context, req *http.Request) (resp *http.Response, ok bool) {
	for attempt := 0; ; attempt++ {
		resp, err := clientDo(e.Client, req)
		if err != nil {
			return nil, false
		}
		if resp.StatusCode != http.StatusTooManyRequests || attempt >= maxRateLimitRetries {
			return resp, true
		}
		wait := retryAfterDelay(resp.Header.Get("Retry-After"))
		resp.Body.Close()
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, false
		}
	}
}
