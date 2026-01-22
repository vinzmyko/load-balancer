// Package circuitbreaker handles if a backend server should be shut down.
package circuitbreaker

import (
	"log"
	"sync"
	"time"
)

type CircuitState int

const (
	stateClosed CircuitState = iota
	stateOpen
	stateHalfOpen
)

// CircuitBreaker implements the circuit breaker pattern
type CircuitBreaker struct {
	backendURL       string
	state            CircuitState
	failures         int
	lastFailureTime  time.Time
	failureThreshold int
	timeout          time.Duration
	mu               sync.Mutex
}

// New creates a new circuit breaker
func New(backendURL string, failureThreshold int, timeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		backendURL:       backendURL,
		state:            stateClosed,
		failures:         0,
		failureThreshold: failureThreshold,
		timeout:          timeout,
	}
}

// CanAttempt checks if request should be allowed
func (cb *CircuitBreaker) CanAttempt() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case stateClosed:
		return true

	case stateOpen:
		// Check if timeout has passed
		if time.Since(cb.lastFailureTime) > cb.timeout {
			cb.state = stateHalfOpen
			log.Printf("Circuit HALF-OPEN for backend %s - testing recovery", cb.backendURL)
			return true
		}
		return false

	case stateHalfOpen:
		return true

	default:
		return true
	}
}

// RecordSuccess records a successful request
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == stateHalfOpen {
		log.Printf("Circuit CLOSED for backend %s - backend recovered", cb.backendURL)
	}

	cb.failures = 0
	cb.state = stateClosed
}

func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures++
	cb.lastFailureTime = time.Now()

	if cb.state == stateHalfOpen {
		// Failed so open the state (Unhealthy)
		cb.state = stateOpen
		log.Printf("Circuit OPENED for backend %s", cb.backendURL)
	} else if cb.failures >= cb.failureThreshold {
		cb.state = stateOpen
		log.Printf("Circuit OPENED for backend %s", cb.backendURL)
	}
}
