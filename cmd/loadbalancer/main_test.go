package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vinzmyko/load-balancer/internal/circuitbreaker"
	"github.com/vinzmyko/load-balancer/internal/health"
)

func TestRoundRobinDistribution(t *testing.T) {
	atomic.StoreUint64(&counter, 0)
	// Create counters for each backend
	var counts [3]atomic.Uint64

	backends := make([]*httptest.Server, 3)

	for i := range 3 {
		idx := i

		backends[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Runs when the backend receives a request
			counts[idx].Add(1)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
		}))

		defer backends[i].Close()
	}

	proxies := make([]*httputil.ReverseProxy, 3)
	circuitBreakers := make([]*circuitbreaker.CircuitBreaker, 3)

	for i := range 3 {
		circuitBreakers[i] = circuitbreaker.New(fmt.Sprintf(":%d", i), 5, 10*time.Second)

		proxy, err := createProxy(backends[i].URL, circuitBreakers[i])
		if err != nil {
			t.Fatalf("Failed to create proxy for backend %d: %v", i, err)
		}
		proxies[i] = proxy
	}

	hc := health.NewChecker(3)

	numRequests := 300
	for range numRequests {
		backend := selectBackend(proxies, circuitBreakers, hc)

		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()

		proxies[backend].ServeHTTP(rec, req)
	}

	expected := numRequests / 3
	tolerance := 10

	for i := range 3 {
		got := counts[i].Load()
		t.Logf("Backend %d received %d requests", i, got)

		if got < uint64(expected-tolerance) || got > uint64(expected+tolerance) {
			t.Errorf("Backend %d got %d requests, want ~%d (±%d)",
				i, got, expected, tolerance)
		}
	}
}

func TestHealthCheckFailover(t *testing.T) {
	atomic.StoreUint64(&counter, 0)
	var counts [3]atomic.Uint64

	backends := make([]*httptest.Server, 3)

	for i := range 3 {
		idx := i

		backends[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counts[idx].Add(1)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
		}))

		defer backends[i].Close()
	}

	proxies := make([]*httputil.ReverseProxy, 3)
	circuitBreakers := make([]*circuitbreaker.CircuitBreaker, 3)

	for i := range 3 {
		circuitBreakers[i] = circuitbreaker.New(fmt.Sprintf(":%d", i), 5, 10*time.Second)

		proxy, err := createProxy(backends[i].URL, circuitBreakers[i])
		if err != nil {
			t.Fatalf("Failed to create proxy for backend %d: %v", i, err)
		}
		proxies[i] = proxy
	}

	hc := health.NewChecker(3)
	hc.SetHealthy(1, false)

	for i := range 3 {
		counts[i].Store(0)
	}

	numRequests := 300
	for range numRequests {
		backend := selectBackend(proxies, circuitBreakers, hc)

		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()

		proxies[backend].ServeHTTP(rec, req)
	}

	// Backend 0: should get ~100 requests (1/3 of 300)
	backend0Count := counts[0].Load()
	t.Logf("Backend 0 received %d requests", backend0Count)
	if backend0Count < 90 || backend0Count > 110 {
		t.Errorf("Backend 0 got %d requests, want ~100", backend0Count)
	}

	// Backend 1: should get 0 requests (unhealthy)
	backend1Count := counts[1].Load()
	t.Logf("Backend 1 received %d requests", backend1Count)
	if backend1Count != 0 {
		t.Errorf("Unhealthy backend 1 got %d requests, want 0", backend1Count)
	}

	// Backend 2: should get ~200 requests (2/3 of 300)
	backend2Count := counts[2].Load()
	t.Logf("Backend 2 received %d requests", backend2Count)
	if backend2Count < 190 || backend2Count > 210 {
		t.Errorf("Backend 2 got %d requests, want ~200", backend2Count)
	}
}

func TestCircuitBreakerOpens(t *testing.T) {
	atomic.StoreUint64(&counter, 0)

	var goodCount atomic.Uint64
	var badCount atomic.Uint64

	goodBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer goodBackend.Close()

	badBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer badBackend.Close()

	proxies := make([]*httputil.ReverseProxy, 2)
	circuitBreakers := make([]*circuitbreaker.CircuitBreaker, 2)

	circuitBreakers[0] = circuitbreaker.New(goodBackend.URL, 3, 10*time.Second)
	proxy0, _ := createProxy(goodBackend.URL, circuitBreakers[0])
	proxies[0] = proxy0

	circuitBreakers[1] = circuitbreaker.New(badBackend.URL, 3, 10*time.Second)
	proxy1, _ := createProxy(badBackend.URL, circuitBreakers[1])
	proxies[1] = proxy1

	hc := health.NewChecker(2)

	// Make requests - bad backend will fail and circuit will open
	for range 20 {
		backend := selectBackend(proxies, circuitBreakers, hc)
		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()
		proxies[backend].ServeHTTP(rec, req)
	}

	t.Logf("Bad backend received %d requests (circuit should have opened after 3)", badCount.Load())

	// Bad backend should have gotten exactly 3 requests before circuit opened
	if badCount.Load() != 3 {
		t.Errorf("Bad backend got %d requests, want exactly 3 (then circuit opens)", badCount.Load())
	}

	// Good backend should have gotten the rest
	if goodCount.Load() < 15 {
		t.Errorf("Good backend got %d requests, want ≥15", goodCount.Load())
	}
}
