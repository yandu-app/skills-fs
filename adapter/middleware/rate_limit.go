package middleware

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// bucketTTL is the duration after which an idle bucket is eligible for eviction.
const bucketTTL = 10 * time.Minute

// cleanupInterval is how often the background goroutine scans for stale buckets.
const cleanupInterval = time.Minute

// RateLimiter is a per-IP token bucket rate limiter.
type RateLimiter struct {
	mu       sync.RWMutex
	buckets  map[string]*bucket
	rate     float64 // tokens per second
	capacity int     // max bucket size

	startOnce sync.Once
	stop      context.CancelFunc
}

type bucket struct {
	tokens    float64
	lastCheck time.Time
}

// NewRateLimiter creates a rate limiter allowing 'rate' requests per second
// with burst 'capacity'.
func NewRateLimiter(rate float64, capacity int) *RateLimiter {
	return &RateLimiter{
		buckets:  make(map[string]*bucket),
		rate:     rate,
		capacity: capacity,
	}
}

// Allow returns true if the request from the given IP is within rate limits.
// On the first call it also starts a background cleanup goroutine.
func (rl *RateLimiter) Allow(ip string) bool {
	rl.startOnce.Do(rl.startCleanup)

	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.buckets[ip]
	if !ok {
		b = &bucket{tokens: float64(rl.capacity) - 1, lastCheck: time.Now()}
		rl.buckets[ip] = b
		return true
	}

	elapsed := time.Since(b.lastCheck).Seconds()
	b.tokens += elapsed * rl.rate
	if b.tokens > float64(rl.capacity) {
		b.tokens = float64(rl.capacity)
	}
	b.lastCheck = time.Now()

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// startCleanup launches a background goroutine that periodically removes
// stale buckets. It is called exactly once, on the first Allow invocation.
func (rl *RateLimiter) startCleanup() {
	ctx, cancel := context.WithCancel(context.Background())
	rl.stop = cancel
	go func() {
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				rl.cleanup()
			case <-ctx.Done():
				return
			}
		}
	}()
}

// cleanup removes buckets that have not been accessed for longer than bucketTTL.
func (rl *RateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	for ip, b := range rl.buckets {
		if now.Sub(b.lastCheck) > bucketTTL {
			delete(rl.buckets, ip)
		}
	}
}

// RateLimit returns HTTP middleware that rate limits by client IP.
func RateLimit(rl *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)
			if !rl.Allow(ip) {
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.Index(xff, ","); idx != -1 {
			xff = xff[:idx]
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-Ip"); xri != "" {
		return xri
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
