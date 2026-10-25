// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build webengine_nonet

package engine

import (
	"context"
	"errors"
	"image"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-browserhttp/browserhttp"
)

// The copy of the User-Agent stays the one the network build sends.
func TestNoNetworkUserAgent(t *testing.T) {
	if defaultUserAgent != browserhttp.DefaultUserAgent || New().UserAgent != browserhttp.DefaultUserAgent {
		t.Fatalf("defaultUserAgent %q, browserhttp %q", defaultUserAgent, browserhttp.DefaultUserAgent)
	}
}

// Without network, a page fetch is refused, and a document's http(s) image
// is never requested, while its data: image still loads.
func TestNoNetworkRefusesRequests(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	e := New()
	if e.Client != nil {
		t.Fatal("a client in a build without network")
	}
	if _, err := e.Fetch(context.Background(), srv.URL); !errors.Is(err, ErrNoNetwork) {
		t.Fatalf("Fetch: %v", err)
	}
	png1x1 := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFBQIAX8jx0gAAAABJRU5ErkJggg=="
	html := `<img src="` + srv.URL + `/a.png" width=10 height=10><img src="` + png1x1 + `" width=10 height=10>`
	if _, _, err := e.RenderHTML(context.Background(), html, srv.URL+"/", image.Rect(0, 0, 100, 100)); err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Fatalf("%d requests reached the server", hits)
	}
	if !strings.Contains(ErrNoNetwork.Error(), "webengine_nonet") {
		t.Fatal(ErrNoNetwork)
	}
}
