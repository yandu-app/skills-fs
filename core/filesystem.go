package core

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

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
	cfg       GlobalConfig
	router    *router
	providers map[string]Provider
	handles   *handleManager
	locks     *lockManager
	streams   *streamManager
	metrics   *Metrics
	skills    *SkillGenerator
	events    *eventBus
	mu        sync.RWMutex
}

func NewFS(cfg GlobalConfig) *FileSystem {
	cfg = cfg.withDefaults()
	return &FileSystem{
		cfg:       cfg,
		router:    newRouter(),
		providers: make(map[string]Provider),
		handles:   newHandleManager(cfg.MaxOpenHandles),
		locks:     newLockManager(),
		streams:   newStreamManager(),
		metrics:   newMetrics(),
		skills:    NewSkillGenerator(cfg.SkillsRoot),
		events:    newEventBus(),
	}
}

// CloseAllHandles forcibly closes every open handle, flushing buffered writes
// and releasing advisory locks. Errors from individual closes are discarded;
// callers should treat the filesystem as unusable after this call.
func (fs *FileSystem) CloseAllHandles() {
	for _, h := range fs.handles.snapshot() {
		_ = h.Close(context.Background())
	}
}

// Shutdown performs a graceful teardown: it closes all handles, closes all
// stream buffers, and clears event notifiers. After Shutdown the filesystem
// should not be used.
func (fs *FileSystem) Shutdown(ctx context.Context) error {
	fs.CloseAllHandles()
	fs.streams.closeAll()
	fs.events.clear()
	return nil
}

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

func (fs *FileSystem) RegisterNotifier(fn func(Event), prefix string) func() {
	id := fs.events.register(fn, prefix)
	return func() { fs.events.unregister(id) }
}

func (fs *FileSystem) Mount(p string, entry MountEntry) error {
	path, err := normalizePath(p)
	if err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
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
	}
	fs.events.emit(Event{Path: path, Kind: EventCreate})
	return nil
}

func (fs *FileSystem) Unmount(p string) error {
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
func (fs *FileSystem) Rename(oldPath, newPath string) error {
	oldPath, err := normalizePath(oldPath)
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

func (fs *FileSystem) Stat(path string, caller CallerIdentity) (Stat, error) {
	started := time.Now()
	var err error
	defer func() { fs.metrics.record(OpStat, started, err) }()
	if path == "/sys" {
		return Stat{Path: path, Kind: KindDir, Mode: 0o555, UID: fs.cfg.DefaultUID, GID: fs.cfg.DefaultGID}, nil
	}
	if path == "/sys/metrics" {
		return Stat{Path: path, Kind: KindBlob, Mode: 0o444, UID: fs.cfg.DefaultUID, GID: fs.cfg.DefaultGID, Size: int64(len(fs.metrics.Prometheus()))}, nil
	}
	if path == "/skills" {
		return Stat{Path: path, Kind: KindDir, Mode: 0o555, UID: fs.cfg.DefaultUID, GID: fs.cfg.DefaultGID}, nil
	}
	if name, ok := skillDirPath(path); ok && fs.skills.Exists(name) {
		return Stat{Path: path, Kind: KindDir, Mode: 0o555, UID: fs.cfg.DefaultUID, GID: fs.cfg.DefaultGID}, nil
	} else if name, ok := skillFilePath(path); ok {
		data, readErr := fs.skills.ReadSkillFile(name)
		if readErr != nil {
			err = readErr
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
	var size int64
	switch m.Kind {
	case KindBlob:
		size = int64(len(m.BlobData))
	case KindAPI:
		size = 0
	case KindDir:
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

func (fs *FileSystem) Readdir(path string, caller CallerIdentity) ([]DirEntry, error) {
	started := time.Now()
	var err error
	defer func() { fs.metrics.record(OpReaddir, started, err) }()
	if path == "/sys" {
		return []DirEntry{{Name: "metrics", Kind: KindBlob, Mode: 0o444}}, nil
	}
	if path == "/skills" {
		entries := make([]DirEntry, 0)
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
	if path != "/" {
		rm, err := fs.router.match(path)
		if err == nil {
			m := rm.mount
			if !canAccess(caller, m.UID, m.GID, m.Mode, OpReaddir) {
				return nil, posix(EACCES, OpReaddir, path, nil)
			}
			if m.Kind != KindDir {
				return nil, posix(ENOTDIR, OpReaddir, path, nil)
			}
		}
	}
	entries, err := fs.router.list(path)
	if err != nil {
		return nil, err
	}
	if path == "/" {
		entries = append(entries, DirEntry{Name: "sys", Kind: KindDir, Mode: 0o555})
		entries = append(entries, DirEntry{Name: "skills", Kind: KindDir, Mode: 0o555})
		entries = mergeDirEntries(nil, entries)
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	}
	return entries, nil
}

func (fs *FileSystem) Read(ctx context.Context, path string, caller CallerIdentity) ([]byte, error) {
	started := time.Now()
	var err error
	defer func() { fs.metrics.record(OpRead, started, err) }()
	if path == "/sys/metrics" {
		return fs.metrics.Prometheus(), nil
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
		cap, provider, err := fs.providerFor(m, OpRead, path)
		params := rm.params
		fs.mu.RUnlock()
		if err != nil {
			return nil, err
		}
		return invokeProvider(ctx, provider, cap, OpRead, path, params, nil, caller)
	case KindLink:
		target := []byte(m.LinkPath)
		fs.mu.RUnlock()
		return target, nil
	case KindStream:
		cfg := m.Stream
		fs.mu.RUnlock()
		b := fs.streams.getOrCreate(path, cfg)
		buf := make([]byte, streamReadChunk)
		n, err := b.read(buf, false)
		if err != nil {
			return nil, err
		}
		return buf[:n], nil
	case KindDir:
		fs.mu.RUnlock()
		return nil, posix(EISDIR, OpRead, path, nil)
	default:
		fs.mu.RUnlock()
		return nil, posix(ENOSYS, OpRead, path, nil)
	}
}

func (fs *FileSystem) Write(ctx context.Context, path string, payload []byte, caller CallerIdentity) error {
	started := time.Now()
	var err error
	defer func() { fs.metrics.record(OpWrite, started, err) }()
	fs.mu.RLock()
	rm, err := fs.router.match(path)
	if err != nil {
		fs.mu.RUnlock()
		return err
	}
	m := rm.mount
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
		m.BlobData = append(m.BlobData[:0], payload...)
		fs.events.emit(Event{Path: path, Kind: EventWrite})
		return nil
	case KindAPI:
		cap, provider, err := fs.providerFor(m, OpWrite, path)
		params := rm.params
		serial := m.serial
		fs.mu.RUnlock()
		if err != nil {
			return err
		}
		return serial.run(func() error {
			_, err := invokeProvider(ctx, provider, cap, OpWrite, path, params, payload, caller)
			return err
		})
	case KindStream:
		cfg := m.Stream
		fs.mu.RUnlock()
		b := fs.streams.getOrCreate(path, cfg)
		_, err := b.write(payload, false)
		if err != nil {
			return err
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
	return *rm.mount, rm.params.ToMap(), nil
}

func (fs *FileSystem) ResolveParams(path string) (MountEntry, ParamSet, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	rm, err := fs.router.match(path)
	if err != nil {
		return MountEntry{}, ParamSet{}, err
	}
	return *rm.mount, rm.params, nil
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
	return cap, provider, nil
}

func invokeProvider(ctx context.Context, provider Provider, cap *CapConfig, op OpCode, path string, pathParams ParamSet, payload []byte, caller CallerIdentity) ([]byte, error) {
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
	result, err := provider.Invoke(ctx, cap.Action, params)
	if err != nil {
		return nil, MapProviderError(err, op, path)
	}
	if result == nil {
		return nil, nil
	}
	return result.Data, nil
}

func hasProviderOps(ops map[OpCode]*CapConfig) bool {
	return len(ops) > 0
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
