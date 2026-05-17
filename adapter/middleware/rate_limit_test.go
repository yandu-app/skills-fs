package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiterAllow(t *testing.T) {
	rl := NewRateLimiter(10, 2) // 10/sec, burst of 2

	ip := "192.168.1.1"
	if !rl.Allow(ip) {
		t.Fatal("first request should be allowed")
	}
	if !rl.Allow(ip) {
		t.Fatal("second request within burst should be allowed")
	}
	if rl.Allow(ip) {
		t.Fatal("third request should exceed burst")
	}

	// Wait for token refill.
	time.Sleep(200 * time.Millisecond)
	if !rl.Allow(ip) {
		t.Fatal("request after partial refill should be allowed")
	}
}

func TestRateLimiterDifferentIPs(t *testing.T) {
	rl := NewRateLimiter(10, 1)
	if !rl.Allow("1.1.1.1") {
		t.Fatal("first IP should be allowed")
	}
	if !rl.Allow("2.2.2.2") {
		t.Fatal("second IP should be allowed independently")
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	rl := NewRateLimiter(10, 1)
	handler := RateLimit(rl)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Second request from same IP should be rate limited.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
}

func TestClientIPForwardedFor(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	if got := clientIP(req); got != "1.2.3.4" {
		t.Fatalf("expected 1.2.3.4, got %q", got)
	}
}

func TestClientIPRealIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Real-Ip", "9.8.7.6")
	if got := clientIP(req); got != "9.8.7.6" {
		t.Fatalf("expected 9.8.7.6, got %q", got)
	}
}
