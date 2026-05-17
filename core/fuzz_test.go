package core

import (
	"testing"
)

func FuzzNormalizePath(f *testing.F) {
	seeds := []string{"/", "/a", "/a/b", "/a/b/c"}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, path string) {
		out, err := normalizePath(path)
		if err != nil {
			return
		}
		if out == "" || out[0] != '/' {
			t.Fatalf("normalized path must start with /: %q", out)
		}
		if out != "/" && out[len(out)-1] == '/' {
			t.Fatalf("normalized path must not end with /: %q", out)
		}
	})
}

func FuzzRouterMatch(f *testing.F) {
	r := newRouter()
	mustAdd := func(path string, kind NodeKind) {
		_, err := r.add(MountEntry{Path: path, Kind: kind, Mode: 0o644})
		if err != nil {
			f.Fatal(err)
		}
	}
	mustAdd("/", KindDir)
	mustAdd("/a", KindBlob)
	mustAdd("/a/b", KindBlob)
	mustAdd("/api/:id", KindAPI)
	mustAdd("/users/:userId/posts/:postId", KindAPI)

	seeds := []string{"/", "/a", "/a/b", "/api/123", "/users/42/posts/7", "/x", "/a/b/c"}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, path string) {
		m, err := r.match(path)
		if err != nil {
			return
		}
		if m.mount == nil {
			t.Fatal("match succeeded but mount is nil")
		}
	})
}
