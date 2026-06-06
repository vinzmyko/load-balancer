// Package balancer handles if all the needs of the LoadBalancer struct.
package balancer

// Handles load balancer creation and forwarding requests to backend.

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
)

// healthChecker interface is a field of *health.Checker. Decouples LB from checker.
type healthChecker interface {
	IsHealthy(backendURL string) bool // Needs a method called isHealthy(backendURL) returning a bool
}

type LoadBalancer struct {
	backends []*Backend
	health   healthChecker
	metrics  *Metrics
	counter  atomic.Uint64
}

func New(backends []*Backend, health healthChecker, metrics *Metrics) *LoadBalancer {
	return &LoadBalancer{
		backends: backends,
		health:   health,
		metrics:  metrics,
	}
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
	next := lb.counter.Add(1)
	backendCount := len(lb.backends)

	for i := range backendCount {
		idx := int((next + uint64(i)) % uint64(backendCount))
		b := lb.backends[idx]

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
	return lb.backends[int(next%uint64(len(lb.backends)))]
}
