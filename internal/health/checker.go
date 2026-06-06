// Package health handles the health check for a backend server.
package health

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Checker manages health checking for multiple backends, keyed by URL.
type Checker struct {
	healthStatus map[string]bool          // backend URL -> is healthy?
	stopChans    map[string]chan struct{} // backend URL -> stop signal/goroutine
	healthMutex  sync.RWMutex             // Mutex guard for both maps
	gauge        *prometheus.GaugeVec
}

// NewChecker creates a health checker. Gauge is updated on health transmissions.
func NewChecker(gauge *prometheus.GaugeVec) *Checker {
	return &Checker{
		healthStatus: make(map[string]bool),
		stopChans:    make(map[string]chan struct{}),
		gauge:        gauge,
	}
}

// StartChecking starts a background health checker for a backend. It is idempotent.
// Spins up a goroutine that runs in a loop, pinging the backend to see if it's alive.
func (hc *Checker) StartChecking(backendURL string) {
	hc.healthMutex.Lock()
	// Guard clause if health checker goroutine already existing
	if _, exists := hc.stopChans[backendURL]; exists {
		hc.healthMutex.Unlock()
		return
	}

	// Create and wire up the health checker
	stopChan := make(chan struct{})
	hc.stopChans[backendURL] = stopChan
	hc.healthStatus[backendURL] = true // assume healthy until a probe fails
	hc.healthMutex.Unlock()

	if hc.gauge != nil {
		hc.gauge.WithLabelValues(backendURL).Set(1)
	}

	// Create the health checker goroutine
	go func() {
		// Create heartbeat ticker that receives a value every 10 seconds
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			// Everytime a heartbeat comes in we check the health and update the value.
			case <-ticker.C:
				isHealthy := checkHealth(backendURL)

				hc.healthMutex.Lock()
				if hc.healthStatus[backendURL] != isHealthy { // If value has changed
					if isHealthy {
						slog.Info("Backend is now HEALTHY", "backend_url", backendURL)
						if hc.gauge != nil {
							hc.gauge.WithLabelValues(backendURL).Set(1)
						}
					} else {
						slog.Warn("Backend is now UNHEALTHY", "backend_url", backendURL)
						if hc.gauge != nil {
							hc.gauge.WithLabelValues(backendURL).Set(0)
						}
					}
					hc.healthStatus[backendURL] = isHealthy
				}
				hc.healthMutex.Unlock()
			case <-stopChan:
				slog.Info("Stopping health checker", "backend_url", backendURL)
				return
			}
		}
	}()
}

// StopChecking stops health checking a backend and removes its state and gauge.
func (hc *Checker) StopChecking(backendURL string) {
	hc.healthMutex.Lock()
	defer hc.healthMutex.Unlock()

	if stopChan, ok := hc.stopChans[backendURL]; ok {
		close(stopChan)
		delete(hc.stopChans, backendURL)
		delete(hc.healthStatus, backendURL)
		if hc.gauge != nil {
			hc.gauge.DeleteLabelValues(backendURL)
		}
	}
}

// Stop signals all health checkers to stop.
func (hc *Checker) Stop() {
	hc.healthMutex.Lock()
	defer hc.healthMutex.Unlock()

	for url, stopChan := range hc.stopChans {
		close(stopChan) // Sends the to the case of <-stopChan
		delete(hc.stopChans, url)
	}
}

// IsHealthy returns whether a backend is currently healthy.
func (hc *Checker) IsHealthy(backendURL string) bool {
	hc.healthMutex.RLock()
	defer hc.healthMutex.RUnlock()
	return hc.healthStatus[backendURL]
}

// SetHealthy manually sets health status (for testing).
func (hc *Checker) SetHealthy(backendURL string, healthy bool) {
	hc.healthMutex.Lock()
	defer hc.healthMutex.Unlock()
	hc.healthStatus[backendURL] = healthy
}

// checkHealth performs a single health check for a backend.
func checkHealth(backendURL string) bool {
	client := &http.Client{Timeout: 2 * time.Second}

	resp, err := client.Get(backendURL + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == 200
}
