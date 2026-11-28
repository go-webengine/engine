// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !webengine_nonet

package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestEngineMaxPerHostConcurrencyResolvesDefault guards e.maxPerHostConcurrency's
// zero-selects-default resolution as a pure function — no semaphore, no
// network — so a mutated guard fails fast instead of only surfacing as a
// hang the first time a zero/negative value reaches semFor's channel
// creation (make(chan struct{}, n) with n<=0 blocks forever on first use).
func TestEngineMaxPerHostConcurrencyResolvesDefault(t *testing.T) {
	cases := []struct {
		name string
		set  int
		want int
	}{
		{"zero selects default", 0, defaultMaxPerHostConcurrency},
		{"negative selects default", -1, defaultMaxPerHostConcurrency},
		{"one is honoured", 1, 1},
		{"above default is honoured", 100, 100},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := &Engine{MaxPerHostConcurrency: c.set}
			if got := e.maxPerHostConcurrency(); got != c.want {
				t.Errorf("maxPerHostConcurrency() with MaxPerHostConcurrency=%d = %d, want %d", c.set, got, c.want)
			}
		})
	}
}

// TestSemForClampsNonPositiveLimit guards semFor's own defense-in-depth clamp
// directly — no network, no real request — so a mutated clamp fails fast
// instead of only surfacing as a hang the first time some OTHER test's real
// request reaches an unbuffered (or negative-capacity) channel.
func TestSemForClampsNonPositiveLimit(t *testing.T) {
	cases := []struct {
		name  string
		limit int
	}{
		{"zero", 0},
		{"negative", -5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := newPerHostLimitedTransport(http.DefaultTransport, func() int { return c.limit })
			sem := tr.semFor("example.com")
			if cap(sem) < 1 {
				t.Errorf("semFor with limit()=%d produced a channel of capacity %d, want >= 1", c.limit, cap(sem))
			}
		})
	}
}

// TestPerHostLimitedTransportBoundsConcurrency guards the actual mechanism:
// no more than the configured limit of requests to one host are ever
// in-flight at the same time, however many are fired at once.
func TestPerHostLimitedTransportBoundsConcurrency(t *testing.T) {
	var current, maxSeen int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&current, 1)
		for {
			seen := atomic.LoadInt32(&maxSeen)
			if n <= seen || atomic.CompareAndSwapInt32(&maxSeen, seen, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond) // hold the slot long enough for overlap to be observable
		atomic.AddInt32(&current, -1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := srv.Client()
	client.Transport = newPerHostLimitedTransport(client.Transport, func() int { return 2 })

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&maxSeen); got > 2 {
		t.Errorf("observed %d concurrent in-flight requests to one host, want <= 2 (the configured limit)", got)
	}
}

// TestPerHostLimitedTransportHostsAreIndependent guards that the cap is truly
// PER-HOST, not a single global limiter mistakenly shared across hosts: two
// different hosts, each capped at 1, must still be able to have a request
// in flight to EACH of them at the same time.
func TestPerHostLimitedTransportHostsAreIndependent(t *testing.T) {
	block := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
		w.WriteHeader(http.StatusOK)
	})
	srvA := httptest.NewServer(handler)
	defer srvA.Close()
	srvB := httptest.NewServer(handler)
	defer srvB.Close()

	client := srvA.Client() // both test servers share the same default transport/cert pool shape
	client.Transport = newPerHostLimitedTransport(client.Transport, func() int { return 1 })

	started := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for _, url := range []string{srvA.URL, srvB.URL} {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, url, nil)
			started <- struct{}{}
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
			}
		}(url)
	}

	// Both requests must have been ABLE to start concurrently — if host B's
	// single request were blocked behind host A's (a shared, not per-host,
	// limiter), only one of these two sends would land before the timeout.
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("a per-host cap on one host blocked a request to a DIFFERENT host")
		}
	}
	close(block)
	wg.Wait()
}

// TestPerHostLimitedTransportRespectsContextCancellation guards that a
// request queued behind a full per-host semaphore aborts promptly when its
// own context is cancelled, rather than hanging until a slot happens to free
// up (or forever, if none ever does).
func TestPerHostLimitedTransportRespectsContextCancellation(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
		w.WriteHeader(http.StatusOK)
	}))
	// Deferred LIFO: close(block) (registered second) must run BEFORE
	// srv.Close() (registered first) — srv.Close() waits for the still-
	// blocked holder handler below to return, which only happens once block
	// closes; reversing this order deadlocks Close() forever.
	defer srv.Close()
	defer close(block)

	client := srv.Client()
	client.Transport = newPerHostLimitedTransport(client.Transport, func() int { return 1 })

	// Occupy the one slot with a request that will hang until block closes.
	holderStarted := make(chan struct{})
	go func() {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		go func() { holderStarted <- struct{}{} }()
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()
	<-holderStarted
	time.Sleep(20 * time.Millisecond) // let the holder actually acquire the slot before queuing behind it

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)

	start := time.Now()
	_, err := client.Do(req)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the queued request to fail once its context expired")
	}
	if elapsed > time.Second {
		t.Errorf("queued request took %v to abort after context cancellation, want well under 1s", elapsed)
	}
}
