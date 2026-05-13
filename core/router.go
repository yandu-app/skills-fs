package core

import (
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
	params map[string]string
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
	n := &r.root
	for _, p := range parts {
		if strings.HasPrefix(p, ":") {
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

func (r *router) match(path string) (routeMatch, error) {
	if path == "" || path[0] != '/' {
		return routeMatch{}, posix(EINVAL, OpStat, path, nil)
	}
	if strings.Contains(path, "//") {
		return routeMatch{}, posix(EINVAL, OpStat, path, nil)
	}
	n := &r.root
	var params map[string]string
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
				if params == nil {
					params = make(map[string]string)
				}
				params[key] = p
			}
			n = next
			if end == len(path) {
				break
			}
			start = end + 1
		}
	}
	if n.mount == nil {
		return routeMatch{}, posix(ENOENT, OpStat, path, nil)
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
