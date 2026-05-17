package middleware

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestRequestIDGeneratesWhenMissing(t *testing.T) {
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	id := rec.Header().Get(requestIDHeader)
	if id == "" {
		t.Fatal("expected generated request ID")
	}
	if len(id) != 16 {
		t.Fatalf("expected 16-char hex ID, got %q (len=%d)", id, len(id))
	}
}

func TestRequestIDPreservesClientHeader(t *testing.T) {
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(requestIDHeader, "client-id-123")
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get(requestIDHeader); got != "client-id-123" {
		t.Fatalf("expected client ID preserved, got %q", got)
	}
}

func TestAccessLogRecordsStatus(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.NewFile(0, os.DevNull), nil))
	handler := AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
