package middleware

import (
	"net/http"
	"sync"
)

// ConnLimiter limits the number of concurrent HTTP connections.
type ConnLimiter struct {
	sem   chan struct{}
	mu    sync.Mutex
	count int
}

// NewConnLimiter creates a limiter allowing at most 'max' concurrent
// connections. A zero or negative max means unlimited.
func NewConnLimiter(max int) *ConnLimiter {
	if max <= 0 {
		return nil
	}
	return &ConnLimiter{sem: make(chan struct{}, max)}
}

// Acquire blocks until a connection slot is available.
func (cl *ConnLimiter) Acquire() {
	if cl == nil {
		return
	}
	cl.sem <- struct{}{}
	cl.mu.Lock()
	cl.count++
	cl.mu.Unlock()
}

// Release returns a connection slot.
func (cl *ConnLimiter) Release() {
	if cl == nil {
		return
	}
	cl.mu.Lock()
	cl.count--
	cl.mu.Unlock()
	<-cl.sem
}

// Active returns the current number of active connections.
func (cl *ConnLimiter) Active() int {
	if cl == nil {
		return 0
	}
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.count
}

// ConnLimit returns HTTP middleware that limits concurrent connections.
func ConnLimit(cl *ConnLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cl.Acquire()
			defer cl.Release()
			next.ServeHTTP(w, r)
		})
	}
}
