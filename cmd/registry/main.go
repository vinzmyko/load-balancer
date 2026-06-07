package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/vinzmyko/load-balancer/internal/registry"
)

func main() {
	// Set up JSON logging to stdout
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	reg := registry.New(15 * time.Second)

	// Start eviction goroutine scanning every 5 seconds
	stop := make(chan struct{})
	reg.StartEviction(5*time.Second, stop)

	// Start HTTP server
	addr := ":8090"
	slog.Info("Starting registry", "address", addr)
	if err := http.ListenAndServe(addr, reg.Handler()); err != nil {
		slog.Error("registry exited", "error", err)
		os.Exit(1)
	}
}
