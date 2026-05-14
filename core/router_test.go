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
