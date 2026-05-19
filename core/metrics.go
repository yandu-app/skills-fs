package core

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var latencyBuckets = []time.Duration{
	1 * time.Millisecond,
	10 * time.Millisecond,
	25 * time.Millisecond,
	50 * time.Millisecond,
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
	1 * time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
}

type Metrics struct {
	mu          sync.RWMutex
	ops         map[OpCode]*opMetrics
	eventBus    *eventBus
	cacheHits   atomic.Uint64
	cacheMisses atomic.Uint64
}

type opMetrics struct {
	count   atomic.Uint64
	errors  atomic.Uint64
	totalNS atomic.Uint64
	buckets []atomic.Uint64
}

func newMetrics() *Metrics {
	return &Metrics{ops: make(map[OpCode]*opMetrics)}
}

func (m *Metrics) record(op OpCode, started time.Time, err error) {
	metric := m.op(op)
	metric.count.Add(1)
	if err != nil {
		metric.errors.Add(1)
	}
	elapsed := time.Since(started)
	ns := uint64(elapsed.Nanoseconds())
	metric.totalNS.Add(ns)
	for i, bound := range latencyBuckets {
		if elapsed <= bound {
			metric.buckets[i].Add(1)
			break
		}
	}
}

func (m *Metrics) recordCacheHit() {
	m.cacheHits.Add(1)
}

func (m *Metrics) recordCacheMiss() {
	m.cacheMisses.Add(1)
}

func (m *Metrics) op(op OpCode) *opMetrics {
	m.mu.RLock()
	metric := m.ops[op]
	m.mu.RUnlock()
	if metric != nil {
		return metric
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	metric = m.ops[op]
	if metric == nil {
		metric = &opMetrics{buckets: make([]atomic.Uint64, len(latencyBuckets))}
		m.ops[op] = metric
	}
	return metric
}

func (m *Metrics) Prometheus() []byte {
	m.mu.RLock()
	keys := make([]string, 0, len(m.ops))
	for op := range m.ops {
		keys = append(keys, string(op))
	}
	sort.Strings(keys)
	snapshot := make(map[string]*opMetrics, len(m.ops))
	for op, metric := range m.ops {
		snapshot[string(op)] = metric
	}
	m.mu.RUnlock()

	var b strings.Builder
	b.WriteString("# TYPE skills_fs_operations_total counter\n")
	b.WriteString("# TYPE skills_fs_operation_errors_total counter\n")
	b.WriteString("# TYPE skills_fs_operation_latency_seconds counter\n")
	b.WriteString("# TYPE skills_fs_operation_latency_seconds histogram\n")
	for _, op := range keys {
		metric := snapshot[op]
		fmt.Fprintf(&b, "skills_fs_operations_total{op=%q} %d\n", op, metric.count.Load())
		fmt.Fprintf(&b, "skills_fs_operation_errors_total{op=%q} %d\n", op, metric.errors.Load())
		fmt.Fprintf(&b, "skills_fs_operation_latency_seconds_sum{op=%q} %.9f\n", op, float64(metric.totalNS.Load())/1e9)
		fmt.Fprintf(&b, "skills_fs_operation_latency_seconds_count{op=%q} %d\n", op, metric.count.Load())
		var cumulative uint64
		for i, bound := range latencyBuckets {
			cumulative += metric.buckets[i].Load()
			fmt.Fprintf(&b, "skills_fs_operation_latency_seconds_bucket{op=%q,le=%q} %d\n", op, bound.String(), cumulative)
		}
		fmt.Fprintf(&b, "skills_fs_operation_latency_seconds_bucket{op=%q,le=%q} %d\n", op, "+Inf", metric.count.Load())
	}
	b.WriteString("# TYPE skills_fs_provider_cache_hits_total counter\n")
	fmt.Fprintf(&b, "skills_fs_provider_cache_hits_total %d\n", m.cacheHits.Load())
	b.WriteString("# TYPE skills_fs_provider_cache_misses_total counter\n")
	fmt.Fprintf(&b, "skills_fs_provider_cache_misses_total %d\n", m.cacheMisses.Load())
	if m.eventBus != nil {
		b.WriteString("# TYPE skills_fs_events_emitted_total counter\n")
		b.WriteString("# TYPE skills_fs_events_delivered_total counter\n")
		fmt.Fprintf(&b, "skills_fs_events_emitted_total %d\n", m.eventBus.emitted.Load())
		fmt.Fprintf(&b, "skills_fs_events_delivered_total %d\n", m.eventBus.delivered.Load())
	}
	return []byte(b.String())
}
