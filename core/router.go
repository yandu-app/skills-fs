package core

import (
	"sort"
	"strings"
	"sync/atomic"
)

type routeNode struct {
	static   map[string]*routeNode
	param    *routeNode
	paramKey string
	mount    *MountEntry
}

type routeMatch struct {
	mount  *MountEntry
	params ParamSet
}

type router struct {
	root routeNode
	seq  atomic.Uint64
}

func newRouter() *router {
	return &router{}
}

func (r *router) add(m MountEntry) (*MountEntry, error) {
	parts, err := cleanParts(m.Path)
	if err != nil {
		return nil, err
	}
	paramCount := 0
	n := &r.root
	for _, p := range parts {
		if strings.HasPrefix(p, ":") {
			paramCount++
			if paramCount > inlineParams {
				return nil, posix(EINVAL, OpStat, m.Path, nil)
			}
			key := p[1:]
			if key == "" {
				return nil, posix(EINVAL, OpStat, m.Path, nil)
			}
			if n.param == nil {
				n.param = &routeNode{}
				n.paramKey = key
			} else if n.paramKey != key {
				return nil, posix(EEXIST, OpStat, m.Path, nil)
			}
			n = n.param
			continue
		}
		if n.static == nil {
			n.static = make(map[string]*routeNode)
		}
		if n.static[p] == nil {
			n.static[p] = &routeNode{}
		}
		n = n.static[p]
	}
	if n.mount != nil {
		return nil, posix(EEXIST, OpStat, m.Path, nil)
	}
	copy := m
	copy.ID = r.seq.Add(1)
	n.mount = &copy
	return n.mount, nil
}

func (r *router) remove(path string) (*MountEntry, error) {
	parts, err := cleanParts(path)
	if err != nil {
		return nil, err
	}
	n := &r.root
	for _, p := range parts {
		if strings.HasPrefix(p, ":") {
			if n.param == nil {
				return nil, posix(ENOENT, OpStat, path, nil)
			}
			n = n.param
			continue
		}
		if n.static == nil || n.static[p] == nil {
			return nil, posix(ENOENT, OpStat, path, nil)
		}
		n = n.static[p]
	}
	if n.mount == nil {
		return nil, posix(ENOENT, OpStat, path, nil)
	}
	m := n.mount
	n.mount = nil
	return m, nil
}

func (r *router) list(path string) ([]DirEntry, error) {
	n, err := r.node(path)
	if err != nil {
		return nil, err
	}
	if n.mount != nil && n.mount.Kind != KindDir && n.mount.Kind != KindDynamicDir {
		return nil, posix(ENOTDIR, OpReaddir, path, nil)
	}
	entries := make([]DirEntry, 0, len(n.static)+1)
	names := make([]string, 0, len(n.static))
	for name := range n.static {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		child := n.static[name]
		entries = append(entries, DirEntry{Name: name, Kind: nodeKind(child), Mode: nodeMode(child)})
	}
	if n.param != nil {
		entries = append(entries, DirEntry{Name: ":" + n.paramKey, Kind: nodeKind(n.param), Mode: nodeMode(n.param)})
	}
	return entries, nil
}

func (r *router) node(path string) (*routeNode, error) {
	if path == "" || path[0] != '/' {
		return nil, posix(EINVAL, OpStat, path, nil)
	}
	if strings.Contains(path, "//") {
		return nil, posix(EINVAL, OpStat, path, nil)
	}
	n := &r.root
	if path == "/" {
		return n, nil
	}
	start := 1
	for start <= len(path) {
		end := start
		for end < len(path) && path[end] != '/' {
			end++
		}
		if end == start {
			return nil, posix(EINVAL, OpStat, path, nil)
		}
		p := path[start:end]
		if p == "." || p == ".." {
			return nil, posix(EINVAL, OpStat, path, nil)
		}
		next, _, ok := nextRoute(n, p)
		if !ok {
			return nil, posix(ENOENT, OpStat, path, nil)
		}
		n = next
		if end == len(path) {
			break
		}
		start = end + 1
	}
	return n, nil
}

func nodeKind(n *routeNode) NodeKind {
	if n.mount != nil {
		return n.mount.Kind
	}
	return KindDir
}

func nodeMode(n *routeNode) uint32 {
	if n.mount != nil {
		return n.mount.Mode
	}
	return 0o555
}

func (r *router) match(path string) (routeMatch, error) {
	if path == "" || path[0] != '/' {
		return routeMatch{}, posix(EINVAL, OpStat, path, nil)
	}
	if strings.Contains(path, "//") {
		return routeMatch{}, posix(EINVAL, OpStat, path, nil)
	}
	n := &r.root
	var params ParamSet
	if path != "/" {
		start := 1
		for start <= len(path) {
			end := start
			for end < len(path) && path[end] != '/' {
				end++
			}
			if end == start {
				return routeMatch{}, posix(EINVAL, OpStat, path, nil)
			}
			p := path[start:end]
			if p == "." || p == ".." {
				return routeMatch{}, posix(EINVAL, OpStat, path, nil)
			}
			next, key, ok := nextRoute(n, p)
			if !ok {
				return routeMatch{}, posix(ENOENT, OpStat, path, nil)
			}
			if key != "" {
				params.set(key, p)
			}
			n = next
			if end == len(path) {
				break
			}
			start = end + 1
		}
	}
	if n.mount == nil {
		if n.static == nil && n.param == nil {
			return routeMatch{}, posix(ENOENT, OpStat, path, nil)
		}
	}
	return routeMatch{mount: n.mount, params: params}, nil
}

func nextRoute(n *routeNode, segment string) (*routeNode, string, bool) {
	if n.static != nil {
		if next := n.static[segment]; next != nil {
			return next, "", true
		}
	}
	if n.param != nil {
		return n.param, n.paramKey, true
	}
	return nil, "", false
}

// snapshot walks the route trie and returns a copy of every mounted entry.
func (r *router) snapshot() []MountEntry {
	var out []MountEntry
	var walk func(prefix string, n *routeNode)
	walk = func(prefix string, n *routeNode) {
		if n.mount != nil {
			out = append(out, *n.mount)
		}
		for name, child := range n.static {
			p := prefix + "/" + name
			walk(p, child)
		}
		if n.param != nil {
			walk(prefix+"/:"+n.paramKey, n.param)
		}
	}
	walk("", &r.root)
	return out
}

// count returns the number of mounted entries in the trie.
func (r *router) count() int {
	var n int
	var walk func(*routeNode)
	walk = func(node *routeNode) {
		if node.mount != nil {
			n++
		}
		for _, child := range node.static {
			walk(child)
		}
		if node.param != nil {
			walk(node.param)
		}
	}
	walk(&r.root)
	return n
}

func cleanParts(path string) ([]string, error) {
	if path == "" || path[0] != '/' {
		return nil, posix(EINVAL, OpStat, path, nil)
	}
	if path == "/" {
		return nil, nil
	}
	raw := strings.Split(strings.Trim(path, "/"), "/")
	for _, p := range raw {
		if p == "" || p == "." || p == ".." {
			return nil, posix(EINVAL, OpStat, path, nil)
		}
	}
	return raw, nil
}
