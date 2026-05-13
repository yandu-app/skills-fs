package core

import (
	"context"
	"sync"
)

type FileSystem struct {
	cfg       GlobalConfig
	router    *router
	providers map[string]Provider
	skills    *SkillGenerator
	mu        sync.RWMutex
}

func NewFS(cfg GlobalConfig) *FileSystem {
	cfg = cfg.withDefaults()
	return &FileSystem{
		cfg:       cfg,
		router:    newRouter(),
		providers: make(map[string]Provider),
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
		return fs.skills.Remove(m.Skill.Name)
	}
	return nil
}

func (fs *FileSystem) Stat(path string, caller CallerIdentity) (Stat, error) {
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
	return fs.router.list(path)
}

func (fs *FileSystem) Read(ctx context.Context, path string, caller CallerIdentity) ([]byte, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	rm, err := fs.router.match(path)
	if err != nil {
		return nil, err
	}
	m := rm.mount
	if !canAccess(caller, m.UID, m.GID, m.Mode, OpRead) {
		return nil, posix(EACCES, OpRead, path, nil)
	}
	switch m.Kind {
	case KindBlob:
		return append([]byte(nil), m.BlobData...), nil
	case KindAPI:
		return fs.invoke(ctx, m, OpRead, path, rm.params, nil, caller)
	case KindLink:
		return []byte(m.LinkPath), nil
	case KindDir:
		return nil, posix(EISDIR, OpRead, path, nil)
	default:
		return nil, posix(ENOSYS, OpRead, path, nil)
	}
}

func (fs *FileSystem) Write(ctx context.Context, path string, payload []byte, caller CallerIdentity) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	rm, err := fs.router.match(path)
	if err != nil {
		return err
	}
	m := rm.mount
	if !canAccess(caller, m.UID, m.GID, m.Mode, OpWrite) {
		return posix(EACCES, OpWrite, path, nil)
	}
	switch m.Kind {
	case KindBlob:
		m.BlobData = append(m.BlobData[:0], payload...)
		return nil
	case KindAPI:
		_, err := fs.invoke(ctx, m, OpWrite, path, rm.params, payload, caller)
		return err
	default:
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
	return *rm.mount, rm.params, nil
}

func (fs *FileSystem) invoke(ctx context.Context, m *MountEntry, op OpCode, path string, pathParams map[string]string, payload []byte, caller CallerIdentity) ([]byte, error) {
	cap := m.Ops[op]
	if cap == nil {
		return nil, posix(ENOSYS, op, path, nil)
	}
	params := map[string]interface{}{}
	for k, v := range pathParams {
		params[k] = v
	}
	var err error
	if cap.ParamsFn != nil {
		params, err = cap.ParamsFn(pathParams, payload, OpContext{Path: path, Op: op, Caller: caller})
		if err != nil {
			return nil, posix(EINVAL, op, path, err)
		}
	}
	provider := fs.providers[cap.ProviderID]
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
