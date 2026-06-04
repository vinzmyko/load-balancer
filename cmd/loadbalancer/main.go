package main

import (
	"context"
	"fmt"
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

type LoadBalancer struct {
	backends      []*Backend
	healthChecker *health.Checker
	metrics       *Metrics
	counter       atomic.Uint64
}

func newLoadBalancer(backends []*Backend, healthChecker *health.Checker, metrics *Metrics) *LoadBalancer {
	return &LoadBalancer{
		backends:      backends,
		healthChecker: healthChecker,
		metrics:       metrics,
	}
}

func (lb *LoadBalancer) selectBackend() *Backend {
	next := lb.counter.Add(1)
	backendCount := len(lb.backends)

	for i := range backendCount {
		idx := int((next + uint64(i)) % uint64(backendCount))

		if !lb.healthChecker.IsHealthy(idx) {
			continue
		}

		if !lb.backends[idx].CircuitBreaker.CanAttempt() {
			continue
		}

		return lb.backends[idx]
	}

	// All backends unhealthy or circuits open just return the first one
	return lb.backends[int(next%uint64(len(lb.backends)))]
}

// Forwards requests to backends
func (lb *LoadBalancer) routeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tracer := otel.Tracer("load-balancer")
		ctx, span := tracer.Start(r.Context(), "handle-request")
		defer span.End()
		start := time.Now()

		wrapped := wrapResponseWriter(w)

		backend := lb.selectBackend()
		backendURL := backend.URL

		// Increment backend request counter
		lb.metrics.requestsTotal.WithLabelValues(backendURL).Inc()

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
		lb.metrics.requestDuration.WithLabelValues(backendURL, statusCode).Observe(duration) // Add measurement to histogram
	}
}

// Metrics contain the Prometheus metrics
type Metrics struct {
	requestsTotal       *prometheus.CounterVec   // Counter that only goes up
	backendHealthy      *prometheus.GaugeVec     // Gauge that can only go up and down
	requestDuration     *prometheus.HistogramVec // A bucket with lots of values
	circuitBreakerState *prometheus.GaugeVec
}

func newMetrics() *Metrics {
	m := &Metrics{
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "loadbalancer_requests_total",
				Help: "Total number of requests forwarded to each backend",
			},
			[]string{"backend"},
		),
		requestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "loadbalancer_request_duration_seconds",
				Help:    "Request duration in seconds",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"backend", "status_code"},
		),
		backendHealthy: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "loadbalancer_backend_healthy",
				Help: "Backend health status (1 = healthy, 0 = unhealthy)",
			},
			[]string{"backends"},
		),
		circuitBreakerState: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "loadbalancer_circuit_breaker_state",
				Help: "Circuit breaker state (0 = closed, 1 = open, 2 = half open)",
			},
			[]string{"backend"},
		),
	}

	prometheus.MustRegister(
		m.requestsTotal,
		m.requestDuration,
		m.backendHealthy,
		m.circuitBreakerState,
	)

	return m
}

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

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		slog.Error("load balancer exited with error", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	shutdown := telemetry.InitTracer(ctx)

	cfg, err := config.Load("config.yaml")
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	metrics := newMetrics()

	backends := make([]*Backend, len(cfg.Backends))
	healthChecker := health.NewChecker(len(cfg.Backends))

	for i, backend := range cfg.Backends {
		cb := circuitbreaker.New(backend.URL, 3, 30*time.Second, func(state circuitbreaker.CircuitState) {
			metrics.circuitBreakerState.WithLabelValues(backend.URL).Set(float64(state))
		})
		backends[i] = createProxy(backend.URL, cb)
		healthChecker.StartChecking(i, backend.URL, metrics.backendHealthy)
	}

	lb := newLoadBalancer(backends, healthChecker, metrics)

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/", lb.routeHandler())

	server := &http.Server{
		Addr: fmt.Sprintf(":%d", cfg.Server.Port),
	}

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())

	go func() {
		metricsAddr := ":9091"
		slog.Info("Starting metrics", "server", metricsAddr)
		if err := http.ListenAndServe(metricsAddr, metricsMux); err != nil {
			slog.Error("Metric server failed", "server", metricsAddr, "error", err)
		}
	}()

	go func() {
		slog.Info("Starting load balancer", "address", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("Server failed", "error", err)
		}
	}()

	<-ctx.Done()
	slog.Info("Shutting down gracefully")

	healthChecker.Stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("Server shutdown error", "error", err)
	}

	if err := shutdown(shutdownCtx); err != nil {
		slog.Error("Tracer shutdown error", "error", err)
	}

	slog.Info("Shutdown complete")
	return nil
}

func createProxy(backendURL string, circuitBreaker *circuitbreaker.CircuitBreaker) *Backend {
	target, err := url.Parse(backendURL)
	if err != nil {
		slog.Error("Backend server parse error",
			"backend_url", backendURL,
			"error", err,
		)
		os.Exit(1)
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
		slog.Error("Proxy error",
			"backend_url", backendURL,
			"error", err,
		)
		circuitBreaker.RecordFailure()
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}

	return &Backend{
		URL:            backendURL,
		Proxy:          proxy,
		CircuitBreaker: circuitBreaker,
	}
}
