package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestIDStoredInContext(t *testing.T) {
	var captured string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if captured == "" {
		t.Fatal("expected request ID in context")
	}
	if rr.Header().Get(requestIDHeader) != captured {
		t.Fatalf("response header %q != context value %q", rr.Header().Get(requestIDHeader), captured)
	}
}

func TestRequestIDClientHeaderInContext(t *testing.T) {
	var captured string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(requestIDHeader, "client-id-123")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if captured != "client-id-123" {
		t.Fatalf("expected client ID, got %q", captured)
	}
}

func TestRequestIDFromContextMissing(t *testing.T) {
	if v := RequestIDFromContext(context.TODO()); v != "" {
		t.Fatalf("expected empty string for nil context, got %q", v)
	}
}
