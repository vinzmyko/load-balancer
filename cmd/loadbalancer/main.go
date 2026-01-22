package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/vinzmyko/load-balancer/internal/circuitbreaker"
	"github.com/vinzmyko/load-balancer/internal/config"
	"github.com/vinzmyko/load-balancer/internal/health"
)

var (
	counter uint64

	// Prometheus metrics
	requestsTotal   *prometheus.CounterVec
	requestDuration *prometheus.HistogramVec
	backendHealthy  *prometheus.GaugeVec
)

// Health checking function handler
func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// Forwards requests to backends
func proxyHandler(proxies []*httputil.ReverseProxy, backends []config.BackendConfig, circuitBreakers []*circuitbreaker.CircuitBreaker, healthChecker *health.Checker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		backend := selectBackend(proxies, circuitBreakers, healthChecker)
		backendURL := backends[backend].URL

		// Increment backend request counter
		requestsTotal.WithLabelValues(backendURL).Inc()

		// Forward request to backend
		proxies[backend].ServeHTTP(w, r)

		duration := time.Since(start).Seconds()
		requestDuration.WithLabelValues(backendURL).Observe(duration) // Add measurement to histogram
	}
}

func main() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "loadbalancer_requests_total",
			Help: "Total number of requests forwarded to each backend",
		},
		[]string{"backend"}, // Label
	)

	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "loadbalancer_request_duration_seconds",
			Help:    "Request duration in seconds",
			Buckets: prometheus.DefBuckets, // Default ranges e.g. [5ms, 10ms ,25ms ,50ms,  100ms, etc.]
		},
		[]string{"backend"},
	)

	backendHealthy = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "loadbalancer_backend_healthy",
			Help: "Backend health status (1 = healthy, 0 = unhealthy)",
		},
		[]string{"backends"},
	)

	prometheus.MustRegister(requestsTotal)
	prometheus.MustRegister(requestDuration)
	prometheus.MustRegister(backendHealthy)

	var proxies []*httputil.ReverseProxy
	circuitBreakers := make([]*circuitbreaker.CircuitBreaker, len(cfg.Backends))

	for i, backend := range cfg.Backends {
		circuitBreakers[i] = circuitbreaker.New(backend.URL, 3, 30*time.Second)

		proxy, err := createProxy(backend.URL, circuitBreakers[i])
		if err != nil {
			log.Fatalf("Failed to create proxy for %s: %v", backend.URL, err)
		}
		proxies = append(proxies, proxy)
	}

	healthChecker := health.NewChecker(len(cfg.Backends))

	for i, backend := range cfg.Backends {
		healthChecker.StartChecking(i, backend.URL, backendHealthy)
	}

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())

	go func() {
		metricsAddr := ":9090"
		log.Printf("Starting metrics server on %s", metricsAddr)
		if err := http.ListenAndServe(metricsAddr, metricsMux); err != nil {
			log.Fatalf("Metrics server failed: %v", err)
		}
	}()

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/", proxyHandler(proxies, cfg.Backends, circuitBreakers, healthChecker))

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	log.Printf("Starting load balancer on %s", addr)

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func createProxy(backendURL string, circuitBreaker *circuitbreaker.CircuitBreaker) (*httputil.ReverseProxy, error) {
	target, err := url.Parse(backendURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse backend server url %s: %w", backendURL, err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)

	// Called on success
	proxy.ModifyResponse = func(resp *http.Response) error {
		circuitBreaker.RecordSuccess()
		return nil
	}

	// Called on errors
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("Proxy error for %s: %v", backendURL, err)
		circuitBreaker.RecordFailure()
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}

	return proxy, nil
}

func selectBackend(backends []*httputil.ReverseProxy, circuitBreakers []*circuitbreaker.CircuitBreaker, healthChecker *health.Checker) int {
	next := atomic.AddUint64(&counter, 1)
	backendCount := len(backends)

	for i := range backendCount {
		idx := int((next + uint64(i)) % uint64(backendCount))

		if !healthChecker.IsHealthy(idx) {
			continue
		}

		if !circuitBreakers[idx].CanAttempt() {
			continue
		}

		return idx
	}

	// All backends unhealthy or circuits open just return the first one
	return int(next % uint64(len(backends)))
}
