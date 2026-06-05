package balancer

// Handles the Backend creation, and response write implementation.

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/vinzmyko/load-balancer/internal/circuitbreaker"
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

func (rw *responseWriter) Flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func wrapResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{
		ResponseWriter: w,
		statusCode:     200,
	}
}

func NewBackend(backendURL string, circuitBreaker *circuitbreaker.CircuitBreaker) (*Backend, error) {
	target, err := url.Parse(backendURL)
	if err != nil {
		slog.Error("Backend server parse error",
			"backend_url", backendURL,
			"error", err,
		)
		return nil, fmt.Errorf("parsing backend url %q: %w", backendURL, err)
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
	}, nil
}
