package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vinzmyko/load-balancer/internal/config"
)

var (
	counter      uint64
	healthStatus map[int]bool
	healthMutex  sync.RWMutex
)

// Health checking function handler
func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// Performs a single health check for a backend
func checkHealth(backendURL string) bool {
	client := &http.Client{Timeout: 2 * time.Second}

	resp, err := client.Get(backendURL + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == 200
}

// Starts a background health checker for a backend
func startHealthChecker(idx int, backendURL string) {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			<-ticker.C

			isHealthy := checkHealth(backendURL)

			healthMutex.Lock()
			if healthStatus[idx] != isHealthy {
				if isHealthy {
					log.Printf("Backend %d (%s) is now HEALTHY", idx, backendURL)
				} else {
					log.Printf("Backend %d (%s) is now UNHEALTHY", idx, backendURL)
				}
				healthStatus[idx] = isHealthy
			}
			healthMutex.Unlock()
		}
	}()
}

// Forwards requests to backends
func proxyHandler(proxies []*httputil.ReverseProxy) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		backend := selectBackend(proxies)
		proxies[backend].ServeHTTP(w, r)
	}
}

func main() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	var proxies []*httputil.ReverseProxy
	for _, backend := range cfg.Backends {
		proxy, err := createProxy(backend.URL)
		if err != nil {
			log.Fatalf("Failed to create proxy for %s: %v", backend.URL, err)
		}
		proxies = append(proxies, proxy)
	}

	healthStatus = make(map[int]bool)
	for i, backend := range cfg.Backends {
		startHealthChecker(i, backend.URL)
	}

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/", proxyHandler(proxies))

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	log.Printf("Starting load balancer on %s", addr)

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func createProxy(backendURL string) (*httputil.ReverseProxy, error) {
	target, err := url.Parse(backendURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse backend server url %s: %w", backendURL, err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)

	return proxy, nil
}

func selectBackend(backends []*httputil.ReverseProxy) int {
	next := atomic.AddUint64(&counter, 1)
	backendCount := len(backends)

	for i := range backendCount {
		idx := int((next + uint64(i)) % uint64(backendCount))

		healthMutex.RLock()
		isHealthy := healthStatus[idx]
		healthMutex.RUnlock()

		if isHealthy {
			return idx
		}
	}

	return int(next % uint64(len(backends)))
}
