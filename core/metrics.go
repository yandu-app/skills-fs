package core

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Metrics struct {
	mu  sync.RWMutex
	ops map[OpCode]*opMetrics
}

type opMetrics struct {
	count   atomic.Uint64
	errors  atomic.Uint64
	totalNS atomic.Uint64
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
	metric.totalNS.Add(uint64(time.Since(started).Nanoseconds()))
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
		metric = &opMetrics{}
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
	for _, op := range keys {
		metric := snapshot[op]
		fmt.Fprintf(&b, "skills_fs_operations_total{op=%q} %d\n", op, metric.count.Load())
		fmt.Fprintf(&b, "skills_fs_operation_errors_total{op=%q} %d\n", op, metric.errors.Load())
		fmt.Fprintf(&b, "skills_fs_operation_latency_seconds{op=%q} %.9f\n", op, float64(metric.totalNS.Load())/1e9)
	}
	return []byte(b.String())
}
