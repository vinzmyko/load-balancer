package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/errgroup"

	"github.com/vinzmyko/load-balancer/internal/balancer"
	"github.com/vinzmyko/load-balancer/internal/config"
	"github.com/vinzmyko/load-balancer/internal/discovery"
	"github.com/vinzmyko/load-balancer/internal/health"
	"github.com/vinzmyko/load-balancer/internal/telemetry"
)

// Health checking function handler
func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		slog.Error("load balancer exited with error", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	shutdown := telemetry.InitTracer(ctx)

	cfg, err := config.Load("config.yaml")
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	var disc discovery.Discoverer
	// If env vars set use dynamic backends else use config file.
	if registryURL := os.Getenv("REGISTRY_URL"); registryURL != "" {
		service := cmp.Or(os.Getenv("SERVICE_NAME"), "backend")
		disc = discovery.NewRegistry(registryURL, service)
		slog.Info("Using registry discovery", "registry_url", registryURL, "service", service)
	} else {
		disc = discovery.NewStatic(cfg.Backends)
		slog.Info("Using static discovery from config.yaml")
	}

	metrics := balancer.NewMetrics()

	hc := health.NewChecker(metrics.BackendHealthy())

	lb := balancer.New(hc, metrics)
	lb.UpdateBackends(disc.Backends()) // polls immediately on startup

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/", lb.RouteHandler())

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Server.Port),
		Handler: mux,
	}

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsServer := &http.Server{
		Addr:    ":9091",
		Handler: metricsMux,
	}

	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		slog.Info("Starting load balancer", "address", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("load balancer server: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		slog.Info("Starting metrics server", "address", metricsServer.Addr)
		if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("metrics server: %w", err)
		}
		return nil
	})

	// Goroutine that keeps the load balancer in sync with the registry.
	g.Go(func() error {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				lb.UpdateBackends(disc.Backends())
			case <-gCtx.Done():
				return nil
			}
		}
	})

	// Wakes when a signal arrives or a server above fails, then drains both servers and the tracer.
	g.Go(func() error {
		<-gCtx.Done()
		slog.Info("Shutting down gracefully")

		hc.Stop()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("Server shutdown error", "error", err)
		}
		if err := metricsServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("Metrics server shutdown error", "error", err)
		}
		if err := shutdown(shutdownCtx); err != nil {
			slog.Error("Tracer shutdown error", "error", err)
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		return fmt.Errorf("server group: %w", err)
	}

	slog.Info("Shutdown complete")
	return nil
}
