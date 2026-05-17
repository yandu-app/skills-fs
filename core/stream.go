package core

import (
	"sync"
)

const defaultStreamCapacity = 64 * 1024
const streamReadChunk = 64 * 1024

// streamManager owns the ring buffers for KindStream mounts. Each mounted
// stream path gets exactly one buffer; all handles opened against that path
// share it, matching FIFO / pipe semantics.
type streamManager struct {
	mu      sync.RWMutex
	buffers map[string]*streamBuffer
}

func newStreamManager() *streamManager {
	return &streamManager{buffers: make(map[string]*streamBuffer)}
}

func (sm *streamManager) getOrCreate(path string, cfg *StreamConfig) *streamBuffer {
	sm.mu.RLock()
	if b, ok := sm.buffers[path]; ok {
		sm.mu.RUnlock()
		return b
	}
	sm.mu.RUnlock()
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if b, ok := sm.buffers[path]; ok {
		return b
	}
	b := newStreamBuffer(cfg)
	sm.buffers[path] = b
	return b
}

func (sm *streamManager) remove(path string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if b, ok := sm.buffers[path]; ok {
		b.close()
		delete(sm.buffers, path)
	}
}

func (sm *streamManager) closeAll() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for _, b := range sm.buffers {
		b.close()
	}
}

func (sm *streamManager) size(path string) int {
	sm.mu.RLock()
	b, ok := sm.buffers[path]
	sm.mu.RUnlock()
	if !ok {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}

// streamBuffer is a single-producer / single-consumer (or competing
// multi-consumer) byte ring with configurable backpressure.
type streamBuffer struct {
	mu          sync.Mutex
	cond        *sync.Cond
	buf         []byte
	r, w        int
	count       int
	capacity    int
	mode        BackpressureMode
	maxChunkSize int
	closed      bool
}

func newStreamBuffer(cfg *StreamConfig) *streamBuffer {
	cap := defaultStreamCapacity
	mode := BackpressureBlock
	chunkSize := streamReadChunk
	if cfg != nil {
		if cfg.Capacity > 0 {
			cap = cfg.Capacity
		}
		mode = cfg.Mode
		if cfg.MaxChunkSize > 0 {
			chunkSize = cfg.MaxChunkSize
		}
	}
	b := &streamBuffer{
		buf:          make([]byte, cap),
		capacity:     cap,
		mode:         mode,
		maxChunkSize: chunkSize,
	}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// read copies up to len(p) bytes from the ring into p. If the buffer is empty
// and nonblock is true it returns EAGAIN immediately; otherwise it blocks until
// data arrives or the buffer is closed.
func (b *streamBuffer) read(p []byte, nonblock bool) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.count == 0 {
		if b.closed {
			return 0, nil
		}
		if nonblock {
			return 0, posix(EAGAIN, OpRead, "", nil)
		}
		for b.count == 0 && !b.closed {
			b.cond.Wait()
		}
		if b.count == 0 {
			return 0, nil
		}
	}

	n := 0
	for n < len(p) && b.count > 0 {
		p[n] = b.buf[b.r]
		b.r = (b.r + 1) % b.capacity
		b.count--
		n++
	}
	b.cond.Broadcast()
	return n, nil
}

// write copies p into the ring. Behaviour depends on the backpressure mode:
//   - Block: waits until space is available (or returns EAGAIN if nonblock).
//   - Drop: discards the oldest bytes to make room for the new data.
//   - Error: returns ENOSPC immediately if the buffer cannot hold all of p.
func (b *streamBuffer) write(p []byte, nonblock bool) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return 0, posix(EPIPE, OpWrite, "", nil)
	}

	toWrite := p
	if b.maxChunkSize > 0 && len(p) > b.maxChunkSize {
		toWrite = p[:b.maxChunkSize]
	}

	switch b.mode {
	case BackpressureError:
		if len(toWrite) > b.capacity-b.count {
			return 0, posix(ENOSPC, OpWrite, "", nil)
		}

	case BackpressureDrop:
		for len(toWrite) > b.capacity-b.count {
			b.r = (b.r + 1) % b.capacity
			b.count--
		}

	case BackpressureBlock:
		for len(toWrite) > b.capacity-b.count && !b.closed {
			if nonblock {
				return 0, posix(EAGAIN, OpWrite, "", nil)
			}
			b.cond.Wait()
		}
		if b.closed {
			return 0, posix(EPIPE, OpWrite, "", nil)
		}
	}

	for i := range toWrite {
		b.buf[b.w] = toWrite[i]
		b.w = (b.w + 1) % b.capacity
		b.count++
	}
	b.cond.Broadcast()
	return len(toWrite), nil
}

func (b *streamBuffer) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.cond.Broadcast()
}
