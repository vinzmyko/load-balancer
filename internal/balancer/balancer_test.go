package balancer

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vinzmyko/load-balancer/internal/circuitbreaker"
)

type fakeHealth struct {
	isHealthy func(backendURL string) bool // function field, holds a func value as a field
}

func (f *fakeHealth) IsHealthy(backendURL string) bool {
	if f.isHealthy == nil {
		return true // default to all healthy
	}
	return f.isHealthy(backendURL)
}

// newTestBackend builds a backend for tests, fails if constructor errors
func newTestBackend(t *testing.T, url string, cb *circuitbreaker.CircuitBreaker) *Backend {
	t.Helper()
	b, err := NewBackend(url, cb)
	if err != nil {
		t.Fatalf("createBackend(%q): %v", url, err)
	}
	return b
}

func TestRoundRobinDistribution(t *testing.T) {
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

	lbBackends := make([]*Backend, 3)
	for i := range 3 {
		cb := circuitbreaker.New(fmt.Sprintf(":%d", i), 5, 10*time.Second, nil)
		lbBackends[i] = newTestBackend(t, backends[i].URL, cb)
	}

	hc := &fakeHealth{}
	lb := New(lbBackends, hc, nil)

	numRequests := 300
	for range numRequests {
		backend := lb.selectBackend()

		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()

		backend.Proxy.ServeHTTP(rec, req)
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

	lbBackends := make([]*Backend, 3)
	for i := range 3 {
		cb := circuitbreaker.New(fmt.Sprintf(":%d", i), 5, 10*time.Second, nil)
		lbBackends[i] = newTestBackend(t, backends[i].URL, cb)
	}

	// Healthy for all backends except idx == 1
	hc := &fakeHealth{isHealthy: func(backendURL string) bool { return backendURL != lbBackends[1].URL }}
	lb := New(lbBackends, hc, nil)

	for i := range 3 {
		counts[i].Store(0)
	}

	numRequests := 300
	for range numRequests {
		backend := lb.selectBackend()

		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()

		backend.Proxy.ServeHTTP(rec, req)
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

	lbBackends := make([]*Backend, 2)
	lbBackends[0] = newTestBackend(t, goodBackend.URL, circuitbreaker.New(goodBackend.URL, 3, 10*time.Second, nil))
	lbBackends[1] = newTestBackend(t, badBackend.URL, circuitbreaker.New(badBackend.URL, 3, 10*time.Second, nil))

	hc := &fakeHealth{}
	lb := New(lbBackends, hc, nil)

	// Make requests - bad backend will fail and circuit will open
	for range 20 {
		backend := lb.selectBackend()
		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()
		backend.Proxy.ServeHTTP(rec, req)
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
