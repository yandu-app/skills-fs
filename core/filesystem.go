package core

import (
	"context"
	"sort"
	"sync"
	"time"
)

type FileSystem struct {
	cfg       GlobalConfig
	router    *router
	providers map[string]Provider
	handles   *handleManager
	locks     *lockManager
	metrics   *Metrics
	skills    *SkillGenerator
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
		metrics:   newMetrics(),
		skills:    NewSkillGenerator(cfg.SkillsRoot),
	}
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

func (fs *FileSystem) Mount(path string, entry MountEntry) error {
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
	return nil
}

func (fs *FileSystem) Unmount(path string) error {
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
		size = 0
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
