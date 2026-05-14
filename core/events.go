package core

import "sync"

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

// notifier is a callback invoked synchronously when an event fires.
type notifier func(Event)

// eventBus multiplexes filesystem mutations to registered listeners.
type eventBus struct {
	mu   sync.RWMutex
	subs []notifier
}

func newEventBus() *eventBus {
	return &eventBus{}
}

func (eb *eventBus) register(fn notifier) {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	eb.subs = append(eb.subs, fn)
}

func (eb *eventBus) emit(e Event) {
	eb.mu.RLock()
	subs := make([]notifier, len(eb.subs))
	copy(subs, eb.subs)
	eb.mu.RUnlock()
	for _, fn := range subs {
		fn(e)
	}
}
