// Package health handles the health check for a backend server.
package health

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Checker manages health checking for multiple backends
type Checker struct {
	healthStatus map[int]bool // All the backend server's health status
	healthMutex  sync.RWMutex // Mutex for health related operations
}

// NewChecker creates a health checker for the given number of backends
func NewChecker(backendCount int) *Checker {
	healthStatus := make(map[int]bool)
	for i := range backendCount {
		healthStatus[i] = true
	}

	return &Checker{
		healthStatus: healthStatus,
	}
}

// StartChecking starts a background health checker for a backend
func (hc *Checker) StartChecking(idx int, backendURL string, gauge *prometheus.GaugeVec) {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			<-ticker.C

			isHealthy := checkHealth(backendURL)

			hc.healthMutex.Lock()
			if hc.healthStatus[idx] != isHealthy {
				if isHealthy {
					log.Printf("Backend %d (%s) is now HEALTHY", idx, backendURL)
					gauge.WithLabelValues(backendURL).Set(1)
				} else {
					log.Printf("Backend %d (%s) is now UNHEALTHY", idx, backendURL)
					gauge.WithLabelValues(backendURL).Set(0)
				}
				hc.healthStatus[idx] = isHealthy
			}
			hc.healthMutex.Unlock()
		}
	}()
}

// IsHealthy returns whether a backend is currently healthy
func (hc *Checker) IsHealthy(idx int) bool {
	hc.healthMutex.RLock()
	defer hc.healthMutex.RUnlock()
	return hc.healthStatus[idx]
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
