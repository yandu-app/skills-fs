package core

import (
	"testing"
)

func TestRouterRemoveParamRoute(t *testing.T) {
	r := newRouter()
	m := MountEntry{Kind: KindBlob, Mode: 0o444}
	if _, err := r.add(m); err == nil {
		t.Fatal("expected error for missing path")
	}

	m.Path = "/items/:id"
	added, err := r.add(m)
	if err != nil {
		t.Fatal(err)
	}
	if added.Path != "/items/:id" {
		t.Fatalf("unexpected path %s", added.Path)
	}

	removed, err := r.remove("/items/:id")
	if err != nil {
		t.Fatal(err)
	}
	if removed.Path != "/items/:id" {
		t.Fatalf("unexpected removed path %s", removed.Path)
	}

	_, err = r.remove("/items/:id")
	if !IsCode(err, ENOENT) {
		t.Fatalf("expected ENOENT, got %v", err)
	}
}

func TestRouterRemoveMissingIntermediate(t *testing.T) {
	r := newRouter()
	m := MountEntry{Path: "/a/b", Kind: KindBlob, Mode: 0o444}
	if _, err := r.add(m); err != nil {
		t.Fatal(err)
	}
	_, err := r.remove("/a/x")
	if !IsCode(err, ENOENT) {
		t.Fatalf("expected ENOENT, got %v", err)
	}
}

func TestRouterRemoveUnmountedNode(t *testing.T) {
	r := newRouter()
	// Add a parent dir mount, then try to remove a child that has no mount.
	m := MountEntry{Path: "/parent", Kind: KindDir, Mode: 0o755}
	if _, err := r.add(m); err != nil {
		t.Fatal(err)
	}
	_, err := r.remove("/parent/child")
	if !IsCode(err, ENOENT) {
		t.Fatalf("expected ENOENT, got %v", err)
	}
}

func TestNodeInvalidPaths(t *testing.T) {
	r := newRouter()
	cases := []string{"", "relative", "/a//b", "/a/", "/a/./b", "/a/../b"}
	for _, path := range cases {
		_, err := r.node(path)
		if err == nil {
			t.Fatalf("%q: expected error", path)
		}
	}
}

func TestNodeMissingPath(t *testing.T) {
	r := newRouter()
	_, err := r.node("/missing")
	if !IsCode(err, ENOENT) {
		t.Fatalf("expected ENOENT, got %v", err)
	}
}

func TestRouterRemoveParentOfParamRoute(t *testing.T) {
	r := newRouter()
	m := MountEntry{Path: "/items/:id", Kind: KindBlob, Mode: 0o444}
	if _, err := r.add(m); err != nil {
		t.Fatal(err)
	}
	_, err := r.remove("/items")
	if !IsCode(err, ENOENT) {
		t.Fatalf("expected ENOENT for parent of param route, got %v", err)
	}
}

func TestRouterRemoveParamNotFound(t *testing.T) {
	r := newRouter()
	m := MountEntry{Path: "/items/static", Kind: KindBlob, Mode: 0o444}
	if _, err := r.add(m); err != nil {
		t.Fatal(err)
	}
	_, err := r.remove("/items/:missing")
	if !IsCode(err, ENOENT) {
		t.Fatalf("expected ENOENT, got %v", err)
	}
}

func TestRouterAddInvalidPath(t *testing.T) {
	r := newRouter()
	m := MountEntry{Path: "relative", Kind: KindBlob, Mode: 0o444}
	_, err := r.add(m)
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
}

func TestRouterAddDuplicateMount(t *testing.T) {
	r := newRouter()
	m := MountEntry{Path: "/dup", Kind: KindBlob, Mode: 0o444}
	if _, err := r.add(m); err != nil {
		t.Fatal(err)
	}
	_, err := r.add(m)
	if !IsCode(err, EEXIST) {
		t.Fatalf("expected EEXIST, got %v", err)
	}
}

func TestRouterAddConflictingParamKeys(t *testing.T) {
	r := newRouter()
	m1 := MountEntry{Path: "/items/:id", Kind: KindBlob, Mode: 0o444}
	if _, err := r.add(m1); err != nil {
		t.Fatal(err)
	}
	m2 := MountEntry{Path: "/items/:name", Kind: KindBlob, Mode: 0o444}
	_, err := r.add(m2)
	if !IsCode(err, EEXIST) {
		t.Fatalf("expected EEXIST for conflicting param keys, got %v", err)
	}
}

func TestCleanPartsEdgeCases(t *testing.T) {
	cases := []struct {
		path string
		ok   bool
	}{
		{"/", true},
		{"/a/b", true},
		{"/a//b", false},
		{"/a/./b", false},
		{"/a/../b", false},
		{"relative", false},
		{"", false},
	}
	for _, tc := range cases {
		parts, err := cleanParts(tc.path)
		if tc.ok {
			if err != nil {
				t.Fatalf("%q: unexpected error: %v", tc.path, err)
			}
			if tc.path == "/" {
				if parts != nil {
					t.Fatalf("%q: expected nil parts for root, got %v", tc.path, parts)
				}
			}
		} else {
			if err == nil {
				t.Fatalf("%q: expected error, got parts %v", tc.path, parts)
			}
		}
	}
}
