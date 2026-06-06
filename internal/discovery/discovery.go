// Package discovery provides the source of truth for which backends exist.
package discovery

import "github.com/vinzmyko/load-balancer/internal/config"

// Discoverer reports which backends currently exist. Implementations can be from
// a config file (static) or dynamic.
type Discoverer interface {
	Backends() []config.BackendConfig // Need a method Backends()
}

// Static implements Discoverer backed with a fixed list loaded from config.yaml.
type Static struct {
	backends []config.BackendConfig
}

// NewStatic returns a Static struct which implements Discoverer that reports the given backends.
func NewStatic(backends []config.BackendConfig) *Static {
	return &Static{backends: backends}
}

// Backends returns the backend list.
func (s *Static) Backends() []config.BackendConfig {
	return s.backends
}
