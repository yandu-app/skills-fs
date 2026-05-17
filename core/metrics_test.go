package core

import (
	"strings"
	"testing"
	"time"
)

func TestMetricsHistogram(t *testing.T) {
	m := newMetrics()
	m.record(OpRead, time.Now().Add(-5*time.Millisecond), nil)
	m.record(OpRead, time.Now().Add(-50*time.Millisecond), nil)
	m.record(OpRead, time.Now().Add(-500*time.Millisecond), nil)

	out := string(m.Prometheus())
	if !strings.Contains(out, "skills_fs_operation_latency_seconds_bucket") {
		t.Fatal("missing histogram buckets in prometheus output")
	}
	if !strings.Contains(out, `le="1ms"`) {
		t.Fatal("missing 1ms bucket")
	}
	if !strings.Contains(out, `le="+Inf"`) {
		t.Fatal("missing +Inf bucket")
	}
	if !strings.Contains(out, "skills_fs_operation_latency_seconds_sum") {
		t.Fatal("missing latency sum")
	}
	if !strings.Contains(out, "skills_fs_operation_latency_seconds_count") {
		t.Fatal("missing latency count")
	}
}

func TestMetricsRecordsErrors(t *testing.T) {
	m := newMetrics()
	m.record(OpWrite, time.Now(), nil)
	m.record(OpWrite, time.Now(), posix(EIO, OpWrite, "/x", nil))

	out := string(m.Prometheus())
	if !strings.Contains(out, `skills_fs_operation_errors_total{op="write"} 1`) {
		t.Fatalf("expected 1 write error, got:\n%s", out)
	}
}

func TestMetricsEventBus(t *testing.T) {
	eb := newEventBus()
	m := newMetrics()
	m.eventBus = eb

	eb.emit(Event{Path: "/a", Kind: EventWrite})
	id := eb.register(func(e Event) {}, "")
	eb.emit(Event{Path: "/b", Kind: EventCreate})
	eb.emit(Event{Path: "/c", Kind: EventRemove})
	eb.unregister(id)

	out := string(m.Prometheus())
	if !strings.Contains(out, "skills_fs_events_emitted_total 3") {
		t.Fatalf("expected 3 emitted, got:\n%s", out)
	}
	if !strings.Contains(out, "skills_fs_events_delivered_total 2") {
		t.Fatalf("expected 2 delivered (1 per emit with active subscriber), got:\n%s", out)
	}
}
