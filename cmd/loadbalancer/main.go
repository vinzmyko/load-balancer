package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"

	"github.com/vinzmyko/load-balancer/internal/circuitbreaker"
	"github.com/vinzmyko/load-balancer/internal/config"
	"github.com/vinzmyko/load-balancer/internal/health"
	"github.com/vinzmyko/load-balancer/internal/telemetry"
)

var (
	counter uint64

	// Prometheus metrics
	requestsTotal       *prometheus.CounterVec   // Counter that only goes up
	backendHealthy      *prometheus.GaugeVec     // Gauge that can only go up and down
	requestDuration     *prometheus.HistogramVec // A bucket with lots of values
	circuitBreakerState *prometheus.GaugeVec
)

type Backend struct {
	URL            string
	Proxy          *httputil.ReverseProxy
	CircuitBreaker *circuitbreaker.CircuitBreaker
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func wrapResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{
		ResponseWriter: w,
		statusCode:     200,
	}
}

// Health checking function handler
func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// Forwards requests to backends
func routeHandler(backends []*Backend, healthChecker *health.Checker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tracer := otel.Tracer("load-balancer")
		ctx, span := tracer.Start(r.Context(), "handle-request")
		defer span.End()
		start := time.Now()

		wrapped := wrapResponseWriter(w)

		backend := selectBackend(backends, healthChecker)
		backendURL := backend.URL

		// Increment backend request counter
		requestsTotal.WithLabelValues(backendURL).Inc()

		_, childSpan := tracer.Start(ctx, "proxy-to-backend")

		// Inject trace context onto request headers before proxy forwards to backend
		otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(r.Header))

		// Forward request to backend
		backend.Proxy.ServeHTTP(wrapped, r)
		childSpan.SetAttributes(
			attribute.String("backend.url", backendURL),
			attribute.Int("http.status_code", wrapped.statusCode),
		)
		childSpan.End()

		// Set the span status
		if wrapped.statusCode >= 500 {
			span.SetStatus(codes.Error, fmt.Sprintf("server error: %d", wrapped.statusCode))
		} else {
			span.SetStatus(codes.Ok, "")
		}

		span.SetAttributes(
			attribute.String("http.method", r.Method),
			attribute.String("http.path", r.URL.Path),
			attribute.String("backend.url", backendURL),
			attribute.Int("http.status_code", wrapped.statusCode),
		)

		duration := time.Since(start).Seconds()
		statusCode := fmt.Sprintf("%d", wrapped.statusCode)
		requestDuration.WithLabelValues(backendURL, statusCode).Observe(duration) // Add measurement to histogram
		spanTraceID := span.SpanContext().TraceID().String()

		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"backend", backendURL,
			"status", wrapped.statusCode,
			"duration_ms", duration*1000,
			"remote_addr", r.RemoteAddr,
			"trace_id", spanTraceID,
		)
	}
}

func main() {
	shutdown := telemetry.InitTracer(context.Background())

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
			Buckets: prometheus.DefBuckets,
		},
		[]string{"backend", "status_code"},
	)

	backendHealthy = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "loadbalancer_backend_healthy",
			Help: "Backend health status (1 = healthy, 0 = unhealthy)",
		},
		[]string{"backends"},
	)

	circuitBreakerState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "loadbalancer_circuit_breaker_state",
			Help: "Circuit breaker state (0 = closed, 1 = open, 2 = half open)",
		},
		[]string{"backend"},
	)

	prometheus.MustRegister(requestsTotal)
	prometheus.MustRegister(requestDuration)
	prometheus.MustRegister(backendHealthy)
	prometheus.MustRegister(circuitBreakerState)

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	backends := make([]*Backend, len(cfg.Backends))

	healthChecker := health.NewChecker(len(cfg.Backends))

	for i, backend := range cfg.Backends {
		cb := circuitbreaker.New(backend.URL, 3, 30*time.Second, func(state circuitbreaker.CircuitState) {
			circuitBreakerState.WithLabelValues(backend.URL).Set(float64(state))
		})
		backends[i] = createProxy(backend.URL, cb)
		healthChecker.StartChecking(i, backend.URL, backendHealthy)
	}

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/", routeHandler(backends, healthChecker))

	server := &http.Server{
		Addr: fmt.Sprintf(":%d", cfg.Server.Port),
	}

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())

	// Start metrics server in background
	go func() {
		metricsAddr := ":9090"
		log.Printf("Starting metrics server on %s", metricsAddr)
		if err := http.ListenAndServe(metricsAddr, metricsMux); err != nil {
			log.Fatalf("Metrics server failed: %v", err)
		}
	}()

	// Start main server in background
	go func() {
		addr := fmt.Sprintf(":%d", cfg.Server.Port)
		log.Printf("Starting load balancer on %s", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Setup signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)

	// Wait for shutdown signal
	sig := <-sigChan
	log.Printf("Received signal %v, shutting down gracefully...", sig)

	healthChecker.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}

	if err := shutdown(ctx); err != nil {
		log.Printf("Tracer shutdown error: %v", err)
	}

	log.Println("Shutdown complete")
}

func createProxy(backendURL string, circuitBreaker *circuitbreaker.CircuitBreaker) *Backend {
	target, err := url.Parse(backendURL)
	if err != nil {
		log.Fatal(fmt.Errorf("failed to parse backend server url %s: %w", backendURL, err))
	}
	proxy := httputil.NewSingleHostReverseProxy(target)

	// Called on success
	proxy.ModifyResponse = func(resp *http.Response) error {
		// Only record success for 2xx and 3xx status codes
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			circuitBreaker.RecordSuccess()
		} else {
			// 4xx and 5xx are failures
			circuitBreaker.RecordFailure()
		}
		return nil
	}

	// Called on errors
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("Proxy error for %s: %v", backendURL, err)
		circuitBreaker.RecordFailure()
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}

	return &Backend{
		URL:            backendURL,
		Proxy:          proxy,
		CircuitBreaker: circuitBreaker,
	}
}

func selectBackend(backends []*Backend, healthChecker *health.Checker) *Backend {
	next := atomic.AddUint64(&counter, 1)
	backendCount := len(backends)

	for i := range backendCount {
		idx := int((next + uint64(i)) % uint64(backendCount))

		if !healthChecker.IsHealthy(idx) {
			continue
		}

		if !backends[idx].CircuitBreaker.CanAttempt() {
			continue
		}

		return backends[idx]
	}

	// All backends unhealthy or circuits open just return the first one
	return backends[int(next%uint64(len(backends)))]
}
