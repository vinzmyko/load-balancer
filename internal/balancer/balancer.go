// Package balancer handles if all the needs of the LoadBalancer struct.
package balancer

// Handles load balancer creation and forwarding requests to backend.

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"

	"github.com/vinzmyko/load-balancer/internal/circuitbreaker"
	"github.com/vinzmyko/load-balancer/internal/config"
)

// healthChecker interface is a field of *health.Checker. Decouples LB from checker.
type healthChecker interface {
	IsHealthy(url string) bool // Needs a method called isHealthy(backendURL) returning a bool
	StartChecking(url string)
	StopChecking(url string)
}

// LoadBalancer owns all the backends, health checkers, metrics, counters, and mutexs.
type LoadBalancer struct {
	backends    atomic.Pointer[[]*Backend] // Lock-free
	health      healthChecker
	metrics     *Metrics
	counter     atomic.Uint64
	reconcileMu sync.Mutex // writers only, readers don't touch
}

// New creates and returns a new LoadBalancer struct.
func New(health healthChecker, metrics *Metrics) *LoadBalancer {
	lb := &LoadBalancer{
		health:  health,
		metrics: metrics,
	}
	empty := make([]*Backend, 0)
	lb.backends.Store(&empty) // Init backends with an empty slice as safe default
	return lb
}

// UpdateBackends updates the current backends to the input backends.
func (lb *LoadBalancer) UpdateBackends(configs []config.BackendConfig) {
	// reconcileMu handles writes meaning two UpdateBackends can't run together.
	lb.reconcileMu.Lock()
	defer lb.reconcileMu.Unlock()

	// Create a map for cheap lookups
	current := make(map[string]*Backend)
	if snapshot := lb.backends.Load(); snapshot != nil {
		for _, b := range *snapshot {
			current[b.URL] = b
		}
	}

	// Holds URLs of backends we want to have running after the update.
	desired := make(map[string]struct{}, len(configs)) // struct{] is zero memory, so essentially this is a set
	// New backend list to replace current one.
	newSet := make([]*Backend, 0, len(configs))

	for _, c := range configs {
		// Marks all the backend URLs are desired.
		desired[c.URL] = struct{}{} // Create an empty struct{}.

		// If desired backend is already in backends reuse it.
		if b, ok := current[c.URL]; ok {
			newSet = append(newSet, b)
			continue
		}

		// New backend - wire up its circuit breaker and start health check.
		cb := circuitbreaker.New(c.URL, 3, 30*time.Second, func(state circuitbreaker.CircuitState) {
			if lb.metrics != nil {
				lb.metrics.CircuitBreakerState().WithLabelValues(c.URL).Set(float64(state))
			}
		})

		b, err := NewBackend(c.URL, cb)
		if err != nil {
			slog.Error("skipping backend with invalid URL", "url", c.URL, "error", err)
			continue
		}

		lb.health.StartChecking(c.URL)
		newSet = append(newSet, b)
		slog.Info("Backend added", "url", c.URL)
	}

	// Stop health checks for backends no longer desired.
	for url := range current {
		if _, keep := desired[url]; !keep {
			lb.health.StopChecking(url)
			slog.Info("Backend removed", "url", url)
		}
	}

	// Update lb.backends to the new desired backends.
	lb.backends.Store(&newSet)
}

// RouteHandler forwards requests to backends.
func (lb *LoadBalancer) RouteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tracer := otel.Tracer("load-balancer")
		ctx, span := tracer.Start(r.Context(), "handle-request")
		defer span.End()
		start := time.Now()

		wrapped := wrapResponseWriter(w)

		backend := lb.selectBackend()
		if backend == nil {
			span.SetStatus(codes.Error, "no backends available")
			http.Error(w, "No backends available", http.StatusServiceUnavailable)
			return
		}
		backendURL := backend.URL

		proxyCtx, childSpan := tracer.Start(ctx, "proxy-to-backend")

		// Inject the child span's context onto request headers before proxy forwards to backend
		otel.GetTextMapPropagator().Inject(proxyCtx, propagation.HeaderCarrier(r.Header))

		// Forward to backend with the child span's context attached
		backend.Proxy.ServeHTTP(wrapped, r.WithContext(proxyCtx))

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
		lb.metrics.requestsTotal.WithLabelValues(backendURL, statusCode).Inc()
		lb.metrics.requestDuration.WithLabelValues(backendURL, statusCode).Observe(duration) // Add measurement to histogram
	}
}

// TODO: weighted round-robin. This ignores each backend's configured
// Weight and distributes evenly. Implement weighted selection here, ideally
// behind a Balancer interface so the strategy is swappable.

// selectBackend handles the algorithm in which the balancer decides which backend
// to forward the request to.
func (lb *LoadBalancer) selectBackend() *Backend {
	// Verify current backends exist.
	snapshot := lb.backends.Load()
	if snapshot == nil || len(*snapshot) == 0 {
		return nil
	}
	backends := *snapshot
	backendCount := len(backends)
	next := lb.counter.Add(1)

	for i := range backendCount {
		idx := int((next + uint64(i)) % uint64(backendCount))
		b := backends[idx]

		if !lb.health.IsHealthy(b.URL) {
			continue
		}

		if !b.CircuitBreaker.CanAttempt() {
			continue
		}

		return b
	}

	// Nothing healthy to pick. Return a backend anyway so we don't crash on nil.
	// The request will likely fail, but the circuit breaker records that and the
	// client gets a 502.
	return backends[int(next%uint64(backendCount))]
}
