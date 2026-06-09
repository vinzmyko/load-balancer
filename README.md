# Dynamic HTTP Load Balancer

A production-style HTTP load balancer built in Go, featuring service discovery,
health checking, circuit breakers, and a full observability stack (metrics,
traces, and logs).

## Features

- Round-robin load balancing over a reverse proxy
- Dynamic service discovery via a built-in registry (with static config fallback)
- Active health checking with automatic failover
- Per-backend circuit breakers (closed → open → half-open)
- Prometheus metrics
- Distributed tracing with OpenTelemetry + Tempo
- Log aggregation with Loki + Promtail
- Pre-provisioned Grafana dashboards
- Structured JSON logging (slog)
- Graceful shutdown

## Architecture

```
                    ┌─────────────┐
         register   │  Registry   │   poll /services/{name}
      ┌────────────▶│  (:8090)    │◀────────────┐
      │             └─────────────┘             │
┌──────────┐                            ┌────────────────┐
│ Backends │◀───────── proxy ───────────│ Load Balancer  │
│ :8081-83 │                            │ :8080 (:9091)  │
└──────────┘                            └────────────────┘
                                                │ metrics / traces / logs
                                ┌───────────────┼────────────────┐
                          ┌──────────┐    ┌──────────┐     ┌──────────┐
                          │Prometheus│    │  Tempo   │     │   Loki   │
                          └──────────┘    └──────────┘     └──────────┘
                                └──────────────┴────────────────┘
                                          ┌──────────┐
                                          │ Grafana  │
                                          │  :3000   │
                                          └──────────┘
```

Backends self-register with the registry and heartbeat every 5s. The load
balancer polls the registry every 5s and reconciles its backend set. If the
registry is unreachable, discovery falls back to the last-known-good set.

## Discovery

Two modes, selected by environment variables:

- Dynamic registry: set `REGISTRY_URL` (and optionally `SERVICE_NAME`,
  defaults to `backend`). The LB fetches the live backend set from the registry.
- Static: if `REGISTRY_URL` is unset, backends are read from `config.yaml`.

## Running

```bash
docker compose up --build
```

| Service        | URL                           |
|----------------|-------------------------------|
| Load balancer  | http://localhost:8080         |
| LB metrics     | http://localhost:9091/metrics |
| Registry       | http://localhost:8090         |
| Prometheus     | http://localhost:9090         |
| Grafana        | http://localhost:3000         |
| Tempo          | http://localhost:3200         |
| Loki           | http://localhost:3100         |

## Monitoring

### Metrics

Prometheus metrics exposed at `http://localhost:9091/metrics`:

- `loadbalancer_requests_total{backend,status_code}` — request count
- `loadbalancer_request_duration_seconds{backend,status_code}` — latency histogram
- `loadbalancer_backend_healthy{backend}` — 1 = healthy, 0 = unhealthy
- `loadbalancer_circuit_breaker_state{backend}` — 0 = closed, 1 = open, 2 = half-open

The "Load Balancer Overview" Grafana dashboard is auto-provisioned with request
rate, error rate, latency percentiles, backend health, and circuit breaker state.

### Tracing

Spans are exported to Tempo via OTLP/gRPC. Trace context is propagated to
backends through request headers, so you can follow a request end to end.

### Logs

Structured JSON logs from all containers are shipped to Loki by Promtail and
queryable in Grafana.

## Development
Built with:
- Go 1.25
- Prometheus client library
- OpenTelemetry SDK
- Standard library for HTTP/networking
