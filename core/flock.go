package core

import (
	"context"
	"sync"
	"time"
)

// LockKind enumerates the advisory lock states a handle can request.
// Values mirror flock(2): shared (multiple readers), exclusive (single
// writer), or none (idle).
type LockKind int

const (
	LockNone LockKind = iota
	LockShared
	LockExclusive
)

const defaultLockTimeout = 30 * time.Second

// lockManager tracks advisory flock state per mounted path. Shared locks may
// stack; an exclusive lock blocks all other holders. The manager keys on the
// path so a handle released from any goroutine sees the same lock state,
// matching flock(2) semantics where the descriptor — not the inode pointer —
// drives ownership.
type lockManager struct {
	mu              sync.Mutex
	states          map[string]*lockState
	deadlockTimeout time.Duration
}

func newLockManager(timeout time.Duration) *lockManager {
	if timeout == 0 {
		timeout = defaultLockTimeout
	}
	return &lockManager{
		states:          make(map[string]*lockState),
		deadlockTimeout: timeout,
	}
}

type lockState struct {
	mu     sync.Mutex
	cond   *sync.Cond
	shared map[*Handle]struct{}
	excl   *Handle
}

func (m *lockManager) stateFor(path string) *lockState {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.states[path]
	if !ok {
		s = &lockState{shared: make(map[*Handle]struct{})}
		s.cond = sync.NewCond(&s.mu)
		m.states[path] = s
	}
	return s
}

func (m *lockManager) acquire(ctx context.Context, path string, h *Handle, kind LockKind, nonblock bool) error {
	if kind != LockShared && kind != LockExclusive {
		return posix(EINVAL, OpWrite, path, nil)
	}
	if h.lockKind == kind {
		return nil
	}
	if h.lockKind != LockNone {
		// upgrade or downgrade by releasing first
		if err := m.release(path, h); err != nil {
			return err
		}
	}
	s := m.stateFor(path)
	deadline := time.Now().Add(m.deadlockTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if canTake(s, h, kind) {
			take(s, h, kind)
			h.lockKind = kind
			return nil
		}
		if nonblock {
			return posix(EBUSY, OpWrite, path, nil)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return posix(ETIMEDOUT, OpWrite, path, nil)
		}
		// cond.Wait has no native timeout; use a watchdog to broadcast when
		// the deadline or context fires so the waiter wakes.
		done := make(chan struct{})
		go func() {
			t := time.NewTimer(remaining)
			defer t.Stop()
			select {
			case <-t.C:
			case <-ctx.Done():
			case <-done:
				return
			}
			s.mu.Lock()
			s.cond.Broadcast()
			s.mu.Unlock()
		}()
		s.cond.Wait()
		close(done)
		if err := ctx.Err(); err != nil {
			return posix(ETIMEDOUT, OpWrite, path, err)
		}
		if time.Now().After(deadline) {
			return posix(ETIMEDOUT, OpWrite, path, nil)
		}
	}
}

func canTake(s *lockState, h *Handle, kind LockKind) bool {
	if s.excl != nil && s.excl != h {
		return false
	}
	if kind == LockExclusive {
		if len(s.shared) > 0 {
			if _, only := s.shared[h]; !only || len(s.shared) > 1 {
				return false
			}
		}
	}
	return true
}

func take(s *lockState, h *Handle, kind LockKind) {
	switch kind {
	case LockShared:
		s.shared[h] = struct{}{}
	case LockExclusive:
		s.excl = h
	}
}

func (m *lockManager) release(path string, h *Handle) error {
	m.mu.Lock()
	s, ok := m.states[path]
	m.mu.Unlock()
	if !ok {
		h.lockKind = LockNone
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch h.lockKind {
	case LockShared:
		delete(s.shared, h)
	case LockExclusive:
		if s.excl == h {
			s.excl = nil
		}
	}
	h.lockKind = LockNone
	s.cond.Broadcast()
	return nil
}

// purge removes the lock state for a path. Called when the mount is unmounted.
func (m *lockManager) purge(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.states, path)
}

// inspect returns (sharedCount, hasExclusive) for tests and observability.
func (m *lockManager) inspect(path string) (int, bool) {
	m.mu.Lock()
	s, ok := m.states[path]
	m.mu.Unlock()
	if !ok {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.shared), s.excl != nil
}
