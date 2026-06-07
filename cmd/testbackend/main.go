package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

var requestCounter uint64

func main() {
	port := "8081"
	if len(os.Args) > 1 {
		port = os.Args[1]
	}

	registryURL := getenv("REGISTRY_URL", "http://localhost:8090")
	serviceName := getenv("SERVICE_NAME", "backend")
	// ADVERTISE_URL is how other services reach this backend.
	advertiseURL := getenv("ADVERTISE_URL", "http://localhost:"+port)

	// Creates context that gets cancelled when receives (CTRL + C) or SIGTERM for graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mux := http.NewServeMux()
	// Log that this backend has received a request
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddUint64(&requestCounter, 1)
		response := fmt.Sprintf("Backend port %s - Request #%d Path: %s\n", port, count, r.URL.Path)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(response))
	})

	// Run server in a goroutine
	server := &http.Server{Addr: ":" + port, Handler: mux}

	go func() {
		slog.Info("Test backend listening", "port", port)
		// ErrServerClosed is expected and can be ignored.
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "error", err)
		}
	}()

	// Register once on start up, then create goroutine to heartbeat registry service.
	register(registryURL, serviceName, advertiseURL)
	go heartbeat(ctx, registryURL, serviceName, advertiseURL, 5*time.Second)

	// Blocks until context is cancelled.
	<-ctx.Done()
	slog.Info("Shutting down, deregistering", "url", advertiseURL)

	deregister(registryURL, serviceName, advertiseURL)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(shutdownCtx)
}

// getenv checks if env key exists if not return fallback.
func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// registration is the request body sent to the registry for register and deregister functions.
type registration struct {
	Service string `json:"service"`
	URL     string `json:"url"`
}

// sendRegistration is a helper for the marshal, send, and request logic.
func sendRegistration(method, registryURL, service, url string) {
	// Serialises struct into []byte for JSON.
	body, _ := json.Marshal(registration{Service: service, URL: url})

	// Build request to registry.
	req, err := http.NewRequest(method, registryURL+"/register", bytes.NewReader(body))
	if err != nil {
		slog.Error("building registration request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	// Send to registry with 3 second timeout if down.
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// If the registry is down retry on the next heartbeat.
		slog.Warn("registration request failed", "method", method, "error", err)
		return
	}
	resp.Body.Close()
}

// register is a wrapper for readability.
func register(registryURL, service, url string) {
	sendRegistration(http.MethodPost, registryURL, service, url)
}

// deregister is a wrapper for readability.
func deregister(registryURL, service, url string) {
	sendRegistration(http.MethodDelete, registryURL, service, url)
}

// heartbeat sends requests to re-register itself onto the registry which updates it's last contact time.
func heartbeat(ctx context.Context, registryURL, service, url string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			register(registryURL, service, url) // heartbeat the registry
		case <-ctx.Done():
			return
		}
	}
}
