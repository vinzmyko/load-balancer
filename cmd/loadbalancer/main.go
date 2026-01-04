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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/vinzmyko/load-balancer/internal/config"
)

var (
	// Backend
	counter      uint64       // Which backend server to send to
	healthStatus map[int]bool // All the backend server's health status
	healthMutex  sync.RWMutex // Mutex for health related operations

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
					backendHealthy.WithLabelValues(backendURL).Set(1)
				} else {
					log.Printf("Backend %d (%s) is now UNHEALTHY", idx, backendURL)
					backendHealthy.WithLabelValues(backendURL).Set(0)
				}
				healthStatus[idx] = isHealthy
			}
			healthMutex.Unlock()
		}
	}()
}

// Forwards requests to backends
func proxyHandler(proxies []*httputil.ReverseProxy, backends []config.BackendConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		backend := selectBackend(proxies)
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
	http.HandleFunc("/", proxyHandler(proxies, cfg.Backends))

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
