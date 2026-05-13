package core

import "sync"

type serialQueue struct {
	mu sync.Mutex
}

func (q *serialQueue) run(fn func() error) error {
	if q == nil {
		return fn()
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return fn()
}
