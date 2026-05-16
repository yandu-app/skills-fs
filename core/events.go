package core

import (
	"strings"
	"sync"
	"sync/atomic"
)

// EventKind classifies filesystem notifications.
type EventKind int

const (
	EventCreate EventKind = iota
	EventWrite
	EventRemove
)

// Event describes a single mutation in the virtual namespace.
type Event struct {
	Path string
	Kind EventKind
}

// subscriber is a registered listener with optional path prefix filtering.
type subscriber struct {
	id     uint64
	fn     func(Event)
	prefix string
}

// eventBus multiplexes filesystem mutations to registered listeners.
type eventBus struct {
	mu   sync.RWMutex
	subs map[uint64]subscriber
	seq  atomic.Uint64
}

func newEventBus() *eventBus {
	return &eventBus{subs: make(map[uint64]subscriber)}
}

// register adds a listener. If prefix is non-empty, the listener only receives
// events whose path starts with that prefix. It returns a unique ID that can
// be passed to unregister.
func (eb *eventBus) register(fn func(Event), prefix string) uint64 {
	id := eb.seq.Add(1)
	eb.mu.Lock()
	defer eb.mu.Unlock()
	eb.subs[id] = subscriber{id: id, fn: fn, prefix: prefix}
	return id
}

// unregister removes a listener by the ID returned from register.
func (eb *eventBus) unregister(id uint64) {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	delete(eb.subs, id)
}

func (eb *eventBus) emit(e Event) {
	eb.mu.RLock()
	subs := make([]subscriber, 0, len(eb.subs))
	for _, s := range eb.subs {
		subs = append(subs, s)
	}
	eb.mu.RUnlock()
	for _, s := range subs {
		if s.prefix != "" && !strings.HasPrefix(e.Path, s.prefix) {
			continue
		}
		s.fn(e)
	}
}

func (eb *eventBus) clear() {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	eb.subs = make(map[uint64]subscriber)
}
