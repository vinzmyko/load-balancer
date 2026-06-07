// Package registry implements in-memory service registry with TTL eviction. Instances register and periodically re-register.
// Instances that stop heartbeating are evicted.
package registry

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Registry maps service name -> instance URL -> last heartbeat time.
type Registry struct {
	mu       sync.Mutex                      // Prevents concurrent access to services map
	services map[string]map[string]time.Time // Multiple services, each with multiple instances, with last check in time
	ttl      time.Duration                   // Time to live without heartbeat before evicted
}

// New creates a registry.
func New(ttl time.Duration) *Registry {
	return &Registry{
		services: make(map[string]map[string]time.Time),
		ttl:      ttl,
	}
}

// registration is the request body for the `/register` endpoint.
type registration struct {
	Service string `json:"service"`
	URL     string `json:"url"`
}

// register creates the instance and updates the last-seen time. Calling this repeatedly is the heartbeat.
func (r *Registry) register(service, url string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// If service is not registered make a map last surveyed time
	if r.services[service] == nil {
		r.services[service] = make(map[string]time.Time)
	}
	// If didn't exist before log first registry
	if _, exists := r.services[service][url]; !exists {
		slog.Info("instance registered", "service", service, "url", url)
	}
	r.services[service][url] = time.Now()
}

// deregister removes the instance from the services map.
func (r *Registry) deregister(service, url string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if instance, ok := r.services[service]; ok {
		if _, ok := instance[url]; ok {
			delete(instance, url)
			slog.Info("instance deregistered", "service", service, "url", url)
		}
	}
}

// list returns all the URLs for a given service.
func (r *Registry) list(service string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	instances := r.services[service]
	urls := make([]string, 0, len(instances))
	for url := range instances {
		urls = append(urls, url)
	}
	return urls
}

// evictExpired removes instances where last heartbeat is older than TTL.
func (r *Registry) evictExpired() {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	for service, instances := range r.services {
		for url, lastSeen := range instances {
			if now.Sub(lastSeen) > r.ttl {
				delete(instances, url)
				slog.Warn("instance evicted (timeout)", "service", service, "url", url)
			}
		}
	}
}

// StartEviction runs the background goroutine until stop is closed.
func (r *Registry) StartEviction(interval time.Duration, stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				r.evictExpired() // Check the services TTL is expired
			case <-stop:
				return
			}
		}
	}()
}

// Handler returns the registry's HTTP routes.
func (r *Registry) Handler() http.Handler {
	// Create HTTP router
	mux := http.NewServeMux()

	// Register handler for `POST /register` w is the writer and req is the incoming request
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, req *http.Request) {
		reg, ok := decodeRegistration(w, req)
		if !ok {
			return
		}
		// Register service if parsed correctly.
		r.register(reg.Service, reg.URL)
		w.WriteHeader(http.StatusNoContent) // Send success but no return, standard for registry
	})

	// Register handler for `DELETE /register`
	mux.HandleFunc("DELETE /register", func(w http.ResponseWriter, req *http.Request) {
		reg, ok := decodeRegistration(w, req)
		if !ok {
			return
		}
		r.deregister(reg.Service, reg.URL)
		w.WriteHeader(http.StatusNoContent)
	})

	// Register handler for `GET /services/{name}`
	mux.HandleFunc("GET /services/{name}", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json") // Sets header response body is JSON
		// Extract `{name}`, gets the list for that service name and put result in the JSON body
		json.NewEncoder(w).Encode(r.list(req.PathValue("name")))
	})

	return mux
}

// decodeRegistration decodes HTTP request into registration struct. Returns bool if error exists.
func decodeRegistration(w http.ResponseWriter, req *http.Request) (registration, bool) {
	var reg registration
	if err := json.NewDecoder(req.Body).Decode(&reg); err != nil || reg.Service == "" || reg.URL == "" {
		http.Error(w, "invalid registration", http.StatusBadRequest)
		return reg, false
	}
	return reg, true
}
