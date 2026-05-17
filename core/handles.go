package core

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// OpenFlags mirror the POSIX flags that FUSE and WebDAV adapters translate
// from kernel open() calls. Only the bits relevant to the embedded engine
// are defined here; richer expansion belongs in adapter packages.
type OpenFlags uint32

const (
	OpenRead OpenFlags = 1 << iota
	OpenWrite
	OpenAppend
	OpenNonBlock
)

// Has reports whether the flag set contains the given flag.
func (f OpenFlags) Has(flag OpenFlags) bool { return f&flag != 0 }

// WriteMode selects whether a handle writes synchronously to its provider or
// coalesces payloads into a buffer flushed on size, delay, or newline.
type WriteMode int

const (
	WriteImmediate WriteMode = iota
	WriteBuffered
)

// WriteBufferPolicy describes how buffered writes are coalesced before being
// flushed through to the mount provider. The zero value yields immediate
// writes; setting Mode = WriteBuffered enables buffering, with MaxSize,
// MaxDelay, and FlushOnNewline acting as the three trigger conditions.
type WriteBufferPolicy struct {
	Mode           WriteMode
	MaxSize        int
	MaxDelay       time.Duration
	FlushOnNewline bool
}

// Handle represents an open reference to a mounted node. Handles are
// goroutine-safe across method calls — internal mutexes serialise the
// buffer state and lock transitions.
type Handle struct {
	id       uint64
	fs       *FileSystem
	path     string
	mount    *MountEntry
	caller   CallerIdentity
	flags    OpenFlags
	closed   atomic.Bool
	lockKind LockKind
	mu       sync.Mutex
	buf      []byte
	policy   WriteBufferPolicy
	timer    *time.Timer
	timerCh  chan struct{}
}

// ID returns the unique identifier assigned by the handle manager.
func (h *Handle) ID() uint64 { return h.id }

// Path returns the mounted path the handle was opened against.
func (h *Handle) Path() string { return h.path }

// Caller returns the identity captured at open time.
func (h *Handle) Caller() CallerIdentity { return h.caller }

// LockKind reports the advisory lock currently held by the handle.
func (h *Handle) LockKind() LockKind { return h.lockKind }

// handleManager keeps open handles addressable by id and enforces the
// MaxOpenHandles budget. It uses sharded maps so the hot path scales with
// concurrent opens.
type handleManager struct {
	shards [handleShardCount]handleShard
	seq    atomic.Uint64
	active atomic.Int64
	max    int64
}

const handleShardCount = 16

type handleShard struct {
	mu      sync.RWMutex
	entries map[uint64]*Handle
}

func newHandleManager(maxOpen int) *handleManager {
	m := &handleManager{max: int64(maxOpen)}
	for i := range m.shards {
		m.shards[i].entries = make(map[uint64]*Handle)
	}
	return m
}

func (m *handleManager) shardOf(id uint64) *handleShard {
	return &m.shards[id%handleShardCount]
}

func (m *handleManager) acquire() (uint64, error) {
	if m.max <= 0 {
		return m.seq.Add(1), nil
	}
	for {
		active := m.active.Load()
		if active >= m.max {
			return 0, posix(EBUSY, "", "", nil)
		}
		if m.active.CompareAndSwap(active, active+1) {
			return m.seq.Add(1), nil
		}
	}
}

func (m *handleManager) put(h *Handle) {
	s := m.shardOf(h.id)
	s.mu.Lock()
	s.entries[h.id] = h
	s.mu.Unlock()
}

func (m *handleManager) get(id uint64) (*Handle, bool) {
	s := m.shardOf(id)
	s.mu.RLock()
	h, ok := s.entries[id]
	s.mu.RUnlock()
	return h, ok
}

func (m *handleManager) drop(id uint64) {
	s := m.shardOf(id)
	s.mu.Lock()
	if _, ok := s.entries[id]; ok {
		delete(s.entries, id)
		if m.max > 0 {
			m.active.Add(-1)
		}
	}
	s.mu.Unlock()
}

// Active reports the number of open handles for observability.
func (m *handleManager) Active() int64 { return m.active.Load() }

func (m *handleManager) snapshot() []*Handle {
	var out []*Handle
	for i := range m.shards {
		s := &m.shards[i]
		s.mu.RLock()
		for _, h := range s.entries {
			out = append(out, h)
		}
		s.mu.RUnlock()
	}
	return out
}

// Open registers a handle for the given path. The mount must permit the
// operation implied by flags or EACCES is returned. Buffer policy is captured
// from the mount entry so subsequent writes coalesce correctly.
func (fs *FileSystem) Open(path string, flags OpenFlags, caller CallerIdentity) (*Handle, error) {
	if !flags.Has(OpenRead) && !flags.Has(OpenWrite) && !flags.Has(OpenAppend) {
		return nil, posix(EINVAL, OpStat, path, nil)
	}
	fs.mu.RLock()
	rm, err := fs.router.match(path)
	if err != nil {
		fs.mu.RUnlock()
		return nil, err
	}
	m := rm.mount
	if !canAccessNamespace(caller, m) {
		fs.mu.RUnlock()
		return nil, posix(ENOENT, OpStat, path, nil)
	}
	if flags.Has(OpenRead) && !canAccess(caller, m.UID, m.GID, m.Mode, OpRead) {
		fs.mu.RUnlock()
		return nil, posix(EACCES, OpRead, path, nil)
	}
	if (flags.Has(OpenWrite) || flags.Has(OpenAppend)) && !canAccess(caller, m.UID, m.GID, m.Mode, OpWrite) {
		fs.mu.RUnlock()
		return nil, posix(EACCES, OpWrite, path, nil)
	}
	policy := WriteBufferPolicy{}
	if m.BufferPolicy != nil {
		policy = *m.BufferPolicy
	}
	fs.mu.RUnlock()
	id, err := fs.handles.acquire()
	if err != nil {
		return nil, err
	}
	h := &Handle{
		id:     id,
		fs:     fs,
		path:   path,
		mount:  m,
		caller: caller,
		flags:  flags,
		policy: policy,
	}
	fs.handles.put(h)
	return h, nil
}

// Close releases the handle, flushing any buffered writes and releasing any
// advisory lock the handle still owns.
func (h *Handle) Close(ctx context.Context) error {
	if !h.closed.CompareAndSwap(false, true) {
		return posix(EBUSY, "", h.path, nil)
	}
	h.mu.Lock()
	flushErr := h.flushAllLocked(ctx)
	h.stopTimerLocked()
	h.mu.Unlock()
	var lockErr error
	if h.lockKind != LockNone {
		lockErr = h.fs.locks.release(h.path, h)
	}
	h.fs.handles.drop(h.id)
	if flushErr != nil {
		return flushErr
	}
	return lockErr
}

// ReadAll returns the next chunk for the handle. For blob/link/dir mounts
// this is the full content; for API mounts it invokes the provider.
func (h *Handle) ReadAll(ctx context.Context) ([]byte, error) {
	if h.closed.Load() {
		return nil, posix(EBUSY, OpRead, h.path, nil)
	}
	if !h.flags.Has(OpenRead) {
		return nil, posix(EACCES, OpRead, h.path, nil)
	}
	if h.mount.Kind == KindStream {
		b := h.fs.streams.getOrCreate(h.path, h.mount.Stream)
		buf := make([]byte, b.maxChunkSize)
		n, err := b.read(buf, h.flags.Has(OpenNonBlock))
		if err != nil {
			return nil, err
		}
		return buf[:n], nil
	}
	return h.fs.Read(ctx, h.path, h.caller)
}

// Write writes payload through the handle, honouring buffer policy and the
// underlying mount's serial queue.
func (h *Handle) Write(ctx context.Context, payload []byte) error {
	if h.closed.Load() {
		return posix(EBUSY, OpWrite, h.path, nil)
	}
	if !h.flags.Has(OpenWrite) && !h.flags.Has(OpenAppend) {
		return posix(EACCES, OpWrite, h.path, nil)
	}
	if h.mount.Kind == KindStream {
		b := h.fs.streams.getOrCreate(h.path, h.mount.Stream)
		for len(payload) > 0 {
			n, err := b.write(payload, h.flags.Has(OpenNonBlock))
			if err != nil {
				return err
			}
			payload = payload[n:]
		}
		return nil
	}
	if h.policy.Mode != WriteBuffered {
		return h.fs.Write(ctx, h.path, payload, h.caller)
	}
	h.mu.Lock()
	h.buf = append(h.buf, payload...)
	if h.policy.FlushOnNewline {
		if idx := bytes.LastIndexByte(h.buf, '\n'); idx >= 0 {
			if err := h.flushPrefixLocked(ctx, idx+1); err != nil {
				h.mu.Unlock()
				return err
			}
		}
	}
	maxSize := h.policy.MaxSize
	if maxSize <= 0 {
		maxSize = 64 * 1024
	}
	if len(h.buf) >= maxSize {
		if err := h.flushAllLocked(ctx); err != nil {
			h.mu.Unlock()
			return err
		}
	}
	if len(h.buf) > 0 && h.policy.MaxDelay > 0 && h.timer == nil {
		h.startTimerLocked()
	}
	h.mu.Unlock()
	return nil
}

// Flush forces any buffered writes through to the provider. Adapters map
// fsync to this method.
func (h *Handle) Flush(ctx context.Context) error {
	if h.closed.Load() {
		return posix(EBUSY, OpWrite, h.path, nil)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.flushAllLocked(ctx)
}

// Flock acquires an advisory lock on the handle. With nonblock=true the call
// returns EBUSY immediately if the lock is contended.
func (h *Handle) Flock(ctx context.Context, kind LockKind, nonblock bool) error {
	if h.closed.Load() {
		return posix(EBUSY, OpWrite, h.path, nil)
	}
	if kind == LockNone {
		return h.Funlock()
	}
	return h.fs.locks.acquire(ctx, h.path, h, kind, nonblock)
}

// Funlock releases any lock the handle currently holds.
func (h *Handle) Funlock() error {
	if h.closed.Load() {
		return posix(EBUSY, OpWrite, h.path, nil)
	}
	if h.lockKind == LockNone {
		return nil
	}
	return h.fs.locks.release(h.path, h)
}

func (h *Handle) flushAllLocked(ctx context.Context) error {
	if len(h.buf) == 0 {
		h.stopTimerLocked()
		return nil
	}
	return h.flushPrefixLocked(ctx, len(h.buf))
}

func (h *Handle) flushPrefixLocked(ctx context.Context, n int) error {
	if n <= 0 {
		return nil
	}
	payload := append([]byte(nil), h.buf[:n]...)
	if err := h.fs.Write(ctx, h.path, payload, h.caller); err != nil {
		return err
	}
	copy(h.buf, h.buf[n:])
	h.buf = h.buf[:len(h.buf)-n]
	if len(h.buf) == 0 {
		h.stopTimerLocked()
	}
	return nil
}

func (h *Handle) startTimerLocked() {
	stop := make(chan struct{})
	h.timerCh = stop
	h.timer = time.AfterFunc(h.policy.MaxDelay, func() {
		select {
		case <-stop:
			return
		default:
		}
		_ = h.Flush(context.Background())
	})
}

func (h *Handle) stopTimerLocked() {
	if h.timer != nil {
		h.timer.Stop()
		close(h.timerCh)
		h.timer = nil
		h.timerCh = nil
	}
}
