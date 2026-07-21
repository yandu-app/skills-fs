package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// reservedPaths are blocked from user mounts to prevent shadowing system
// endpoints and adapter internals.
var reservedPaths = []string{"/sys", "/healthz", "/debug"}

func isReservedPath(p string) bool {
	for _, r := range reservedPaths {
		if p == r || strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

// audit returns a callback that should be deferred with the error result.
// When AuditFunc is nil the returned callback is a no-op.
func (fs *FileSystem) audit(op string, path string, caller CallerIdentity) func(error) {
	if fs.cfg.AuditFunc == nil {
		return func(error) {}
	}
	start := time.Now()
	return func(err error) {
		fs.cfg.AuditFunc(AuditEntry{
			Timestamp: start,
			Op:        op,
			Path:      path,
			Caller:    caller,
			Err:       err,
			Duration:  time.Since(start),
		})
	}
}

// normalizePath validates and canonicalizes a path. It rejects empty
// segments, ".", "..", and paths that do not start with "/".
func normalizePath(p string) (string, error) {
	if p == "" || p[0] != '/' {
		return "", posix(EINVAL, OpStat, p, nil)
	}
	if p == "/" {
		return p, nil
	}
	// Reject trailing slash (except root) to keep paths canonical.
	if strings.HasSuffix(p, "/") {
		return "", posix(EINVAL, OpStat, p, nil)
	}
	raw := strings.Split(p[1:], "/")
	for _, seg := range raw {
		if seg == "" || seg == "." || seg == ".." {
			return "", posix(EINVAL, OpStat, p, nil)
		}
	}
	return p, nil
}

type FileSystem struct {
	cfg             GlobalConfig
	router          *router
	providers       map[string]Provider
	handles         *handleManager
	locks           *lockManager
	streams         *streamManager
	metrics         *Metrics
	skills          *SkillGenerator
	events          *eventBus
	bufPool         sync.Pool
	breakers        map[string]*circuitBreaker
	breakersMu      sync.Mutex
	providerCacheMu sync.Mutex
	providerCache   map[string]providerCacheEntry
	cleanup         []func() error
	cleanupMu       sync.Mutex
	writeback sync.Map
	mu              sync.RWMutex
}

type providerCacheEntry struct {
	result  *ProviderResult
	expires time.Time
}

// NewFS creates a new virtual filesystem. Panics if cfg is invalid.
func NewFS(cfg GlobalConfig) *FileSystem {
	if err := cfg.Validate(); err != nil {
		panic("invalid GlobalConfig: " + err.Error())
	}
	cfg = cfg.withDefaults()
	fs := &FileSystem{
		cfg:           cfg,
		router:        newRouter(),
		providers:     make(map[string]Provider),
		handles:       newHandleManager(cfg.MaxOpenHandles),
		locks:         newLockManager(cfg.LockTimeout),
		streams:       newStreamManager(),
		metrics:       newMetrics(),
		skills:        NewSkillGenerator(cfg.SkillsRoot),
		events:        newEventBus(),
		breakers:      make(map[string]*circuitBreaker),
		providerCache: make(map[string]providerCacheEntry),
		bufPool: sync.Pool{
			New: func() any {
				b := make([]byte, streamReadChunk)
				return &b
			},
		},
	}
	fs.metrics.eventBus = fs.events
	return fs
}

// Skills returns the SkillGenerator for this filesystem.
func (fs *FileSystem) Skills() *SkillGenerator { return fs.skills }

// CloseAllHandles forcibly closes every open handle, flushing buffered writes
// and releasing advisory locks. Errors from individual closes are discarded;
// callers should treat the filesystem as unusable after this call.
func (fs *FileSystem) CloseAllHandles() {
	for _, h := range fs.handles.snapshot() {
		_ = h.Close(context.Background())
	}
}

// OnShutdown registers a cleanup hook that runs during Shutdown after all
// handles, streams, and events are torn down. Hooks run in reverse
// registration order (LIFO). Errors from individual hooks are discarded so
// that later hooks still execute.
func (fs *FileSystem) OnShutdown(fn func() error) {
	fs.cleanupMu.Lock()
	defer fs.cleanupMu.Unlock()
	fs.cleanup = append(fs.cleanup, fn)
}

// Shutdown performs a graceful teardown: it closes all handles with the
// provided context, closes all stream buffers, clears event notifiers, and
// runs any registered OnShutdown hooks. If ctx is cancelled or reaches its
// deadline, Shutdown returns immediately with ctx.Err() and some handles may
// remain open.
func (fs *FileSystem) Shutdown(ctx context.Context) error {
	for _, h := range fs.handles.snapshot() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		_ = h.Close(ctx)
	}
	fs.streams.closeAll()
	fs.events.clear()
	fs.runCleanup()
	return nil
}

func (fs *FileSystem) runCleanup() {
	fs.cleanupMu.Lock()
	hooks := make([]func() error, len(fs.cleanup))
	copy(hooks, fs.cleanup)
	fs.cleanup = nil
	fs.cleanupMu.Unlock()
	for i := len(hooks) - 1; i >= 0; i-- {
		_ = hooks[i]()
	}
}

// RegisterProvider adds a provider to the filesystem. Returns EEXIST if already registered.
func (fs *FileSystem) RegisterProvider(p Provider) error {
	if p == nil || p.ID() == "" {
		return posix(EINVAL, "", "", nil)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if _, exists := fs.providers[p.ID()]; exists {
		return posix(EEXIST, "", p.ID(), nil)
	}
	fs.providers[p.ID()] = p
	return nil
}

// ProviderHealth returns a map of provider ID to health status string.
// If a provider implements HealthCheckable, its HealthCheck is called.
// Otherwise it is reported as "unknown".
func (fs *FileSystem) ProviderHealth(ctx context.Context) map[string]string {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	result := make(map[string]string, len(fs.providers))
	for id, p := range fs.providers {
		if hc, ok := p.(HealthCheckable); ok {
			if err := hc.HealthCheck(ctx); err != nil {
				result[id] = "unhealthy: " + err.Error()
				continue
			}
			result[id] = "healthy"
		} else {
			result[id] = "unknown"
		}
	}
	return result
}

func (fs *FileSystem) RegisterNotifier(fn func(Event), prefix string) func() {
	id := fs.events.register(fn, prefix)
	return func() { fs.events.unregister(id) }
}

// Mount registers a node at the given path. Returns EEXIST if the path is taken,
// EINVAL if provider ops reference an unregistered provider, or EBUSY if MaxMounts is reached.
func (fs *FileSystem) Mount(p string, entry MountEntry) (err error) {
	done := fs.audit("mount", p, CallerIdentity{})
	defer func() { done(err) }()
	path, err := normalizePath(p)
	if err != nil {
		return err
	}
	if isReservedPath(path) {
		return posix(EINVAL, OpStat, path, nil)
	}
	if err := entry.Validate(); err != nil {
		return posix(EINVAL, OpStat, path, nil)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.cfg.MaxMounts > 0 && fs.router.count() >= fs.cfg.MaxMounts {
		return posix(ENOSPC, OpStat, path, nil)
	}
	if entry.Kind == KindBlob && int64(len(entry.BlobData)) > fs.cfg.MaxBlobSize {
		return posix(ENOSPC, OpStat, path, nil)
	}
	if len(fs.providers) == 0 && hasProviderOps(entry.Ops) {
		return posix(EINVAL, OpStat, path, nil)
	}
	entry.Path = path
	if entry.Kind == "" {
		entry.Kind = KindAPI
	}
	if entry.Mode == 0 {
		entry.Mode = 0o444
	}
	if entry.UID == 0 {
		entry.UID = fs.cfg.DefaultUID
	}
	if entry.GID == 0 {
		entry.GID = fs.cfg.DefaultGID
	}
	if entry.Serial {
		entry.serial = &serialQueue{}
	}
	for _, op := range entry.Ops {
		if op == nil {
			return posix(EINVAL, OpStat, path, nil)
		}
		if _, ok := fs.providers[op.ProviderID]; !ok {
			return posix(EINVAL, OpStat, path, nil)
		}
	}
	mounted, err := fs.router.add(entry)
	if err != nil {
		return err
	}
	if mounted.Skill != nil && mounted.Skill.Enabled {
		if err := fs.skills.Generate(*mounted.Skill); err != nil {
			_, _ = fs.router.remove(path)
			return err
		}
		if err := fs.MountSkillAtRoot(*mounted.Skill); err != nil {
			_, _ = fs.router.remove(path)
			return err
		}
	}
	fs.events.emit(Event{Path: path, Kind: EventCreate})
	return nil
}

// MountSkillAtRoot mounts a generated skill's SKILL.md at /SKILL.md in the
// virtual filesystem when cfg.ExposeAtRoot is true. The skill must already be
// generated (e.g. by fs.Skills().Generate). If AgentsTemplate is set, also
// exposes /AGENTS.md at the root.
func (fs *FileSystem) MountSkillAtRoot(cfg SkillConfig) error {
	if !cfg.ExposeAtRoot {
		return nil
	}
	data, err := fs.skills.ReadSkillFile(cfg.Name)
	if err != nil {
		return err
	}
	rootEntry := MountEntry{
		Path:     "/SKILL.md",
		Kind:     KindBlob,
		Mode:     0o444,
		BlobData: data,
		UID:      fs.cfg.DefaultUID,
		GID:      fs.cfg.DefaultGID,
	}
	if _, err := fs.router.add(rootEntry); err != nil {
		return err
	}
	fs.events.emit(Event{Path: "/SKILL.md", Kind: EventCreate})

	if cfg.AgentsTemplate != "" {
		agentsPath := filepath.Join(fs.skills.root, cfg.Name, "AGENTS.md")
		agentsData, err := os.ReadFile(agentsPath)
		if err != nil {
			return err
		}
		agentsEntry := MountEntry{
			Path:     "/AGENTS.md",
			Kind:     KindBlob,
			Mode:     0o444,
			BlobData: agentsData,
			UID:      fs.cfg.DefaultUID,
			GID:      fs.cfg.DefaultGID,
		}
		if _, err := fs.router.add(agentsEntry); err != nil {
			return err
		}
		fs.events.emit(Event{Path: "/AGENTS.md", Kind: EventCreate})
	}
	return nil
}

// UnmountSkillAtRoot removes the /SKILL.md and /AGENTS.md mounts created by
// MountSkillAtRoot.
func (fs *FileSystem) UnmountSkillAtRoot() error {
	_, _ = fs.router.remove("/AGENTS.md")
	_, err := fs.router.remove("/SKILL.md")
	return err
}
func (fs *FileSystem) Unmount(p string) (err error) {
	done := fs.audit("unmount", p, CallerIdentity{})
	defer func() { done(err) }()
	path, err := normalizePath(p)
	if err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	m, err := fs.router.remove(path)
	if err != nil {
		return err
	}
	if m.Skill != nil && m.Skill.Enabled {
		if err := fs.skills.Remove(m.Skill.Name); err != nil {
			return err
		}
	}
	fs.locks.purge(path)
	fs.streams.remove(path)
	fs.events.emit(Event{Path: path, Kind: EventRemove})
	return nil
}

// Remove is a semantic alias for Unmount.
func (fs *FileSystem) Remove(path string) error {
	return fs.Unmount(path)
}

// Rename moves a mount from oldPath to newPath, preserving its properties.
// It returns an error if oldPath does not exist or newPath is already mounted.
func (fs *FileSystem) Rename(oldPath, newPath string) (err error) {
	done := fs.audit("rename", oldPath+"->"+newPath, CallerIdentity{})
	defer func() { done(err) }()
	oldPath, err = normalizePath(oldPath)
	if err != nil {
		return err
	}
	newPath, err = normalizePath(newPath)
	if err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	m, err := fs.router.remove(oldPath)
	if err != nil {
		return err
	}
	m.Path = newPath
	if _, err := fs.router.add(*m); err != nil {
		// Attempt to restore original mount on failure.
		m.Path = oldPath
		_, _ = fs.router.add(*m)
		return err
	}
	fs.events.emit(Event{Path: oldPath, Kind: EventRemove})
	fs.events.emit(Event{Path: newPath, Kind: EventCreate})
	return nil
}

// Stat returns metadata for the node at path. Results are cached within StatCacheTTL.
func (fs *FileSystem) Stat(path string, caller CallerIdentity) (st Stat, err error) {
	done := fs.audit("stat", path, caller)
	defer func() { done(err) }()
	started := time.Now()
	defer func() { fs.metrics.record(OpStat, started, err) }()
	if path == "/sys" {
		return Stat{Path: path, Kind: KindDir, Mode: 0o555, UID: fs.cfg.DefaultUID, GID: fs.cfg.DefaultGID}, nil
	}
	if path == "/sys/metrics" {
		return Stat{Path: path, Kind: KindBlob, Mode: 0o444, UID: fs.cfg.DefaultUID, GID: fs.cfg.DefaultGID, Size: int64(len(fs.Prometheus()))}, nil
	}
	if path == "/skills" {
		return Stat{Path: path, Kind: KindDir, Mode: 0o555, UID: fs.cfg.DefaultUID, GID: fs.cfg.DefaultGID}, nil
	}
	if name, ok := skillDirPath(path); ok && fs.skills.Exists(name) {
		return Stat{Path: path, Kind: KindDir, Mode: 0o555, UID: fs.cfg.DefaultUID, GID: fs.cfg.DefaultGID}, nil
	} else if name, ok := skillFilePath(path); ok {
		data, readErr := fs.skills.ReadSkillFile(name)
		if readErr != nil {
			if !isPosix(readErr) {
				return Stat{}, posix(ENOENT, OpStat, path, readErr)
			}
			return Stat{}, readErr
		}
		return Stat{Path: path, Kind: KindBlob, Mode: 0o444, UID: fs.cfg.DefaultUID, GID: fs.cfg.DefaultGID, Size: int64(len(data))}, nil
	}
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	rm, err := fs.router.match(path)
	if err != nil {
		return Stat{}, err
	}
	m := rm.mount
	if !canAccessNamespace(caller, m) {
		return Stat{}, posix(ENOENT, OpStat, path, nil)
	}
	var size int64
	if m == nil {
		// Intermediate directory node (no mount, but has children).
		return Stat{Path: path, Kind: KindDir, Mode: 0o555, UID: fs.cfg.DefaultUID, GID: fs.cfg.DefaultGID, Size: 0}, nil
	}
	switch m.Kind {
	case KindBlob:
		size = int64(len(m.BlobData))
	case KindAPI:
		size = 0
	case KindDir, KindDynamicDir:
		size = 0
	case KindLink:
		size = int64(len(m.LinkPath))
	case KindStream:
		size = int64(fs.streams.size(path))
	default:
		return Stat{}, posix(EINVAL, OpStat, path, nil)
	}
	return Stat{Path: path, Kind: m.Kind, Mode: m.Mode, UID: m.UID, GID: m.GID, Size: size}, nil
}

// Readdir lists children of a directory node. Returns ENOTDIR if the path is not KindDir.
func (fs *FileSystem) Readdir(path string, caller CallerIdentity) (entries []DirEntry, err error) {
	done := fs.audit("readdir", path, caller)
	defer func() { done(err) }()
	started := time.Now()
	defer func() { fs.metrics.record(OpReaddir, started, err) }()
	if path == "/sys" {
		return []DirEntry{{Name: "metrics", Kind: KindBlob, Mode: 0o444}}, nil
	}
	if path == "/skills" {
		entries = make([]DirEntry, 0)
		for _, skill := range fs.skills.List() {
			entries = append(entries, DirEntry{Name: skill.Name, Kind: KindDir, Mode: 0o555})
		}
		fs.mu.RLock()
		routeEntries, routeErr := fs.router.list(path)
		fs.mu.RUnlock()
		if routeErr == nil {
			entries = mergeDirEntries(entries, routeEntries)
		}
		return entries, nil
	}
	if name, ok := skillDirPath(path); ok && fs.skills.Exists(name) {
		return []DirEntry{{Name: "SKILL.md", Kind: KindBlob, Mode: 0o444}}, nil
	}
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	var matchedMount *MountEntry
	var matchedParams ParamSet
	if path != "/" {
		rm, err := fs.router.match(path)
		if err == nil {
			m := rm.mount
			if m == nil {
				// Intermediate directory (no mount but has children) - skip mount-level access check.
			} else if !canAccessNamespace(caller, m) {
				return nil, posix(ENOENT, OpReaddir, path, nil)
			} else if !canAccess(caller, m.UID, m.GID, m.Mode, OpReaddir) {
				return nil, posix(EACCES, OpReaddir, path, nil)
			} else if m.Kind != KindDir && m.Kind != KindDynamicDir {
				return nil, posix(ENOTDIR, OpReaddir, path, nil)
			}
			matchedMount = m
			matchedParams = rm.params
		}
	}
	entries, err = fs.router.list(path)
	if err != nil {
		return nil, err
	}
	entries = filterDirEntriesByNamespace(fs.router, entries, path, caller)
	if matchedMount != nil && matchedMount.Kind == KindDynamicDir {
		dynamicEntries, err := fs.listDynamicDir(context.Background(), matchedMount, path, matchedParams, caller)
		if err == nil && len(dynamicEntries) > 0 {
			entries = mergeDirEntries(entries, dynamicEntries)
		}
	}
	// If the path is a dynamic directory, hide router param placeholders (e.g.
	// ":group_id") from the listing; they are routing internals, not browseable
	// entries. Static directories retain them for backward compatibility.
	if matchedMount != nil && matchedMount.Kind == KindDynamicDir {
		filtered := entries[:0]
		for _, e := range entries {
			if !strings.HasPrefix(e.Name, ":") {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}
	if path == "/" {
		entries = append(entries, DirEntry{Name: "sys", Kind: KindDir, Mode: 0o555})
		entries = append(entries, DirEntry{Name: "skills", Kind: KindDir, Mode: 0o555})
		entries = mergeDirEntries(nil, entries)
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	}
	return entries, nil
}

// filterDirEntriesByNamespace removes entries whose exact-path mount belongs
// to a different namespace. Intermediate directories (no mount at the exact
// path) are kept because they may contain accessible descendants.
func filterDirEntriesByNamespace(r *router, entries []DirEntry, parent string, caller CallerIdentity) []DirEntry {
	if caller.Namespace == "" {
		return entries
	}
	var out []DirEntry
	for _, e := range entries {
		childPath := parent + "/" + e.Name
		if parent == "/" {
			childPath = "/" + e.Name
		}
		rm, err := r.match(childPath)
		if err != nil {
			// No exact match: intermediate directory or param placeholder.
			// Keep it; deeper access will be checked at the leaf.
			out = append(out, e)
			continue
		}
		if canAccessNamespace(caller, rm.mount) {
			out = append(out, e)
		}
	}
	return out
}

// Read returns the contents of a blob, link, or API node.
// Returns EISDIR for directories, ENOSYS for streams, EACCES if read permission is missing.
func (fs *FileSystem) Read(ctx context.Context, path string, caller CallerIdentity) (data []byte, err error) {
	done := fs.audit("read", path, caller)
	defer func() { done(err) }()
	started := time.Now()
	defer func() { fs.metrics.record(OpRead, started, err) }()
	if path == "/sys/metrics" {
		return fs.Prometheus(), nil
	}
	if name, ok := skillFilePath(path); ok {
		data, readErr := fs.skills.ReadSkillFile(name)
		if readErr != nil {
			err = readErr
			return nil, readErr
		}
		return data, nil
	}
	fs.mu.RLock()
	rm, err := fs.router.match(path)
	if err != nil {
		fs.mu.RUnlock()
		return nil, err
	}
	m := rm.mount
	if m == nil {
		fs.mu.RUnlock()
		return nil, posix(ENOTDIR, OpRead, path, fmt.Errorf("not a readable path: %s", path))
	}
	if !canAccessNamespace(caller, m) {
		fs.mu.RUnlock()
		return nil, posix(ENOENT, OpRead, path, nil)
	}
	if !canAccess(caller, m.UID, m.GID, m.Mode, OpRead) {
		fs.mu.RUnlock()
		return nil, posix(EACCES, OpRead, path, nil)
	}
	switch m.Kind {
	case KindBlob:
		data := append([]byte(nil), m.BlobData...)
		fs.mu.RUnlock()
		return data, nil
	case KindAPI:
		// Writeback: if this mount has writeback enabled, check for stored result first.
		if m.Writeback {
			if cached, ok := fs.writeback.Load(path); ok {
				data := make([]byte, len(cached.([]byte)))
				copy(data, cached.([]byte))
				fs.mu.RUnlock()
				return data, nil
			}
		}
		cap, provider, err := fs.providerFor(m, OpRead, path)
		params := rm.params
		fs.mu.RUnlock()
		if err != nil {
			return nil, err
		}
		if cap.Async {
			// #nosec G118 -- async provider call must outlive the request context.
			go func() {
				_, err := fs.invokeProvider(context.Background(), provider, cap, OpRead, path, params, nil, caller)
				fs.recordBreakerResult(cap.ProviderID, err == nil)
			}()
			return []byte{}, nil
		}
		data, err := fs.invokeProvider(ctx, provider, cap, OpRead, path, params, nil, caller)
		fs.recordBreakerResult(cap.ProviderID, err == nil)
		return data, err
	case KindLink:
		target := []byte(m.LinkPath)
		fs.mu.RUnlock()
		return target, nil
	case KindStream:
		cfg := m.Stream
		fs.mu.RUnlock()
		b := fs.streams.getOrCreate(path, cfg)
		bufPtr := fs.bufPool.Get().(*[]byte)
		buf := *bufPtr
		readBuf := buf
		if cfg != nil && cfg.MaxChunkSize > 0 && cfg.MaxChunkSize < len(buf) {
			readBuf = buf[:cfg.MaxChunkSize]
		}
		n, err := b.read(readBuf, false)
		if err != nil {
			fs.bufPool.Put(bufPtr)
			return nil, err
		}
		result := make([]byte, n)
		copy(result, buf[:n])
		fs.bufPool.Put(bufPtr)
		return result, nil
	case KindDir, KindDynamicDir:
		fs.mu.RUnlock()
		return nil, posix(EISDIR, OpRead, path, nil)
	default:
		fs.mu.RUnlock()
		return nil, posix(ENOSYS, OpRead, path, nil)
	}
}

// Write replaces blob data or dispatches a write to the API provider.
// For blob mounts the payload replaces the entire content.
// Invalidates the stat cache on success.
func (fs *FileSystem) Write(ctx context.Context, path string, payload []byte, caller CallerIdentity) (err error) {
	done := fs.audit("write", path, caller)
	defer func() { done(err) }()
	started := time.Now()
	defer func() { fs.metrics.record(OpWrite, started, err) }()
	fs.mu.RLock()
	rm, err := fs.router.match(path)
	if err != nil {
		fs.mu.RUnlock()
		return err
	}
	m := rm.mount
	if m == nil {
		fs.mu.RUnlock()
		return posix(ENOTDIR, OpWrite, path, fmt.Errorf("not a writable path: %s", path))
	}
	if !canAccessNamespace(caller, m) {
		fs.mu.RUnlock()
		return posix(ENOENT, OpWrite, path, nil)
	}
	if !canAccess(caller, m.UID, m.GID, m.Mode, OpWrite) {
		fs.mu.RUnlock()
		return posix(EACCES, OpWrite, path, nil)
	}
	switch m.Kind {
	case KindBlob:
		fs.mu.RUnlock()
		fs.mu.Lock()
		defer fs.mu.Unlock()
		rm, err := fs.router.match(path)
		if err != nil {
			return err
		}
		m = rm.mount
		if !canAccess(caller, m.UID, m.GID, m.Mode, OpWrite) {
			return posix(EACCES, OpWrite, path, nil)
		}
		if int64(len(payload)) > fs.cfg.MaxBlobSize {
			return posix(ENOSPC, OpWrite, path, nil)
		}
		m.BlobData = append(m.BlobData[:0], payload...)
		fs.events.emit(Event{Path: path, Kind: EventWrite})
		return nil
	case KindAPI:
		cap, provider, err := fs.providerFor(m, OpWrite, path)
		params := rm.params
		serial := m.serial
		writeback := m.Writeback
		schema := m.Schema
		p := path // capture before releasing lock
		fs.mu.RUnlock()
		if err != nil {
			return err
		}
		return serial.run(func() error {
			data, err := fs.invokeProvider(ctx, provider, cap, OpWrite, path, params, payload, caller)
			fs.recordBreakerResult(cap.ProviderID, err == nil)
			if writeback {
				if err != nil {
					fs.writeback.Store(p, []byte(schemaErrJSON(schema, err, p)))
					return nil // treat as success so FUSE write doesn't fail
				}
				if data != nil {
					fs.writeback.Store(p, data)
				}
			}
			return err
		})
	case KindStream:
		cfg := m.Stream
		fs.mu.RUnlock()
		b := fs.streams.getOrCreate(path, cfg)
		for len(payload) > 0 {
			n, err := b.write(payload, false)
			if err != nil {
				return err
			}
			payload = payload[n:]
		}
		fs.events.emit(Event{Path: path, Kind: EventWrite})
		return nil
	default:
		fs.mu.RUnlock()
		return posix(ENOSYS, OpWrite, path, nil)
	}
}

func (fs *FileSystem) Resolve(path string) (MountEntry, map[string]string, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	rm, err := fs.router.match(path)
	if err != nil {
		return MountEntry{}, nil, err
	}
	if rm.mount == nil {
		return MountEntry{}, rm.params.ToMap(), fmt.Errorf("not a mounted path: %s", path)
	}
	return *rm.mount, rm.params.ToMap(), nil
}

func (fs *FileSystem) ResolveParams(path string) (MountEntry, ParamSet, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	rm, err := fs.router.match(path)
	if err != nil {
		return MountEntry{}, ParamSet{}, err
	}
	if rm.mount == nil {
		return MountEntry{}, rm.params, fmt.Errorf("not a mounted path: %s", path)
	}
	return *rm.mount, rm.params, nil
}

// Snapshot returns a copy of every mounted entry in the namespace.
func (fs *FileSystem) Snapshot() []MountEntry {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	return fs.router.snapshot()
}

// Restore mounts a slice of entries, clearing existing mounts first.
// It returns the first error encountered without rolling back.
func (fs *FileSystem) Restore(entries []MountEntry) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for _, e := range fs.router.snapshot() {
		_, _ = fs.router.remove(e.Path)
	}
	for _, e := range entries {
		if _, err := fs.router.add(e); err != nil {
			return err
		}
	}
	return nil
}

// DiffSnapshots compares two mount entry slices and returns added, removed,
// and modified entries. Comparison ignores runtime-only fields (serial, ID).
func DiffSnapshots(old, new []MountEntry) SnapshotDiff {
	oldMap := make(map[string]MountEntry, len(old))
	for _, e := range old {
		oldMap[e.Path] = e
	}
	newMap := make(map[string]MountEntry, len(new))
	for _, e := range new {
		newMap[e.Path] = e
	}

	var diff SnapshotDiff
	for _, e := range new {
		if prev, ok := oldMap[e.Path]; !ok {
			diff.Added = append(diff.Added, e)
		} else if !mountEntryEqual(prev, e) {
			diff.Modified = append(diff.Modified, MountEntryChange{Path: e.Path, Old: prev, New: e})
		}
	}
	for _, e := range old {
		if _, ok := newMap[e.Path]; !ok {
			diff.Removed = append(diff.Removed, e)
		}
	}
	return diff
}

func mountEntryEqual(a, b MountEntry) bool {
	if a.Kind != b.Kind || a.Mode != b.Mode || a.UID != b.UID || a.GID != b.GID {
		return false
	}
	if a.LinkPath != b.LinkPath || a.Visibility != b.Visibility {
		return false
	}
	if !bytes.Equal(a.BlobData, b.BlobData) {
		return false
	}
	if (a.Stream == nil) != (b.Stream == nil) {
		return false
	}
	if a.Stream != nil && b.Stream != nil {
		if a.Stream.Capacity != b.Stream.Capacity || a.Stream.Mode != b.Stream.Mode {
			return false
		}
	}
	if (a.Skill == nil) != (b.Skill == nil) {
		return false
	}
	if a.Skill != nil && b.Skill != nil {
		if a.Skill.Name != b.Skill.Name || a.Skill.Enabled != b.Skill.Enabled {
			return false
		}
	}
	return true
}

// ReadLink returns the target of a symlink without following it.
func (fs *FileSystem) ReadLink(path string) (string, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	rm, err := fs.router.match(path)
	if err != nil {
		return "", err
	}
	if rm.mount.Kind != KindLink {
		return "", posix(EINVAL, OpRead, path, nil)
	}
	return rm.mount.LinkPath, nil
}

// FollowLink resolves a path by following symlinks. It returns the final
// resolved path or an error if a loop is detected or a link target is invalid.
func (fs *FileSystem) FollowLink(p string) (string, error) {
	const maxDepth = 16
	for i := 0; i < maxDepth; i++ {
		target, err := fs.ReadLink(p)
		if err != nil {
			var pe *PosixError
			if errors.As(err, &pe) && pe.Code == EINVAL {
				// Not a symlink — return the path as-is.
				return p, nil
			}
			return "", err
		}
		if !strings.HasPrefix(target, "/") {
			dir := p[:strings.LastIndex(p, "/")]
			if dir == "" {
				dir = "/"
			}
			target = path.Join(dir, target)
		}
		p = target
	}
	return "", posix(ELOOP, OpRead, p, nil)
}

func (fs *FileSystem) providerFor(m *MountEntry, op OpCode, path string) (*CapConfig, Provider, error) {
	cap := m.Ops[op]
	if cap == nil {
		return nil, nil, posix(ENOSYS, op, path, nil)
	}
	provider := fs.providers[cap.ProviderID]
	if provider == nil {
		return nil, nil, posix(ECOMM, op, path, nil)
	}
	if fs.breakerOpen(cap.ProviderID) {
		return nil, nil, posix(ECOMM, op, path, fmt.Errorf("circuit breaker open for provider %s", cap.ProviderID))
	}
	return cap, provider, nil
}

func (fs *FileSystem) invokeProvider(ctx context.Context, provider Provider, cap *CapConfig, op OpCode, path string, pathParams ParamSet, payload []byte, caller CallerIdentity) ([]byte, error) {
	if cap.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cap.Timeout)
		defer cancel()
	}
	params := map[string]interface{}{}
	pathParams.Each(func(k, v string) {
		params[k] = v
	})
	var err error
	if cap.ParamsFn != nil {
		params, err = cap.ParamsFn(pathParams.ToMap(), payload, OpContext{Path: path, Op: op, Caller: caller})
		if err != nil {
			return nil, posix(EINVAL, op, path, err)
		}
	}

	cacheKey := ""
	if cap.CacheTTL > 0 {
		cacheKey = fmt.Sprintf("%s|%s|%v", cap.ProviderID, cap.Action, params)
		fs.providerCacheMu.Lock()
		ent, ok := fs.providerCache[cacheKey]
		fs.providerCacheMu.Unlock()
		if ok && time.Now().Before(ent.expires) {
			fs.metrics.recordCacheHit()
			return ent.result.Data, nil
		}
		fs.metrics.recordCacheMiss()
	}

	result, err := provider.Invoke(ctx, cap.Action, params)
	if err != nil {
		return nil, MapProviderError(err, op, path)
	}
	if result == nil {
		return nil, nil
	}

	if cap.CacheTTL > 0 && cacheKey != "" {
		fs.providerCacheMu.Lock()
		fs.providerCache[cacheKey] = providerCacheEntry{result: result, expires: time.Now().Add(cap.CacheTTL)}
		fs.providerCacheMu.Unlock()
	}
	return result.Data, nil
}

func hasProviderOps(ops map[OpCode]*CapConfig) bool {
	return len(ops) > 0
}

// Prometheus returns the complete set of metrics in Prometheus text format,
// including operation histograms, event counters, and runtime gauges.
func (fs *FileSystem) Prometheus() []byte {
	var b strings.Builder
	b.Write(fs.metrics.Prometheus())

	fs.mu.RLock()
	mounts := fs.router.count()
	providers := len(fs.providers)
	fs.mu.RUnlock()

	handles := fs.handles.Active()

	b.WriteString("# TYPE skills_fs_mounts_total gauge\n")
	fmt.Fprintf(&b, "skills_fs_mounts_total %d\n", mounts)
	b.WriteString("# TYPE skills_fs_handles_active gauge\n")
	fmt.Fprintf(&b, "skills_fs_handles_active %d\n", handles)
	b.WriteString("# TYPE skills_fs_providers_total gauge\n")
	fmt.Fprintf(&b, "skills_fs_providers_total %d\n", providers)

	fs.breakersMu.Lock()
	if len(fs.breakers) > 0 {
		b.WriteString("# TYPE skills_fs_breaker_state gauge\n")
		for id, cb := range fs.breakers {
			st := cb.currentState()
			fmt.Fprintf(&b, "skills_fs_breaker_state{provider=%q} %d\n", id, st)
		}
	}
	fs.breakersMu.Unlock()

	return []byte(b.String())
}

// circuit breaker states.
const (
	cbClosed = iota
	cbOpen
	cbHalfOpen
)

type circuitBreaker struct {
	state       int
	failures    int
	successes   int
	lastFailure time.Time
	mu          sync.Mutex
}

func (fs *FileSystem) breakerFor(id string) *circuitBreaker {
	fs.breakersMu.Lock()
	defer fs.breakersMu.Unlock()
	b, ok := fs.breakers[id]
	if !ok {
		b = &circuitBreaker{state: cbClosed}
		fs.breakers[id] = b
	}
	return b
}

func (fs *FileSystem) breakerOpen(id string) bool {
	if !fs.cfg.Breaker.Enabled {
		return false
	}
	b := fs.breakerFor(id)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == cbOpen {
		if time.Since(b.lastFailure) > fs.cfg.Breaker.ResetTimeout {
			b.state = cbHalfOpen
			b.failures = 0
			b.successes = 0
			return false
		}
		return true
	}
	return false
}

func (cb *circuitBreaker) currentState() int {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

func (fs *FileSystem) recordBreakerResult(id string, success bool) {
	if !fs.cfg.Breaker.Enabled {
		return
	}
	b := fs.breakerFor(id)
	b.mu.Lock()
	defer b.mu.Unlock()
	if success {
		if b.state == cbHalfOpen {
			b.successes++
			if b.successes >= fs.cfg.Breaker.HalfOpenMaxCalls {
				b.state = cbClosed
				b.failures = 0
				b.successes = 0
			}
		} else {
			b.failures = 0
		}
	} else {
		b.failures++
		b.lastFailure = time.Now()
		if b.failures >= fs.cfg.Breaker.FailureThreshold {
			b.state = cbOpen
		}
	}
}

func skillDirPath(path string) (string, bool) {
	const prefix = "/skills/"
	if !strings.HasPrefix(path, prefix) || strings.Contains(path[len(prefix):], "/") {
		return "", false
	}
	name := path[len(prefix):]
	return name, skillNameRE.MatchString(name)
}

func skillFilePath(path string) (string, bool) {
	const prefix = "/skills/"
	const suffix = "/SKILL.md"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	name := strings.TrimSuffix(path[len(prefix):], suffix)
	return name, skillNameRE.MatchString(name)
}

func (fs *FileSystem) listDynamicDir(ctx context.Context, m *MountEntry, path string, params ParamSet, caller CallerIdentity) ([]DirEntry, error) {
	cap, provider, err := fs.providerFor(m, OpRead, path)
	if err != nil {
		return nil, err
	}
	data, err := fs.invokeProvider(ctx, provider, cap, OpRead, path, params, nil, caller)
	if err != nil {
		return nil, err
	}
	entries, err := parseDynamicEntries(data)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		childPath := path
		if path == "/" {
			childPath = "/" + entries[i].Name
		} else {
			childPath = path + "/" + entries[i].Name
		}
		// If the provider did not specify a kind, try to infer it from any
		// nested mount registered at the child path.
		if entries[i].Kind == "" {
		if rm, err := fs.router.match(childPath); err == nil && rm.mount != nil {
			entries[i].Kind = rm.mount.Kind
			entries[i].Mode = rm.mount.Mode
		} else {
			entries[i].Kind = KindDir
			entries[i].Mode = 0o555
		}
		}
	}
	return entries, nil
}

type dynamicEntry struct {
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
	Mode string `json:"mode,omitempty"`
}

func parseDynamicEntries(data []byte) ([]DirEntry, error) {
	// First try the simple array-of-names format: ["a", "b"]
	var names []string
	if err := json.Unmarshal(data, &names); err == nil {
		entries := make([]DirEntry, 0, len(names))
		for _, name := range names {
			entries = append(entries, DirEntry{Name: name})
		}
		return entries, nil
	}
	// Then try the object format: {"entries": [{"name": "a"}, ...]}
	var wrapped struct {
		Entries []dynamicEntry `json:"entries"`
	}
	if err := json.Unmarshal(data, &wrapped); err == nil && wrapped.Entries != nil {
		entries := make([]DirEntry, 0, len(wrapped.Entries))
		for _, de := range wrapped.Entries {
			var mode uint32
			if de.Mode != "" {
				m, err := strconv.ParseUint(de.Mode, 8, 32)
				if err == nil {
					mode = uint32(m)
				}
			}
			entries = append(entries, DirEntry{Name: de.Name, Kind: NodeKind(de.Kind), Mode: mode})
		}
		return entries, nil
	}
	return nil, fmt.Errorf("dynamic directory entries must be a JSON array of names or {\"entries\": [{\"name\":\"...\"}]}")
}

func mergeDirEntries(a, b []DirEntry) []DirEntry {
	seen := make(map[string]DirEntry, len(a)+len(b))
	for _, entry := range a {
		seen[entry.Name] = entry
	}
	for _, entry := range b {
		if _, ok := seen[entry.Name]; !ok {
			seen[entry.Name] = entry
		}
	}
	out := make([]DirEntry, 0, len(seen))
	for _, entry := range seen {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// schemaErrJSON builds a JSON error response with the expected schema for
// writeback mounts. When a write fails (JSON parse error or provider error),
// the resulting JSON is stored so a subsequent read returns the schema hint.
func schemaErrJSON(schema string, err error, p string) []byte {
	// Try to parse schema as JSON for pretty embedding; fall back to raw string.
	var schemaObj interface{}
	schemaRaw := json.RawMessage{}
	if schema != "" {
		if jerr := json.Unmarshal([]byte(schema), &schemaObj); jerr != nil {
			// Not valid JSON object — treat as raw text.
			schemaObj = schema
		} else if jerr2 := json.Unmarshal([]byte(schema), &schemaRaw); jerr2 == nil {
			// Valid JSON — embed as raw message so it preserves formatting.
			schemaObj = schemaRaw
		}
	}
	b, _ := json.Marshal(map[string]interface{}{
		"error":           err.Error(),
		"expected_schema": schemaObj,
		"hint":            "write valid JSON matching expected_schema; see " + p + ".schema",
	})
	return b
}
