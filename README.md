# HTTP Load Balancer

A production-ready HTTP load balancer built in Go, featuring health checking, circuit breakers, and comprehensive observability.

## Features

- Round-robin load balancing
- Active health checking
- Circuit breakers
- Prometheus metrics
- Structured logging
- Graceful shutdown

## Monitoring

### Metrics

Prometheus metrics available at `http://localhost:9090/metrics`:

### Logs

Structured logs for each request:
```
INFO request method=GET path=/api/users backend=http://localhost:8081 status=200 duration_ms=2.34
```

## Testing

Three integration tests verify core behavior:

1. Round-robin distribution
2. Health check failover
3. Circuit breaker

## Development

Built with:
- Go 1.21+
- Prometheus client library
- Standard library for HTTP/networking
