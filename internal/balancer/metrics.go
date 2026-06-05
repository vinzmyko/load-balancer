package balancer

// Handles the prometheus metrics creation and getters for prometheus stats.

import "github.com/prometheus/client_golang/prometheus"

// Metrics contain the Prometheus metrics
type Metrics struct {
	requestsTotal       *prometheus.CounterVec   // Counter that only goes up
	backendHealthy      *prometheus.GaugeVec     // Gauge that can only go up and down
	requestDuration     *prometheus.HistogramVec // A bucket with lots of values
	circuitBreakerState *prometheus.GaugeVec
}

func NewMetrics() *Metrics {
	m := &Metrics{
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "loadbalancer_requests_total",
				Help: "Total number of requests forwarded to each backend",
			},
			[]string{"backend", "status_code"},
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
			[]string{"backend"},
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

// CircuitBreakerState returns the circuit breaker state prometheus gauge vec.
func (m *Metrics) CircuitBreakerState() *prometheus.GaugeVec { return m.circuitBreakerState }

// BackendHealthy returns backendHealthy prometheus gauge vec.
func (m *Metrics) BackendHealthy() *prometheus.GaugeVec      { return m.backendHealthy }
