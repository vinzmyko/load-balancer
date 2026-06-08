package discovery

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/vinzmyko/load-balancer/internal/config"
)

// Registry implements Discoverer that fetches the live backend set from a service registry.
// On failed poll to registry server returns last known good set of backends.
type Registry struct {
	registryURL string
	service     string
	client      *http.Client

	mu       sync.Mutex
	lastGood []config.BackendConfig
}

// NewRegistry returns a discovery client that fetches backends for the named service
// from registryURL.
func NewRegistry(registryURL, service string) *Registry {
	return &Registry{
		registryURL: registryURL,
		service:     service,
		client:      &http.Client{Timeout: 3 * time.Second},
	}
}

func (r *Registry) Backends() []config.BackendConfig {
	// Poll registry server, if down return last good backends.
	resp, err := r.client.Get(r.registryURL + "/services/" + r.service)
	if err != nil {
		slog.Warn("registry poll failed, using last-known-good", "error", err)
		return r.snapshot()
	}
	defer resp.Body.Close()

	// If no error but StatusCode is not StatusOk return last good backends.
	if resp.StatusCode != http.StatusOK {
		slog.Warn("registry poll returned non-200, using last-known-good", "status", resp.StatusCode)
		return r.snapshot()
	}

	// Get the backend urls of the service else return last good backends.
	var urls []string
	if err := json.NewDecoder(resp.Body).Decode(&urls); err != nil {
		slog.Warn("decoding registry response, using last-known-good", "error", err)
		return r.snapshot()
	}

	// Parse to the BackendConfig structs.
	backends := make([]config.BackendConfig, 0, len(urls))
	for _, u := range urls {
		backends = append(backends, config.BackendConfig{URL: u, Weight: 1})
	}

	r.mu.Lock()
	// Update last-known-good.
	r.lastGood = backends
	r.mu.Unlock()

	return backends
}

// snapshot returns the last good set of backends. Used when registry service cannot be connected to.
func (r *Registry) snapshot() []config.BackendConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastGood
}
