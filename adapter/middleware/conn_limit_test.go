package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestConnLimiterBlocksWhenFull(t *testing.T) {
	cl := NewConnLimiter(1)
	cl.Acquire()
	if cl.Active() != 1 {
		t.Fatalf("expected 1 active, got %d", cl.Active())
	}

	done := make(chan struct{})
	go func() {
		cl.Acquire()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("expected second acquire to block")
	case <-time.After(50 * time.Millisecond):
		// expected
	}

	cl.Release()
	select {
	case <-done:
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected second acquire to unblock after release")
	}
}

func TestConnLimitMiddleware(t *testing.T) {
	cl := NewConnLimiter(2)
	var wg sync.WaitGroup
	handler := ConnLimit(cl)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wg.Done()
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))

	// Start 3 concurrent requests; 2 should execute, 3rd should wait.
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		}()
	}

	// Wait for first 2 to enter the handler.
	wg.Wait()
	if cl.Active() > 2 {
		t.Fatalf("expected at most 2 active, got %d", cl.Active())
	}
}

func TestNilConnLimiter(t *testing.T) {
	var cl *ConnLimiter
	cl.Acquire()
	cl.Release()
	if cl.Active() != 0 {
		t.Fatal("nil limiter should report 0 active")
	}
}

func TestConnLimiterZeroMeansUnlimited(t *testing.T) {
	cl := NewConnLimiter(0)
	if cl != nil {
		t.Fatal("zero max should return nil")
	}
	clNeg := NewConnLimiter(-1)
	if clNeg != nil {
		t.Fatal("negative max should return nil")
	}
}
