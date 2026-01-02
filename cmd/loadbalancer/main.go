package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/vinzmyko/load-balancer/internal/config"
)

// Health checking function handler
func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// Forwards requests to backends
func proxyHandler(proxies []*httputil.ReverseProxy) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		proxies[0].ServeHTTP(w, r)
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
