package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeProvider struct {
	id       string
	err      error
	mu       sync.Mutex
	calls    []providerCall
	response []byte
}

type blockingProvider struct {
	id       string
	started  chan string
	release  chan struct{}
	mu       sync.Mutex
	order    []string
	active   atomic.Int32
	maxAlive atomic.Int32
}

func newBlockingProvider(id string) *blockingProvider {
	return &blockingProvider{
		id:      id,
		started: make(chan string, 16),
		release: make(chan struct{}),
	}
}

func (p *blockingProvider) ID() string {
	return p.id
}

func (p *blockingProvider) Invoke(_ context.Context, action string, params map[string]interface{}) (*ProviderResult, error) {
	alive := p.active.Add(1)
	for {
		old := p.maxAlive.Load()
		if alive <= old || p.maxAlive.CompareAndSwap(old, alive) {
			break
		}
	}
	name, _ := params["name"].(string)
	p.started <- name
	<-p.release
	p.mu.Lock()
	p.order = append(p.order, name)
	p.mu.Unlock()
	p.active.Add(-1)
	return &ProviderResult{}, nil
}

type providerCall struct {
	action string
	params map[string]interface{}
}

func (p *fakeProvider) ID() string {
	return p.id
}

func (p *fakeProvider) Invoke(_ context.Context, action string, params map[string]interface{}) (*ProviderResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, providerCall{action: action, params: params})
	if p.err != nil {
		return nil, p.err
	}
	return &ProviderResult{Data: p.response}, nil
}

func TestResolvePrefersExactOverParam(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/papers/items/:id", MountEntry{Kind: KindBlob, Mode: 0o444, BlobData: []byte("param")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/papers/items/latest", MountEntry{Kind: KindBlob, Mode: 0o444, BlobData: []byte("exact")}); err != nil {
		t.Fatal(err)
	}
	got, err := fs.Read(context.Background(), "/papers/items/latest", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "exact" {
		t.Fatalf("expected exact route, got %q", got)
	}
}

func TestResolveExtractsPathParams(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/papers/items/:itemId/attachments/:attId", MountEntry{Kind: KindBlob}); err != nil {
		t.Fatal(err)
	}
	_, params, err := fs.Resolve("/papers/items/p1/attachments/a9")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"itemId": "p1", "attId": "a9"}
	if !reflect.DeepEqual(params, want) {
		t.Fatalf("params mismatch: got %#v want %#v", params, want)
	}
	_, set, err := fs.ResolveParams("/papers/items/p1/attachments/a9")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := set.Get("itemId"); !ok || got != "p1" {
		t.Fatalf("ResolveParams itemId = %q, %v", got, ok)
	}
}

func TestDuplicateMountRejected(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/a/b", MountEntry{Kind: KindBlob}); err != nil {
		t.Fatal(err)
	}
	err := fs.Mount("/a/b", MountEntry{Kind: KindBlob})
	if !IsCode(err, EEXIST) {
		t.Fatalf("expected EEXIST, got %v", err)
	}
}

func TestTooManyPathParamsRejected(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	path := "/:a/:b/:c/:d/:e/:f/:g/:h/:i"
	if err := fs.Mount(path, MountEntry{Kind: KindBlob}); !IsCode(err, EINVAL) {
		t.Fatalf("expected EINVAL, got %v", err)
	}
}

func TestRegisterProviderRejectsInvalidAndDuplicate(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.RegisterProvider(nil); !IsCode(err, EINVAL) {
		t.Fatalf("expected nil provider EINVAL, got %v", err)
	}
	provider := &fakeProvider{id: "p"}
	if err := fs.RegisterProvider(provider); err != nil {
		t.Fatal(err)
	}
	if err := fs.RegisterProvider(provider); !IsCode(err, EEXIST) {
		t.Fatalf("expected duplicate EEXIST, got %v", err)
	}
}

func TestMountRejectsUnknownProvider(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	err := fs.Mount("/x", MountEntry{
		Kind: KindAPI,
		Ops:  map[OpCode]*CapConfig{OpRead: {ProviderID: "missing", Action: "x"}},
	})
	if !IsCode(err, EINVAL) {
		t.Fatalf("expected EINVAL, got %v", err)
	}
}

func TestReadPermissionDenied(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/secret", MountEntry{Kind: KindBlob, Mode: 0o600, UID: 1000, GID: 1000, BlobData: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	_, err := fs.Read(context.Background(), "/secret", CallerIdentity{UID: 2000, GID: 2000})
	if !IsCode(err, EACCES) {
		t.Fatalf("expected EACCES, got %v", err)
	}
}

func TestGroupAndOtherPermissions(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/group", MountEntry{Kind: KindBlob, Mode: 0o040, UID: 1, GID: 200, BlobData: []byte("g")}); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Read(context.Background(), "/group", CallerIdentity{UID: 2, GID: 200}); err != nil {
		t.Fatalf("group read should pass: %v", err)
	}
	if err := fs.Mount("/other", MountEntry{Kind: KindBlob, Mode: 0o004, UID: 1, GID: 2, BlobData: []byte("o")}); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Read(context.Background(), "/other", CallerIdentity{UID: 3, GID: 4}); err != nil {
		t.Fatalf("other read should pass: %v", err)
	}
}

func TestWritePermissionDenied(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/readonly", MountEntry{Kind: KindBlob, Mode: 0o444, BlobData: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	err := fs.Write(context.Background(), "/readonly", []byte("y"), CallerIdentity{})
	if !IsCode(err, EACCES) {
		t.Fatalf("expected EACCES, got %v", err)
	}
}

func TestInvalidAndMissingPaths(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("relative", MountEntry{Kind: KindBlob}); !IsCode(err, EINVAL) {
		t.Fatalf("expected relative path EINVAL, got %v", err)
	}
	if _, err := fs.Read(context.Background(), "/missing", CallerIdentity{}); !IsCode(err, ENOENT) {
		t.Fatalf("expected missing ENOENT, got %v", err)
	}
	if _, _, err := fs.Resolve("/bad//path"); !IsCode(err, EINVAL) {
		t.Fatalf("expected bad path EINVAL, got %v", err)
	}
	if err := fs.Unmount("/missing"); !IsCode(err, ENOENT) {
		t.Fatalf("expected unmount missing ENOENT, got %v", err)
	}
}

func TestBlobWriteAndStat(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/blob", MountEntry{Kind: KindBlob, Mode: 0o644, UID: 1000, GID: 1000, BlobData: []byte("old")}); err != nil {
		t.Fatal(err)
	}
	caller := CallerIdentity{UID: 1000, GID: 42}
	if err := fs.Write(context.Background(), "/blob", []byte("new-data"), caller); err != nil {
		t.Fatal(err)
	}
	stat, err := fs.Stat("/blob", caller)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Size != int64(len("new-data")) || stat.Mode != 0o644 || stat.UID != 1000 {
		t.Fatalf("unexpected stat: %#v", stat)
	}
	got, err := fs.Read(context.Background(), "/blob", caller)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new-data" {
		t.Fatalf("unexpected blob data %q", got)
	}
}

func TestOpenHandleReadWriteAndClose(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/blob", MountEntry{Kind: KindBlob, Mode: 0o666, BlobData: []byte("old")}); err != nil {
		t.Fatal(err)
	}
	h, err := fs.Open("/blob", OpenRead|OpenWrite, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Fatalf("read = %q", got)
	}
	if err := h.Write(context.Background(), []byte("new")); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ReadAll(context.Background()); !IsCode(err, EBUSY) {
		t.Fatalf("closed handle read should fail with EBUSY, got %v", err)
	}
}

func TestMaxOpenHandles(t *testing.T) {
	fs := NewFS(GlobalConfig{MaxOpenHandles: 1})
	if err := fs.Mount("/blob", MountEntry{Kind: KindBlob, Mode: 0o666}); err != nil {
		t.Fatal(err)
	}
	h, err := fs.Open("/blob", OpenRead, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Open("/blob", OpenRead, CallerIdentity{}); !IsCode(err, EBUSY) {
		t.Fatalf("expected EBUSY, got %v", err)
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h2, err := fs.Open("/blob", OpenRead, CallerIdentity{}); err != nil {
		t.Fatalf("open after close should pass: %v", err)
	} else if err := h2.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBufferedHandleFlushOnNewlineAndClose(t *testing.T) {
	provider := &fakeProvider{id: "p"}
	fs := NewFS(GlobalConfig{})
	if err := fs.RegisterProvider(provider); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/commands/:name", MountEntry{
		Kind: KindAPI,
		Mode: 0o222,
		BufferPolicy: &WriteBufferPolicy{
			Mode:           WriteBuffered,
			FlushOnNewline: true,
		},
		Ops: map[OpCode]*CapConfig{OpWrite: {
			ProviderID: "p",
			Action:     "command.run",
			ParamsFn: func(pathParams map[string]string, payload []byte, _ OpContext) (map[string]interface{}, error) {
				return map[string]interface{}{"name": pathParams["name"], "payload": string(payload)}, nil
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	h, err := fs.Open("/commands/build", OpenWrite, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Write(context.Background(), []byte("one")); err != nil {
		t.Fatal(err)
	}
	if len(provider.calls) != 0 {
		t.Fatalf("buffer flushed too early: %#v", provider.calls)
	}
	if err := h.Write(context.Background(), []byte("\ntwo")); err != nil {
		t.Fatal(err)
	}
	if len(provider.calls) != 1 || provider.calls[0].params["payload"] != "one\n" {
		t.Fatalf("newline flush mismatch: %#v", provider.calls)
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.calls) != 2 || provider.calls[1].params["payload"] != "two" {
		t.Fatalf("close flush mismatch: %#v", provider.calls)
	}
}

func TestBufferedHandleFlushOnMaxSize(t *testing.T) {
	provider := &fakeProvider{id: "p"}
	fs := NewFS(GlobalConfig{})
	if err := fs.RegisterProvider(provider); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/commands/run", MountEntry{
		Kind:         KindAPI,
		Mode:         0o222,
		BufferPolicy: &WriteBufferPolicy{Mode: WriteBuffered, MaxSize: 4},
		Ops: map[OpCode]*CapConfig{OpWrite: {
			ProviderID: "p",
			Action:     "run",
			ParamsFn: func(_ map[string]string, payload []byte, _ OpContext) (map[string]interface{}, error) {
				return map[string]interface{}{"payload": string(payload)}, nil
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	h, err := fs.Open("/commands/run", OpenWrite, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Write(context.Background(), []byte("abcd")); err != nil {
		t.Fatal(err)
	}
	if len(provider.calls) != 1 || provider.calls[0].params["payload"] != "abcd" {
		t.Fatalf("max flush mismatch: %#v", provider.calls)
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStatKinds(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	mounts := map[string]MountEntry{
		"/api":    {Kind: KindAPI},
		"/dir":    {Kind: KindDir},
		"/link":   {Kind: KindLink, LinkPath: "/target"},
		"/stream": {Kind: KindStream},
	}
	for path, entry := range mounts {
		if err := fs.Mount(path, entry); err != nil {
			t.Fatal(err)
		}
		stat, err := fs.Stat(path, CallerIdentity{})
		if err != nil {
			t.Fatal(err)
		}
		if stat.Kind != entry.Kind {
			t.Fatalf("%s kind = %s, want %s", path, stat.Kind, entry.Kind)
		}
	}
}

func TestReaddirRootAndNestedMounts(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	mounts := map[string]MountEntry{
		"/papers":             {Kind: KindDir, Mode: 0o555},
		"/papers/items/:id":   {Kind: KindBlob, Mode: 0o444},
		"/skills":             {Kind: KindDir, Mode: 0o555},
		"/skills/greet":       {Kind: KindAPI, Mode: 0o444},
		"/z-last/static-file": {Kind: KindBlob, Mode: 0o444},
	}
	for path, entry := range mounts {
		if err := fs.Mount(path, entry); err != nil {
			t.Fatalf("mount %s: %v", path, err)
		}
	}
	root, err := fs.Readdir("/", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := []DirEntry{
		{Name: "papers", Kind: KindDir, Mode: 0o555},
		{Name: "skills", Kind: KindDir, Mode: 0o555},
		{Name: "sys", Kind: KindDir, Mode: 0o555},
		{Name: "z-last", Kind: KindDir, Mode: 0o555},
	}
	if !reflect.DeepEqual(root, wantRoot) {
		t.Fatalf("root entries = %#v, want %#v", root, wantRoot)
	}
	papers, err := fs.Readdir("/papers/items", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	wantPapers := []DirEntry{{Name: ":id", Kind: KindBlob, Mode: 0o444}}
	if !reflect.DeepEqual(papers, wantPapers) {
		t.Fatalf("papers entries = %#v, want %#v", papers, wantPapers)
	}
}

func TestReaddirErrors(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/blob", MountEntry{Kind: KindBlob}); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Readdir("/blob", CallerIdentity{}); !IsCode(err, ENOTDIR) {
		t.Fatalf("expected ENOTDIR, got %v", err)
	}
	if _, err := fs.Readdir("/missing", CallerIdentity{}); !IsCode(err, ENOENT) {
		t.Fatalf("expected ENOENT, got %v", err)
	}
	if _, err := fs.Readdir("/bad//path", CallerIdentity{}); !IsCode(err, EINVAL) {
		t.Fatalf("expected EINVAL, got %v", err)
	}
}

func TestSysMetricsVirtualFiles(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/blob", MountEntry{Kind: KindBlob, Mode: 0o444, BlobData: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Read(context.Background(), "/blob", CallerIdentity{}); err != nil {
		t.Fatal(err)
	}
	stat, err := fs.Stat("/sys/metrics", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if stat.Kind != KindBlob || stat.Mode != 0o444 || stat.Size == 0 {
		t.Fatalf("unexpected metrics stat: %#v", stat)
	}
	entries, err := fs.Readdir("/sys", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	want := []DirEntry{{Name: "metrics", Kind: KindBlob, Mode: 0o444}}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("sys entries = %#v, want %#v", entries, want)
	}
	data, err := fs.Read(context.Background(), "/sys/metrics", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"skills_fs_operations_total{op=\"read\"}",
		"skills_fs_operation_errors_total{op=\"read\"}",
		"skills_fs_operation_latency_seconds_sum{op=\"read\"}",
		"skills_fs_operation_latency_seconds_count{op=\"read\"}",
		"skills_fs_operation_latency_seconds_bucket{op=\"read\"",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics missing %q in:\n%s", want, text)
		}
	}
}

func TestLinkRead(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/link", MountEntry{Kind: KindLink, LinkPath: "/target"}); err != nil {
		t.Fatal(err)
	}
	got, err := fs.Read(context.Background(), "/link", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "/target" {
		t.Fatalf("unexpected link target %q", got)
	}
}

func TestReadLink(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/link", MountEntry{Kind: KindLink, LinkPath: "/target"}); err != nil {
		t.Fatal(err)
	}
	target, err := fs.ReadLink("/link")
	if err != nil {
		t.Fatal(err)
	}
	if target != "/target" {
		t.Fatalf("unexpected target %q", target)
	}
}

func TestReadLinkNotLink(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/blob", MountEntry{Kind: KindBlob, Mode: 0o666}); err != nil {
		t.Fatal(err)
	}
	_, err := fs.ReadLink("/blob")
	if !IsCode(err, EINVAL) {
		t.Fatalf("expected EINVAL, got %v", err)
	}
}

func TestFollowLinkAbsolute(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/target", MountEntry{Kind: KindBlob, Mode: 0o666, BlobData: []byte("hello")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/link", MountEntry{Kind: KindLink, LinkPath: "/target"}); err != nil {
		t.Fatal(err)
	}
	resolved, err := fs.FollowLink("/link")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "/target" {
		t.Fatalf("unexpected resolved path %q", resolved)
	}
}

func TestFollowLinkRelative(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/dir/target", MountEntry{Kind: KindBlob, Mode: 0o666, BlobData: []byte("hello")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/dir/link", MountEntry{Kind: KindLink, LinkPath: "target"}); err != nil {
		t.Fatal(err)
	}
	resolved, err := fs.FollowLink("/dir/link")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "/dir/target" {
		t.Fatalf("unexpected resolved path %q", resolved)
	}
}

func TestFollowLinkLoop(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/a", MountEntry{Kind: KindLink, LinkPath: "/b"}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/b", MountEntry{Kind: KindLink, LinkPath: "/a"}); err != nil {
		t.Fatal(err)
	}
	_, err := fs.FollowLink("/a")
	if !IsCode(err, ELOOP) {
		t.Fatalf("expected ELOOP, got %v", err)
	}
}

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		path string
		ok   bool
	}{
		{"/", true},
		{"/foo", true},
		{"/foo/bar", true},
		{"foo", false},
		{"", false},
		{"/foo/", false},
		{"/foo/../bar", false},
		{"/foo/./bar", false},
		{"/foo//bar", false},
		{"/../foo", false},
	}
	for _, tc := range tests {
		_, err := normalizePath(tc.path)
		if tc.ok && err != nil {
			t.Fatalf("path %q: expected ok, got %v", tc.path, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("path %q: expected error, got nil", tc.path)
		}
	}
}

func TestMountRejectsBadPath(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/foo/../bar", MountEntry{Kind: KindBlob}); !IsCode(err, EINVAL) {
		t.Fatalf("expected EINVAL, got %v", err)
	}
}

func TestUnmountRejectsBadPath(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Unmount("/foo/../bar"); !IsCode(err, EINVAL) {
		t.Fatalf("expected EINVAL, got %v", err)
	}
}

func TestRenameRejectsBadPath(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Rename("/foo/../a", "/b"); !IsCode(err, EINVAL) {
		t.Fatalf("expected EINVAL for old, got %v", err)
	}
	if err := fs.Rename("/a", "/foo/../b"); !IsCode(err, EINVAL) {
		t.Fatalf("expected EINVAL for new, got %v", err)
	}
}

func TestSnapshotAndRestore(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/a", MountEntry{Kind: KindBlob, Mode: 0o644, BlobData: []byte("alpha")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/b", MountEntry{Kind: KindDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}

	snap := fs.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(snap))
	}

	fs2 := NewFS(GlobalConfig{})
	if err := fs2.Restore(snap); err != nil {
		t.Fatal(err)
	}

	data, err := fs2.Read(context.Background(), "/a", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "alpha" {
		t.Fatalf("unexpected data %q", data)
	}

	entries, err := fs2.Readdir("/b", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty dir, got %d entries", len(entries))
	}
}

func TestRestoreClearsExisting(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/old", MountEntry{Kind: KindBlob}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Restore([]MountEntry{{Path: "/new", Kind: KindBlob, Mode: 0o644}}); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat("/old", CallerIdentity{}); !IsCode(err, ENOENT) {
		t.Fatalf("expected ENOENT for /old, got %v", err)
	}
	if _, err := fs.Stat("/new", CallerIdentity{}); err != nil {
		t.Fatalf("expected /new to exist, got %v", err)
	}
}

func TestMaxMountsBudget(t *testing.T) {
	fs := NewFS(GlobalConfig{MaxMounts: 2})
	if err := fs.Mount("/a", MountEntry{Kind: KindBlob}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/b", MountEntry{Kind: KindBlob}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/c", MountEntry{Kind: KindBlob}); !IsCode(err, ENOSPC) {
		t.Fatalf("expected ENOSPC, got %v", err)
	}
}

func TestMountReservedPath(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	for _, p := range []string{"/sys", "/sys/metrics", "/healthz", "/debug", "/debug/pprof"} {
		if err := fs.Mount(p, MountEntry{Kind: KindBlob}); !IsCode(err, EINVAL) {
			t.Fatalf("mount %q: expected EINVAL, got %v", p, err)
		}
	}
	if err := fs.Mount("/syslog", MountEntry{Kind: KindBlob}); err != nil {
		t.Fatalf("mount /syslog should be allowed, got %v", err)
	}
}

func TestAsyncProviderRead(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	p := &fakeProvider{id: "async", response: []byte("delayed")}
	if err := fs.RegisterProvider(p); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/api", MountEntry{
		Kind: KindAPI,
		Mode: 0o444,
		Ops: map[OpCode]*CapConfig{
			OpRead: {ProviderID: "async", Action: "greet", Async: true},
		},
	}); err != nil {
		t.Fatal(err)
	}

	data, err := fs.Read(context.Background(), "/api", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("expected empty async result, got %q", data)
	}

	// Wait for background invocation.
	time.Sleep(100 * time.Millisecond)
	p.mu.Lock()
	if len(p.calls) != 1 {
		t.Fatalf("expected 1 async call, got %d", len(p.calls))
	}
	p.mu.Unlock()
}

func TestMaxBlobSizeBudget(t *testing.T) {
	fs := NewFS(GlobalConfig{MaxBlobSize: 5})
	if err := fs.Mount("/small", MountEntry{Kind: KindBlob, Mode: 0o666, BlobData: []byte("12345")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/big", MountEntry{Kind: KindBlob, Mode: 0o666, BlobData: []byte("123456")}); !IsCode(err, ENOSPC) {
		t.Fatalf("expected ENOSPC on mount, got %v", err)
	}
	if err := fs.Write(context.Background(), "/small", []byte("123456"), CallerIdentity{}); !IsCode(err, ENOSPC) {
		t.Fatalf("expected ENOSPC on write, got %v", err)
	}
}

func TestUnknownKindMountIsEINVAL(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/badkind", MountEntry{Kind: NodeKind("weird")}); !IsCode(err, EINVAL) {
		t.Fatalf("expected EINVAL on mount, got %v", err)
	}
}

func TestReadDirAndMissingOpErrors(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/dir", MountEntry{Kind: KindDir, Mode: 0o666}); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Read(context.Background(), "/dir", CallerIdentity{}); !IsCode(err, EISDIR) {
		t.Fatalf("expected EISDIR, got %v", err)
	}
	if err := fs.Write(context.Background(), "/dir", []byte("x"), CallerIdentity{}); !IsCode(err, ENOSYS) {
		t.Fatalf("expected ENOSYS, got %v", err)
	}
}

func TestAPIReadInvokesProviderWithParams(t *testing.T) {
	provider := &fakeProvider{id: "p", response: []byte("ok")}
	fs := NewFS(GlobalConfig{})
	if err := fs.RegisterProvider(provider); err != nil {
		t.Fatal(err)
	}
	err := fs.Mount("/items/:id/status", MountEntry{
		Kind: KindAPI,
		Mode: 0o444,
		Ops: map[OpCode]*CapConfig{
			OpRead: {ProviderID: "p", Action: "item.status"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := fs.Read(context.Background(), "/items/42/status", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ok" {
		t.Fatalf("got %q", got)
	}
	if len(provider.calls) != 1 || provider.calls[0].params["id"] != "42" {
		t.Fatalf("provider params not propagated: %#v", provider.calls)
	}
}

func TestAPIReadWithoutCapReturnsENOSYS(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/x", MountEntry{Kind: KindAPI}); err != nil {
		t.Fatal(err)
	}
	_, err := fs.Read(context.Background(), "/x", CallerIdentity{})
	if !IsCode(err, ENOSYS) {
		t.Fatalf("expected ENOSYS, got %v", err)
	}
}

func TestAPIWriteUsesParamsFnPayload(t *testing.T) {
	provider := &fakeProvider{id: "p"}
	fs := NewFS(GlobalConfig{})
	if err := fs.RegisterProvider(provider); err != nil {
		t.Fatal(err)
	}
	err := fs.Mount("/commands/:name", MountEntry{
		Kind: KindAPI,
		Mode: 0o222,
		Ops: map[OpCode]*CapConfig{
			OpWrite: {
				ProviderID: "p",
				Action:     "command.run",
				ParamsFn: func(pathParams map[string]string, payload []byte, _ OpContext) (map[string]interface{}, error) {
					return map[string]interface{}{
						"name":    pathParams["name"],
						"payload": string(payload),
					}, nil
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Write(context.Background(), "/commands/build", []byte(`{"force":true}`), CallerIdentity{}); err != nil {
		t.Fatal(err)
	}
	got := provider.calls[0].params
	if got["name"] != "build" || got["payload"] != `{"force":true}` {
		t.Fatalf("unexpected params: %#v", got)
	}
}

func TestSerialAPIWriteRunsOneAtATime(t *testing.T) {
	provider := newBlockingProvider("p")
	fs := NewFS(GlobalConfig{})
	if err := fs.RegisterProvider(provider); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/commands/:name", MountEntry{
		Kind:   KindAPI,
		Mode:   0o222,
		Serial: true,
		Ops: map[OpCode]*CapConfig{OpWrite: {
			ProviderID: "p",
			Action:     "command.run",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	go func() { errs <- fs.Write(context.Background(), "/commands/first", []byte("x"), CallerIdentity{}) }()
	if got := <-provider.started; got != "first" {
		t.Fatalf("first started = %q", got)
	}
	go func() { errs <- fs.Write(context.Background(), "/commands/second", []byte("x"), CallerIdentity{}) }()
	select {
	case got := <-provider.started:
		t.Fatalf("serial write started concurrently: %s", got)
	case <-time.After(20 * time.Millisecond):
	}
	provider.release <- struct{}{}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if got := <-provider.started; got != "second" {
		t.Fatalf("second started = %q", got)
	}
	provider.release <- struct{}{}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if provider.maxAlive.Load() != 1 {
		t.Fatalf("serial provider max concurrency = %d", provider.maxAlive.Load())
	}
}

func TestNonSerialAPIWriteMayOverlap(t *testing.T) {
	provider := newBlockingProvider("p")
	fs := NewFS(GlobalConfig{})
	if err := fs.RegisterProvider(provider); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/commands/:name", MountEntry{
		Kind: KindAPI,
		Mode: 0o222,
		Ops: map[OpCode]*CapConfig{OpWrite: {
			ProviderID: "p",
			Action:     "command.run",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	go func() { errs <- fs.Write(context.Background(), "/commands/first", []byte("x"), CallerIdentity{}) }()
	go func() { errs <- fs.Write(context.Background(), "/commands/second", []byte("x"), CallerIdentity{}) }()
	<-provider.started
	<-provider.started
	provider.release <- struct{}{}
	provider.release <- struct{}{}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if provider.maxAlive.Load() < 2 {
		t.Fatalf("non-serial writes did not overlap")
	}
}

func TestParamsFnErrorIsEINVAL(t *testing.T) {
	provider := &fakeProvider{id: "p"}
	fs := NewFS(GlobalConfig{})
	if err := fs.RegisterProvider(provider); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/x", MountEntry{
		Kind: KindAPI,
		Mode: 0o444,
		Ops: map[OpCode]*CapConfig{OpRead: {
			ProviderID: "p",
			Action:     "x",
			ParamsFn: func(map[string]string, []byte, OpContext) (map[string]interface{}, error) {
				return nil, errors.New("bad params")
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	_, err := fs.Read(context.Background(), "/x", CallerIdentity{})
	if !IsCode(err, EINVAL) {
		t.Fatalf("expected EINVAL, got %v", err)
	}
}

func TestProviderErrorMapping(t *testing.T) {
	provider := &fakeProvider{id: "p", err: errors.New("TIMEOUT")}
	fs := NewFS(GlobalConfig{})
	if err := fs.RegisterProvider(provider); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/x", MountEntry{
		Kind: KindAPI,
		Mode: 0o444,
		Ops:  map[OpCode]*CapConfig{OpRead: {ProviderID: "p", Action: "x"}},
	}); err != nil {
		t.Fatal(err)
	}
	_, err := fs.Read(context.Background(), "/x", CallerIdentity{})
	if !IsCode(err, ETIMEDOUT) {
		t.Fatalf("expected ETIMEDOUT, got %v", err)
	}
}

func TestPosixErrorFormattingAndUnwrap(t *testing.T) {
	cause := errors.New("cause")
	err := posix(EIO, OpRead, "/x", cause)
	if !strings.Contains(err.Error(), "EIO") {
		t.Fatalf("unexpected error string: %s", err)
	}
	var pe *PosixError
	if !errors.As(err, &pe) || pe.Unwrap() != cause {
		t.Fatalf("unwrap failed: %#v", err)
	}
}

func TestSkillGenerateAndUnmountCleanup(t *testing.T) {
	root := t.TempDir()
	fs := NewFS(GlobalConfig{SkillsRoot: root})
	err := fs.Mount("/skills/greet", MountEntry{
		Kind: KindAPI,
		Mode: 0o444,
		Skill: &SkillConfig{
			Enabled:      true,
			Name:         "greeting",
			Description:  "Provides greeting text.",
			BodyTemplate: "Read with cat.",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "greeting", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" || !strings.Contains(string(data), "name: greeting") {
		t.Fatalf("unexpected skill content: %q", data)
	}
	if err := fs.Unmount("/skills/greet"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "greeting")); !os.IsNotExist(err) {
		t.Fatalf("skill dir should be removed, stat err=%v", err)
	}
}

func TestGeneratedSkillsVirtualNamespace(t *testing.T) {
	root := t.TempDir()
	fs := NewFS(GlobalConfig{SkillsRoot: root})
	if err := fs.Mount("/tools/greet", MountEntry{
		Kind: KindAPI,
		Mode: 0o444,
		Skill: &SkillConfig{
			Enabled:      true,
			Name:         "greeting",
			Description:  "Provides greeting text.",
			BodyTemplate: "Read with cat.",
		},
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := fs.Readdir("/skills", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	wantEntries := []DirEntry{{Name: "greeting", Kind: KindDir, Mode: 0o555}}
	if !reflect.DeepEqual(entries, wantEntries) {
		t.Fatalf("skills entries = %#v, want %#v", entries, wantEntries)
	}
	skillEntries, err := fs.Readdir("/skills/greeting", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(skillEntries, []DirEntry{{Name: "SKILL.md", Kind: KindBlob, Mode: 0o444}}) {
		t.Fatalf("skill dir entries = %#v", skillEntries)
	}
	stat, err := fs.Stat("/skills/greeting/SKILL.md", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if stat.Kind != KindBlob || stat.Size == 0 {
		t.Fatalf("unexpected skill stat: %#v", stat)
	}
	data, err := fs.Read(context.Background(), "/skills/greeting/SKILL.md", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "name: greeting") {
		t.Fatalf("unexpected skill file:\n%s", data)
	}
	if err := fs.Unmount("/tools/greet"); err != nil {
		t.Fatal(err)
	}
	entries, err = fs.Readdir("/skills", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("skills should be empty after unmount: %#v", entries)
	}
}

func TestSkillGenerateRichFrontmatterAndTemplate(t *testing.T) {
	root := t.TempDir()
	gen := NewSkillGenerator(root)
	cfg := SkillConfig{
		Enabled:       true,
		Name:          "rich-skill",
		Description:   "Rich skill.",
		License:       "MIT",
		Compatibility: "codex",
		AllowedTools:  []string{"Read", "Grep"},
		Metadata:      map[string]string{"b": "2", "a": "1"},
		BodyTemplate:  "Hello {{.Name}}.",
	}
	if err := gen.Generate(cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "rich-skill", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"license: MIT", "compatibility: codex", "  - Read", "  a: 1", "Hello rich-skill."} {
		if !strings.Contains(text, want) {
			t.Fatalf("skill missing %q in:\n%s", want, text)
		}
	}
}

func TestSkillValidationFailures(t *testing.T) {
	gen := NewSkillGenerator(t.TempDir())
	cases := []SkillConfig{
		{Enabled: true, Name: "Bad_Name", Description: "x", BodyTemplate: "x"},
		{Enabled: true, Name: "ok-name", Description: "", BodyTemplate: "x"},
		{Enabled: true, Name: "ok-name", Description: "x", Compatibility: strings.Repeat("x", 501), BodyTemplate: "x"},
	}
	for _, tc := range cases {
		if err := gen.Generate(tc); !IsCode(err, EINVAL) {
			t.Fatalf("expected EINVAL for %#v, got %v", tc, err)
		}
	}
}

func TestSkillGeneratorRootAndRemoveValidation(t *testing.T) {
	gen := NewSkillGenerator("")
	err := gen.Generate(SkillConfig{Enabled: true, Name: "x", Description: "x", BodyTemplate: "x"})
	if !IsCode(err, EINVAL) {
		t.Fatalf("expected missing root EINVAL, got %v", err)
	}
	gen = NewSkillGenerator(t.TempDir())
	if err := gen.Remove("Bad_Name"); !IsCode(err, EINVAL) {
		t.Fatalf("expected invalid remove EINVAL, got %v", err)
	}
}

func TestRemoveAliasForUnmount(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/blob", MountEntry{Kind: KindBlob, Mode: 0o644, BlobData: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Remove("/blob"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat("/blob", CallerIdentity{}); !IsCode(err, ENOENT) {
		t.Fatalf("expected ENOENT after remove, got %v", err)
	}
}

func TestRenameBlob(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/old", MountEntry{Kind: KindBlob, Mode: 0o644, BlobData: []byte("data")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Rename("/old", "/new"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat("/old", CallerIdentity{}); !IsCode(err, ENOENT) {
		t.Fatalf("expected old path gone, got %v", err)
	}
	data, err := fs.Read(context.Background(), "/new", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "data" {
		t.Fatalf("unexpected data %q", data)
	}
}

func TestRenameDir(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/old", MountEntry{Kind: KindDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Rename("/old", "/new"); err != nil {
		t.Fatal(err)
	}
	stat, err := fs.Stat("/new", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if stat.Kind != KindDir {
		t.Fatalf("expected dir, got %s", stat.Kind)
	}
}

func TestRenameMissingSource(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	err := fs.Rename("/missing", "/new")
	if !IsCode(err, ENOENT) {
		t.Fatalf("expected ENOENT, got %v", err)
	}
}

func TestRenameDestinationExists(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/old", MountEntry{Kind: KindBlob, Mode: 0o644, BlobData: []byte("old")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/new", MountEntry{Kind: KindBlob, Mode: 0o644, BlobData: []byte("new")}); err != nil {
		t.Fatal(err)
	}
	err := fs.Rename("/old", "/new")
	if !IsCode(err, EEXIST) {
		t.Fatalf("expected EEXIST, got %v", err)
	}
}

func TestCloseAllHandles(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/blob", MountEntry{Kind: KindBlob, Mode: 0o666, BlobData: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	h1, err := fs.Open("/blob", OpenRead, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	h2, err := fs.Open("/blob", OpenRead, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if fs.handles.Active() != 2 {
		t.Fatalf("expected 2 active handles, got %d", fs.handles.Active())
	}
	fs.CloseAllHandles()
	if fs.handles.Active() != 0 {
		t.Fatalf("expected 0 active handles after CloseAllHandles, got %d", fs.handles.Active())
	}
	if _, err := h1.ReadAll(context.Background()); !IsCode(err, EBUSY) {
		t.Fatalf("expected EBUSY on closed handle h1, got %v", err)
	}
	if _, err := h2.ReadAll(context.Background()); !IsCode(err, EBUSY) {
		t.Fatalf("expected EBUSY on closed handle h2, got %v", err)
	}
}

func TestShutdown(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/blob", MountEntry{Kind: KindBlob, Mode: 0o666, BlobData: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	h, err := fs.Open("/blob", OpenRead, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fs.handles.Active() != 0 {
		t.Fatalf("expected 0 active handles after shutdown, got %d", fs.handles.Active())
	}
	if _, err := h.ReadAll(context.Background()); !IsCode(err, EBUSY) {
		t.Fatalf("expected EBUSY after shutdown, got %v", err)
	}
}

func TestShutdownClosesStreams(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/events", MountEntry{
		Kind: KindStream,
		Mode: 0o666,
		Stream: &StreamConfig{
			Capacity: 64,
			Mode:     BackpressureBlock,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Write(context.Background(), "/events", []byte("hello"), CallerIdentity{}); err != nil {
		t.Fatal(err)
	}
	// Drain the written data before shutdown.
	data, err := fs.Read(context.Background(), "/events", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected pre-shutdown read: %q", data)
	}
	if err := fs.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	// After shutdown the stream buffer is closed; reading should return EOF (0, nil).
	data, err = fs.Read(context.Background(), "/events", CallerIdentity{})
	if err != nil {
		t.Fatalf("expected EOF after stream close, got err=%v", err)
	}
	if len(data) != 0 {
		t.Fatalf("expected empty read after stream close, got %q", data)
	}
}

func TestMountEntryValidation(t *testing.T) {
	fs := NewFS(GlobalConfig{})

	// Invalid kind should fail.
	if err := fs.Mount("/bad", MountEntry{Kind: NodeKind("bogus"), Mode: 0o644}); err == nil {
		t.Fatal("expected error for invalid kind")
	}

	// BlobData on non-blob should fail.
	if err := fs.Mount("/bad", MountEntry{Kind: KindDir, Mode: 0o755, BlobData: []byte("x")}); err == nil {
		t.Fatal("expected error for BlobData on dir")
	}

	// LinkPath on non-link should fail.
	if err := fs.Mount("/bad", MountEntry{Kind: KindBlob, Mode: 0o644, LinkPath: "/x"}); err == nil {
		t.Fatal("expected error for LinkPath on blob")
	}

	// Stream config on non-stream should fail.
	if err := fs.Mount("/bad", MountEntry{Kind: KindBlob, Mode: 0o644, Stream: &StreamConfig{Capacity: 1}}); err == nil {
		t.Fatal("expected error for Stream on blob")
	}

	// Enabled skill without Name should fail.
	if err := fs.Mount("/bad", MountEntry{Kind: KindAPI, Mode: 0o644, Skill: &SkillConfig{Enabled: true}}); err == nil {
		t.Fatal("expected error for enabled skill without name")
	}

	// Invalid visibility should fail.
	if err := fs.Mount("/bad", MountEntry{Kind: KindBlob, Mode: 0o644, Visibility: "secret"}); err == nil {
		t.Fatal("expected error for invalid visibility")
	}

	// Valid entries should succeed.
	if err := fs.Mount("/blob", MountEntry{Kind: KindBlob, Mode: 0o644, BlobData: []byte("ok")}); err != nil {
		t.Fatalf("unexpected blob mount error: %v", err)
	}
	if err := fs.Mount("/link", MountEntry{Kind: KindLink, Mode: 0o777, LinkPath: "/blob"}); err != nil {
		t.Fatalf("unexpected link mount error: %v", err)
	}
	if err := fs.Mount("/stream", MountEntry{Kind: KindStream, Mode: 0o666, Stream: &StreamConfig{Capacity: 1}}); err != nil {
		t.Fatalf("unexpected stream mount error: %v", err)
	}
	if err := fs.Mount("/api", MountEntry{Kind: KindAPI, Mode: 0o644}); err != nil {
		t.Fatalf("unexpected api mount error: %v", err)
	}
	if err := fs.Mount("/dir", MountEntry{Kind: KindDir, Mode: 0o755}); err != nil {
		t.Fatalf("unexpected dir mount error: %v", err)
	}
}

func TestDiffSnapshots(t *testing.T) {
	old := []MountEntry{
		{Path: "/a", Kind: KindBlob, Mode: 0o644, BlobData: []byte("a-data")},
		{Path: "/b", Kind: KindDir, Mode: 0o755},
		{Path: "/c", Kind: KindLink, Mode: 0o777, LinkPath: "/a"},
	}
	new := []MountEntry{
		{Path: "/a", Kind: KindBlob, Mode: 0o644, BlobData: []byte("a-data")},
		{Path: "/b", Kind: KindDir, Mode: 0o755},
		{Path: "/d", Kind: KindBlob, Mode: 0o644, BlobData: []byte("d-data")},
	}

	diff := DiffSnapshots(old, new)
	if len(diff.Added) != 1 || diff.Added[0].Path != "/d" {
		t.Fatalf("expected /d added, got %+v", diff.Added)
	}
	if len(diff.Removed) != 1 || diff.Removed[0].Path != "/c" {
		t.Fatalf("expected /c removed, got %+v", diff.Removed)
	}
	if len(diff.Modified) != 0 {
		t.Fatalf("expected no modifications, got %+v", diff.Modified)
	}

	// Modify /b mode.
	new[1].Mode = 0o700
	diff = DiffSnapshots(old, new)
	if len(diff.Modified) != 1 || diff.Modified[0].Path != "/b" {
		t.Fatalf("expected /b modified, got %+v", diff.Modified)
	}
	if diff.Modified[0].Old.Mode != 0o755 || diff.Modified[0].New.Mode != 0o700 {
		t.Fatal("mode change not captured")
	}

	// Modify blob data.
	old[0].BlobData = []byte("old")
	new[0].BlobData = []byte("new")
	diff = DiffSnapshots(old, new)
	if len(diff.Modified) != 2 {
		t.Fatalf("expected 2 modifications, got %d", len(diff.Modified))
	}
}

func TestDiffSnapshotsEmpty(t *testing.T) {
	diff := DiffSnapshots(nil, nil)
	if len(diff.Added) != 0 || len(diff.Removed) != 0 || len(diff.Modified) != 0 {
		t.Fatal("expected empty diff for empty snapshots")
	}
}

func TestCircuitBreaker(t *testing.T) {
	fp := &fakeProvider{id: "p1", err: fmt.Errorf("fail")}
	fs := NewFS(GlobalConfig{Breaker: CircuitBreakerConfig{Enabled: true, FailureThreshold: 3, ResetTimeout: 100 * time.Millisecond, HalfOpenMaxCalls: 1}})
	if err := fs.RegisterProvider(fp); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/api", MountEntry{Kind: KindAPI, Mode: 0o644, Ops: map[OpCode]*CapConfig{OpRead: {ProviderID: "p1", Action: "test"}}});
	err != nil {
		t.Fatal(err)
	}

	// Trigger 3 failures to open the breaker.
	for i := 0; i < 3; i++ {
		_, err := fs.Read(context.Background(), "/api", CallerIdentity{})
		if err == nil {
			t.Fatal("expected provider error")
		}
	}

	// Next call should be rejected by breaker.
	_, err := fs.Read(context.Background(), "/api", CallerIdentity{})
	if err == nil {
		t.Fatal("expected breaker error")
	}
	if !IsCode(err, ECOMM) {
		t.Fatalf("expected ECOMM, got %v", err)
	}

	// Wait for reset timeout to enter half-open.
	time.Sleep(150 * time.Millisecond)

	// With fakeProvider still failing, half-open call fails and re-opens breaker.
	_, err = fs.Read(context.Background(), "/api", CallerIdentity{})
	if err == nil {
		t.Fatal("expected provider error in half-open")
	}

	// Switch provider to success.
	fp.err = nil
	fp.response = []byte("ok")

	// Wait again for half-open.
	time.Sleep(150 * time.Millisecond)

	// Half-open success should close breaker.
	data, err := fs.Read(context.Background(), "/api", CallerIdentity{})
	if err != nil {
		t.Fatalf("expected success after breaker close, got %v", err)
	}
	if string(data) != "ok" {
		t.Fatalf("unexpected data: %q", data)
	}

	// Subsequent calls should work normally.
	data, err = fs.Read(context.Background(), "/api", CallerIdentity{})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if string(data) != "ok" {
		t.Fatalf("unexpected data: %q", data)
	}
}
