package core

import (
	"context"
	"encoding/json"
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
	invokeCh chan struct{}   // test hook: receives signal when Invoke completes
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
	defer func() {
		if p.invokeCh != nil {
			select {
			case p.invokeCh <- struct{}{}:
			default:
			}
		}
	}()
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
	p := &fakeProvider{id: "async", response: []byte("delayed"), invokeCh: make(chan struct{}, 8)}
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

	// Wait for background invocation to complete (deterministic).
	<-p.invokeCh
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

func TestShutdownRespectsContextCancellation(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/blob", MountEntry{Kind: KindBlob, Mode: 0o666, BlobData: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	_, _ = fs.Open("/blob", OpenRead, CallerIdentity{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := fs.Shutdown(ctx); err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
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
	if err := fs.Mount("/api", MountEntry{Kind: KindAPI, Mode: 0o644, Ops: map[OpCode]*CapConfig{OpRead: {ProviderID: "p1", Action: "test"}}}); err != nil {
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

	// Rewind lastFailure so breakerOpen sees reset timeout has elapsed.
	b := fs.breakerFor("p1")
	b.mu.Lock()
	b.lastFailure = time.Now().Add(-200 * time.Millisecond)
	b.mu.Unlock()

	// With fakeProvider still failing, half-open call fails and re-opens breaker.
	_, err = fs.Read(context.Background(), "/api", CallerIdentity{})
	if err == nil {
		t.Fatal("expected provider error in half-open")
	}

	// Switch provider to success.
	fp.err = nil
	fp.response = []byte("ok")

	// Rewind lastFailure again for second half-open attempt.
	b.mu.Lock()
	b.lastFailure = time.Now().Add(-200 * time.Millisecond)
	b.mu.Unlock()

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

func TestAuditLogging(t *testing.T) {
	var entries []AuditEntry
	audit := func(e AuditEntry) {
		entries = append(entries, e)
	}

	fs := NewFS(GlobalConfig{AuditFunc: audit, DefaultUID: 1, DefaultGID: 1})
	caller := CallerIdentity{UID: 1, GID: 1}

	if err := fs.Mount("/blob", MountEntry{Kind: KindBlob, Mode: 0o644, BlobData: []byte("hello")}); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat("/blob", caller); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Read(context.Background(), "/blob", caller); err != nil {
		t.Fatal(err)
	}
	if err := fs.Write(context.Background(), "/blob", []byte("world"), caller); err != nil {
		t.Fatal(err)
	}
	if err := fs.Unmount("/blob"); err != nil {
		t.Fatal(err)
	}
	// Error path: stat missing path.
	if _, err := fs.Stat("/missing", caller); err == nil {
		t.Fatal("expected error")
	}

	wantOps := []string{"mount", "stat", "read", "write", "unmount", "stat"}
	if len(entries) != len(wantOps) {
		t.Fatalf("expected %d audit entries, got %d", len(wantOps), len(entries))
	}
	for i, want := range wantOps {
		if entries[i].Op != want {
			t.Fatalf("entry %d: expected op %q, got %q", i, want, entries[i].Op)
		}
		if entries[i].Duration < 0 {
			t.Fatalf("entry %d: negative duration", i)
		}
	}
	// Verify the last stat recorded an error.
	if entries[len(entries)-1].Err == nil {
		t.Fatal("expected error in last audit entry")
	}
	// Verify caller is captured for stat/read/write.
	if entries[1].Caller != caller {
		t.Fatalf("expected caller %+v, got %+v", caller, entries[1].Caller)
	}
}

type slowProvider struct {
	id    string
	delay time.Duration
}

func (p *slowProvider) ID() string { return p.id }
func (p *slowProvider) Invoke(ctx context.Context, action string, params map[string]interface{}) (*ProviderResult, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(p.delay):
		return &ProviderResult{Data: []byte("ok")}, nil
	}
}

func TestProviderTimeout(t *testing.T) {
	p := &slowProvider{id: "slow", delay: 200 * time.Millisecond}
	fs := NewFS(GlobalConfig{})
	if err := fs.RegisterProvider(p); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/api", MountEntry{
		Kind: KindAPI,
		Mode: 0o644,
		Ops: map[OpCode]*CapConfig{
			OpRead: {ProviderID: "slow", Action: "test", Timeout: 10 * time.Millisecond},
		},
	}); err != nil {
		t.Fatal(err)
	}

	_, err := fs.Read(context.Background(), "/api", CallerIdentity{})
	if !IsCode(err, ETIMEDOUT) {
		t.Fatalf("expected ETIMEDOUT, got %v", err)
	}
}

func TestProviderTimeoutSuccess(t *testing.T) {
	p := &slowProvider{id: "slow", delay: 5 * time.Millisecond}
	fs := NewFS(GlobalConfig{})
	if err := fs.RegisterProvider(p); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/api", MountEntry{
		Kind: KindAPI,
		Mode: 0o644,
		Ops: map[OpCode]*CapConfig{
			OpRead: {ProviderID: "slow", Action: "test", Timeout: 200 * time.Millisecond},
		},
	}); err != nil {
		t.Fatal(err)
	}

	data, err := fs.Read(context.Background(), "/api", CallerIdentity{})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if string(data) != "ok" {
		t.Fatalf("unexpected data %q", data)
	}
}

func TestNamespaceIsolationRead(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/ns-a/data", MountEntry{Kind: KindBlob, Mode: 0o644, BlobData: []byte("a"), Namespace: "tenant-a"}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/ns-b/data", MountEntry{Kind: KindBlob, Mode: 0o644, BlobData: []byte("b"), Namespace: "tenant-b"}); err != nil {
		t.Fatal(err)
	}

	// tenant-a can read their own data.
	data, err := fs.Read(context.Background(), "/ns-a/data", CallerIdentity{Namespace: "tenant-a"})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "a" {
		t.Fatalf("expected a, got %q", data)
	}

	// tenant-a cannot read tenant-b data.
	_, err = fs.Read(context.Background(), "/ns-b/data", CallerIdentity{Namespace: "tenant-a"})
	if !IsCode(err, ENOENT) {
		t.Fatalf("expected ENOENT cross-namespace read, got %v", err)
	}

	// tenant-b cannot read tenant-a data.
	_, err = fs.Read(context.Background(), "/ns-a/data", CallerIdentity{Namespace: "tenant-b"})
	if !IsCode(err, ENOENT) {
		t.Fatalf("expected ENOENT cross-namespace read, got %v", err)
	}
}

func TestNamespaceIsolationReaddir(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/dir/a", MountEntry{Kind: KindBlob, Mode: 0o644, BlobData: []byte("a"), Namespace: "tenant-a"}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/dir/b", MountEntry{Kind: KindBlob, Mode: 0o644, BlobData: []byte("b"), Namespace: "tenant-b"}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/dir/c", MountEntry{Kind: KindBlob, Mode: 0o644, BlobData: []byte("c")}); err != nil {
		t.Fatal(err)
	}

	// tenant-a sees only a and c (global).
	entries, err := fs.Readdir("/dir", CallerIdentity{Namespace: "tenant-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries for tenant-a, got %d", len(entries))
	}
	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
	}
	if !names["a"] || !names["c"] {
		t.Fatalf("expected a and c, got %+v", names)
	}
	if names["b"] {
		t.Fatal("tenant-a should not see b")
	}
}

func TestNamespaceIsolationGlobalMount(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/shared", MountEntry{Kind: KindBlob, Mode: 0o644, BlobData: []byte("shared")}); err != nil {
		t.Fatal(err)
	}

	// Any tenant can access global mounts.
	for _, ns := range []string{"tenant-a", "tenant-b", ""} {
		data, err := fs.Read(context.Background(), "/shared", CallerIdentity{Namespace: ns})
		if err != nil {
			t.Fatalf("namespace %q: %v", ns, err)
		}
		if string(data) != "shared" {
			t.Fatalf("namespace %q: expected shared, got %q", ns, data)
		}
	}
}

func TestNamespaceIsolationStat(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/secret", MountEntry{Kind: KindBlob, Mode: 0o644, BlobData: []byte("x"), Namespace: "private"}); err != nil {
		t.Fatal(err)
	}

	_, err := fs.Stat("/secret", CallerIdentity{Namespace: "other"})
	if !IsCode(err, ENOENT) {
		t.Fatalf("expected ENOENT, got %v", err)
	}

	st, err := fs.Stat("/secret", CallerIdentity{Namespace: "private"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Kind != KindBlob {
		t.Fatalf("expected blob, got %s", st.Kind)
	}
}

func TestNamespaceIsolationOpen(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/file", MountEntry{Kind: KindBlob, Mode: 0o644, BlobData: []byte("x"), Namespace: "ns1"}); err != nil {
		t.Fatal(err)
	}

	_, err := fs.Open("/file", OpenRead, CallerIdentity{Namespace: "ns2"})
	if !IsCode(err, ENOENT) {
		t.Fatalf("expected ENOENT, got %v", err)
	}

	h, err := fs.Open("/file", OpenRead, CallerIdentity{Namespace: "ns1"})
	if err != nil {
		t.Fatal(err)
	}
	h.Close(context.Background())
}

type countingProvider struct {
	id        string
	callCount int
}

func (p *countingProvider) ID() string { return p.id }
func (p *countingProvider) Invoke(_ context.Context, action string, params map[string]interface{}) (*ProviderResult, error) {
	p.callCount++
	return &ProviderResult{Data: []byte(fmt.Sprintf("call-%d", p.callCount))}, nil
}

func TestProviderCacheHit(t *testing.T) {
	p := &countingProvider{id: "cached"}
	fs := NewFS(GlobalConfig{})
	if err := fs.RegisterProvider(p); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/api", MountEntry{
		Kind: KindAPI,
		Mode: 0o644,
		Ops: map[OpCode]*CapConfig{
			OpRead: {ProviderID: "cached", Action: "test", CacheTTL: 100 * time.Millisecond},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// First call hits the provider.
	data, err := fs.Read(context.Background(), "/api", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "call-1" {
		t.Fatalf("expected call-1, got %q", data)
	}

	// Second call should be cached.
	data, err = fs.Read(context.Background(), "/api", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "call-1" {
		t.Fatalf("expected cached call-1, got %q", data)
	}
	if p.callCount != 1 {
		t.Fatalf("expected 1 provider call, got %d", p.callCount)
	}

	// Expire cache entry deterministically.
	fs.providerCacheMu.Lock()
	for k, ent := range fs.providerCache {
		ent.expires = time.Now().Add(-time.Second)
		fs.providerCache[k] = ent
	}
	fs.providerCacheMu.Unlock()

	// Third call should hit the provider again.
	data, err = fs.Read(context.Background(), "/api", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "call-2" {
		t.Fatalf("expected call-2 after expiry, got %q", data)
	}
	if p.callCount != 2 {
		t.Fatalf("expected 2 provider calls, got %d", p.callCount)
	}
}

func TestProviderCacheDisabled(t *testing.T) {
	p := &countingProvider{id: "nocache"}
	fs := NewFS(GlobalConfig{})
	if err := fs.RegisterProvider(p); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/api", MountEntry{
		Kind: KindAPI,
		Mode: 0o644,
		Ops: map[OpCode]*CapConfig{
			OpRead: {ProviderID: "nocache", Action: "test"}, // no CacheTTL
		},
	}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		_, err := fs.Read(context.Background(), "/api", CallerIdentity{})
		if err != nil {
			t.Fatal(err)
		}
	}
	if p.callCount != 3 {
		t.Fatalf("expected 3 provider calls without cache, got %d", p.callCount)
	}
}

func TestMountEntryEqual(t *testing.T) {
	base := MountEntry{Kind: KindBlob, Mode: 0o644, UID: 1, GID: 2, BlobData: []byte("x")}
	cases := []struct {
		name string
		a, b MountEntry
		want bool
	}{
		{"identical", base, base, true},
		{"different kind", base, MountEntry{Kind: KindDir, Mode: 0o644, UID: 1, GID: 2, BlobData: []byte("x")}, false},
		{"different mode", base, MountEntry{Kind: KindBlob, Mode: 0o755, UID: 1, GID: 2, BlobData: []byte("x")}, false},
		{"different uid", base, MountEntry{Kind: KindBlob, Mode: 0o644, UID: 99, GID: 2, BlobData: []byte("x")}, false},
		{"different gid", base, MountEntry{Kind: KindBlob, Mode: 0o644, UID: 1, GID: 99, BlobData: []byte("x")}, false},
		{"different link", MountEntry{Kind: KindLink, LinkPath: "/a"}, MountEntry{Kind: KindLink, LinkPath: "/b"}, false},
		{"different visibility", MountEntry{Kind: KindBlob, Visibility: "public"}, MountEntry{Kind: KindBlob, Visibility: "private"}, false},
		{"different blob", base, MountEntry{Kind: KindBlob, Mode: 0o644, UID: 1, GID: 2, BlobData: []byte("y")}, false},
		{"stream nil vs set", MountEntry{Kind: KindStream, Stream: nil}, MountEntry{Kind: KindStream, Stream: &StreamConfig{Capacity: 1}}, false},
		{"stream capacity", MountEntry{Kind: KindStream, Stream: &StreamConfig{Capacity: 1}}, MountEntry{Kind: KindStream, Stream: &StreamConfig{Capacity: 2}}, false},
		{"stream mode", MountEntry{Kind: KindStream, Stream: &StreamConfig{Capacity: 1, Mode: BackpressureBlock}}, MountEntry{Kind: KindStream, Stream: &StreamConfig{Capacity: 1, Mode: BackpressureDrop}}, false},
		{"skill nil vs set", MountEntry{Kind: KindBlob, Skill: nil}, MountEntry{Kind: KindBlob, Skill: &SkillConfig{Name: "x"}}, false},
		{"skill name", MountEntry{Kind: KindBlob, Skill: &SkillConfig{Name: "a"}}, MountEntry{Kind: KindBlob, Skill: &SkillConfig{Name: "b"}}, false},
		{"skill enabled", MountEntry{Kind: KindBlob, Skill: &SkillConfig{Name: "a", Enabled: true}}, MountEntry{Kind: KindBlob, Skill: &SkillConfig{Name: "a", Enabled: false}}, false},
		{"same stream", MountEntry{Kind: KindStream, Stream: &StreamConfig{Capacity: 10, Mode: BackpressureDrop}}, MountEntry{Kind: KindStream, Stream: &StreamConfig{Capacity: 10, Mode: BackpressureDrop}}, true},
		{"same skill", MountEntry{Kind: KindBlob, Skill: &SkillConfig{Name: "a", Enabled: true}}, MountEntry{Kind: KindBlob, Skill: &SkillConfig{Name: "a", Enabled: true}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mountEntryEqual(tc.a, tc.b)
			if got != tc.want {
				t.Fatalf("mountEntryEqual(%+v, %+v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestFilterDirEntriesByNamespace(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/ns-a", MountEntry{Kind: KindBlob, Mode: 0o644, Namespace: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/ns-b", MountEntry{Kind: KindBlob, Mode: 0o644, Namespace: "beta"}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/global", MountEntry{Kind: KindBlob, Mode: 0o644, Namespace: ""}); err != nil {
		t.Fatal(err)
	}

	// Caller in alpha namespace should see alpha and global, but not beta.
	caller := CallerIdentity{Namespace: "alpha"}
	entries, err := fs.Readdir("/", caller)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	for _, want := range []string{"global", "ns-a", "skills", "sys"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected entry %q in root dir, got %v", want, names)
		}
	}
	for _, reject := range []string{"ns-b"} {
		for _, n := range names {
			if n == reject {
				t.Fatalf("expected entry %q to be filtered from root dir", reject)
			}
		}
	}

	// Empty-namespace caller sees everything.
	allEntries, err := fs.Readdir("/", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	var allNames []string
	for _, e := range allEntries {
		allNames = append(allNames, e.Name)
	}
	for _, want := range []string{"global", "ns-a", "ns-b", "skills", "sys"} {
		found := false
		for _, n := range allNames {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected entry %q in root dir for global caller, got %v", want, allNames)
		}
	}
}

func TestProviderHealth(t *testing.T) {
	fs := NewFS(GlobalConfig{})

	// Provider without health check.
	p1 := &fakeProvider{id: "p1"}
	if err := fs.RegisterProvider(p1); err != nil {
		t.Fatal(err)
	}

	// Provider with health check.
	p2 := &healthyProvider{id: "p2"}
	if err := fs.RegisterProvider(p2); err != nil {
		t.Fatal(err)
	}

	// Provider with failing health check.
	p3 := &healthyProvider{id: "p3", err: errors.New("boom")}
	if err := fs.RegisterProvider(p3); err != nil {
		t.Fatal(err)
	}

	h := fs.ProviderHealth(context.Background())
	if h["p1"] != "unknown" {
		t.Fatalf("expected p1 unknown, got %q", h["p1"])
	}
	if h["p2"] != "healthy" {
		t.Fatalf("expected p2 healthy, got %q", h["p2"])
	}
	if h["p3"] != "unhealthy: boom" {
		t.Fatalf("expected p3 unhealthy, got %q", h["p3"])
	}
}

type healthyProvider struct {
	id  string
	err error
}

func (p *healthyProvider) ID() string { return p.id }
func (p *healthyProvider) Invoke(ctx context.Context, action string, params map[string]interface{}) (*ProviderResult, error) {
	return nil, nil
}
func (p *healthyProvider) HealthCheck(ctx context.Context) error {
	return p.err
}

func TestStatReservedPaths(t *testing.T) {
	fs := NewFS(GlobalConfig{})

	st, err := fs.Stat("/sys", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Kind != KindDir {
		t.Fatalf("expected /sys to be dir, got %s", st.Kind)
	}

	st, err = fs.Stat("/sys/metrics", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Kind != KindBlob {
		t.Fatalf("expected /sys/metrics to be blob, got %s", st.Kind)
	}
}

func TestStatSkillDir(t *testing.T) {
	root := t.TempDir()
	fs := NewFS(GlobalConfig{SkillsRoot: root})
	if err := fs.skills.Generate(SkillConfig{
		Name:        "test-skill",
		Description: "d",
		Enabled:     true,
	}); err != nil {
		t.Fatal(err)
	}

	st, err := fs.Stat("/skills/test-skill", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Kind != KindDir {
		t.Fatalf("expected skill dir, got %s", st.Kind)
	}
}

func TestExposeAtRootMountsSkillFile(t *testing.T) {
	root := t.TempDir()
	fs := NewFS(GlobalConfig{SkillsRoot: root})
	if err := fs.Mount("/api", MountEntry{
		Kind: KindAPI,
		Mode: 0o444,
		Skill: &SkillConfig{
			Name:         "test-skill",
			Description:  "d",
			Enabled:      true,
			ExposeAtRoot: true,
			BodyTemplate: "# Test\n",
		},
	}); err != nil {
		t.Fatal(err)
	}
	st, err := fs.Stat("/SKILL.md", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Kind != KindBlob {
		t.Fatalf("expected /SKILL.md to be blob, got %s", st.Kind)
	}
	data, err := fs.Read(context.Background(), "/SKILL.md", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty /SKILL.md")
	}
}

func TestExposeAtRootConflict(t *testing.T) {
	root := t.TempDir()
	fs := NewFS(GlobalConfig{SkillsRoot: root})
	if err := fs.Mount("/SKILL.md", MountEntry{Kind: KindBlob, Mode: 0o444, BlobData: []byte("existing")}); err != nil {
		t.Fatal(err)
	}
	err := fs.Mount("/api", MountEntry{
		Kind: KindAPI,
		Mode: 0o444,
		Skill: &SkillConfig{
			Name:         "test-skill",
			Description:  "d",
			Enabled:      true,
			ExposeAtRoot: true,
			BodyTemplate: "# Test\n",
		},
	})
	if err == nil {
		t.Fatal("expected error when /SKILL.md already exists")
	}
}

func TestStatSkillFileDeleted(t *testing.T) {
	root := t.TempDir()
	fs := NewFS(GlobalConfig{SkillsRoot: root})
	if err := fs.skills.Generate(SkillConfig{
		Name:        "test-skill",
		Description: "d",
		Enabled:     true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "test-skill", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	_, err := fs.Stat("/skills/test-skill/SKILL.md", CallerIdentity{})
	if err == nil {
		t.Fatal("expected error when skill file is missing")
	}
}

func TestOnShutdownHooks(t *testing.T) {
	fs := NewFS(GlobalConfig{})

	var order []int
	fs.OnShutdown(func() error {
		order = append(order, 1)
		return nil
	})
	fs.OnShutdown(func() error {
		order = append(order, 2)
		return nil
	})
	fs.OnShutdown(func() error {
		order = append(order, 3)
		return nil
	})

	if err := fs.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Hooks run in reverse registration order (LIFO).
	want := []int{3, 2, 1}
	if len(order) != len(want) {
		t.Fatalf("expected %d hooks, got %d", len(want), len(order))
	}
	for i, v := range want {
		if order[i] != v {
			t.Fatalf("hook %d: expected %d, got %d", i, v, order[i])
		}
	}
}

func TestOnShutdownHookErrorIsTolerated(t *testing.T) {
	fs := NewFS(GlobalConfig{})

	var ran bool
	fs.OnShutdown(func() error {
		return errors.New("boom")
	})
	fs.OnShutdown(func() error {
		ran = true
		return nil
	})

	if err := fs.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("expected second hook to run despite first hook error")
	}
}

func TestOnShutdownHooksClearedAfterRun(t *testing.T) {
	fs := NewFS(GlobalConfig{})

	var calls int
	fs.OnShutdown(func() error {
		calls++
		return nil
	})

	if err := fs.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := fs.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected hook to run once, got %d", calls)
	}
}

func TestNewFSPanicsOnInvalidConfig(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for invalid config")
		}
	}()
	_ = NewFS(GlobalConfig{MaxOpenHandles: -1})
}

func TestResolveParamsErrors(t *testing.T) {
	fs := NewFS(GlobalConfig{})

	// Unmatched path.
	_, _, err := fs.ResolveParams("/missing")
	if !IsCode(err, ENOENT) {
		t.Fatalf("expected ENOENT, got %v", err)
	}

	// Invalid path format.
	_, _, err = fs.ResolveParams("bad-path")
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
}

func TestRestoreReturnsFirstError(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Restore([]MountEntry{
		{Path: "no-leading-slash", Kind: KindBlob, Mode: 0o644},
	}); err == nil {
		t.Fatal("expected error for invalid mount entry")
	}
}

func TestReadLinkInvalidPath(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	_, err := fs.ReadLink("relative-path")
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
}

func TestMountAPIWithoutProviders(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	err := fs.Mount("/api", MountEntry{
		Kind: KindAPI,
		Mode: 0o644,
		Ops: map[OpCode]*CapConfig{
			OpRead: {ProviderID: "missing", Action: "test"},
		},
	})
	if !IsCode(err, EINVAL) {
		t.Fatalf("expected EINVAL, got %v", err)
	}
}

func TestMountNilOpInMap(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	p := &fakeProvider{id: "fp"}
	if err := fs.RegisterProvider(p); err != nil {
		t.Fatal(err)
	}
	err := fs.Mount("/api", MountEntry{
		Kind: KindAPI,
		Mode: 0o644,
		Ops: map[OpCode]*CapConfig{
			OpRead:  {ProviderID: "fp", Action: "test"},
			OpWrite: nil,
		},
	})
	if !IsCode(err, EINVAL) {
		t.Fatalf("expected EINVAL, got %v", err)
	}
}

func TestHandleReadAllWithoutOpenRead(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/blob", MountEntry{Kind: KindBlob, Mode: 0o666, BlobData: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	h, err := fs.Open("/blob", OpenWrite, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close(context.Background())
	if _, err := h.ReadAll(context.Background()); !IsCode(err, EACCES) {
		t.Fatalf("expected EACCES, got %v", err)
	}
}

func TestFollowLinkMissingPath(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	_, err := fs.FollowLink("/missing")
	if !IsCode(err, ENOENT) {
		t.Fatalf("expected ENOENT, got %v", err)
	}
}

func TestDynamicDir(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	p := &dynamicProvider{}
	if err := fs.RegisterProvider(p); err != nil {
		t.Fatal(err)
	}
	mustMount := func(path string, kind NodeKind, action string) {
		err := fs.Mount(path, MountEntry{
			Path: path,
			Kind: kind,
			Mode: 0o755,
			Ops: map[OpCode]*CapConfig{
				OpRead: {ProviderID: "dyn", Action: action},
			},
		})
		if err != nil {
			t.Fatalf("mount %s: %v", path, err)
		}
	}
	mustMount("/groups", KindDynamicDir, "list_groups")
	mustMount("/groups/:group_id", KindDynamicDir, "list_ranges")
	mustMount("/groups/:group_id/:time_range", KindDynamicDir, "list_msgs")
	mustMount("/groups/:group_id/:time_range/:message_id", KindAPI, "get_msg")

	entries, err := fs.Readdir("/groups", CallerIdentity{})
	if err != nil {
		t.Fatalf("readdir /groups: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "g1" || entries[0].Kind != KindDynamicDir {
		t.Fatalf("unexpected /groups entries: %v", entries)
	}

	entries, err = fs.Readdir("/groups/g1", CallerIdentity{})
	if err != nil {
		t.Fatalf("readdir /groups/g1: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "recent" || entries[0].Kind != KindDynamicDir {
		t.Fatalf("unexpected /groups/g1 entries: %v", entries)
	}

	entries, err = fs.Readdir("/groups/g1/recent", CallerIdentity{})
	if err != nil {
		t.Fatalf("readdir /groups/g1/recent: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "m1" || entries[0].Kind != KindAPI {
		t.Fatalf("unexpected /groups/g1/recent entries: %v", entries)
	}

	data, err := fs.Read(context.Background(), "/groups/g1/recent/m1", CallerIdentity{})
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	if string(data) != `{"text":"hello"}` {
		t.Fatalf("unexpected message data: %s", data)
	}
}

type dynamicProvider struct{}

func (p *dynamicProvider) ID() string { return "dyn" }

func (p *dynamicProvider) Invoke(_ context.Context, action string, params map[string]interface{}) (*ProviderResult, error) {
	switch action {
	case "list_groups":
		return &ProviderResult{Data: []byte(`{"entries":[{"name":"g1","kind":"dynamic_dir"}]}`)}, nil
	case "list_ranges":
		return &ProviderResult{Data: []byte(`{"entries":[{"name":"recent","kind":"dynamic_dir"}]}`)}, nil
	case "list_msgs":
		return &ProviderResult{Data: []byte(`{"entries":[{"name":"m1","kind":"api"}]}`)}, nil
	case "get_msg":
		return &ProviderResult{Data: []byte(`{"text":"hello"}`)}, nil
	}
	return nil, fmt.Errorf("unknown action %s", action)
}

func TestWritebackStoresResult(t *testing.T) {
	provider := &fakeProvider{id: "p", response: []byte(`{"ok":true}`)}
	fs := NewFS(GlobalConfig{})
	if err := fs.RegisterProvider(provider); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/cmd", MountEntry{
		Kind:      KindAPI,
		Mode:      0o666,
		Writeback: true,
		Ops: map[OpCode]*CapConfig{
			OpRead:  {ProviderID: "p", Action: "read"},
			OpWrite: {ProviderID: "p", Action: "write"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	// Write succeeds — result should be stored in writeback map.
	if err := fs.Write(context.Background(), "/cmd", []byte(`{"x":1}`), CallerIdentity{}); err != nil {
		t.Fatal(err)
	}
	// Read should return the stored result, not invoke the provider again.
	data, err := fs.Read(context.Background(), "/cmd", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"ok":true}` {
		t.Fatalf("expected writeback result, got %s", data)
	}
}

func TestWritebackErrorReturnsSchema(t *testing.T) {
	provider := &fakeProvider{id: "p", err: errors.New("provider failed")}
	fs := NewFS(GlobalConfig{})
	if err := fs.RegisterProvider(provider); err != nil {
		t.Fatal(err)
	}
	schema := `{"example":{"key":"value"}}`
	if err := fs.Mount("/cmd", MountEntry{
		Kind:      KindAPI,
		Mode:      0o666,
		Writeback: true,
		Schema:    schema,
		Ops: map[OpCode]*CapConfig{
			OpWrite: {ProviderID: "p", Action: "write"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	// Write fails — writeback should store schema error, but return nil (success).
	if err := fs.Write(context.Background(), "/cmd", []byte(`{"x":1}`), CallerIdentity{}); err != nil {
		t.Fatalf("writeback should return nil on error, got: %v", err)
	}
	// Read should return the schema error JSON.
	data, err := fs.Read(context.Background(), "/cmd", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("writeback read should return valid JSON: %s", data)
	}
	if _, ok := resp["error"]; !ok {
		t.Fatalf("expected 'error' field in writeback result: %s", data)
	}
	if _, ok := resp["expected_schema"]; !ok {
		t.Fatalf("expected 'expected_schema' field in writeback result: %s", data)
	}
	if _, ok := resp["hint"]; !ok {
		t.Fatalf("expected 'hint' field in writeback result: %s", data)
	}
}

func TestNoWritebackWithoutFlag(t *testing.T) {
	provider := &fakeProvider{id: "p", response: []byte(`{"ok":true}`)}
	fs := NewFS(GlobalConfig{})
	if err := fs.RegisterProvider(provider); err != nil {
		t.Fatal(err)
	}
	// Writeback=false (default): read should invoke provider, not return cached.
	if err := fs.Mount("/cmd", MountEntry{
		Kind: KindAPI,
		Mode:      0o666,
		Ops: map[OpCode]*CapConfig{
			OpRead:  {ProviderID: "p", Action: "read"},
			OpWrite: {ProviderID: "p", Action: "write"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	provider.calls = nil
	// Write.
	if err := fs.Write(context.Background(), "/cmd", []byte(`{"x":1}`), CallerIdentity{}); err != nil {
		t.Fatal(err)
	}
	// Read should invoke provider (not hit writeback cache).
	_, err := fs.Read(context.Background(), "/cmd", CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.calls) == 0 {
		t.Fatal("expected provider to be called on read when writeback is disabled")
	}
}

func TestRawWriteParamsPassesTrimmedPayload(t *testing.T) {
	provider := &fakeProvider{id: "p", response: []byte(`{"ok":true}`)}
	fs := NewFS(GlobalConfig{})
	if err := fs.RegisterProvider(provider); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/raw-cmd/:id", MountEntry{
		Kind:      KindAPI,
		Mode:      0o666,
		Writeback: true,
		Ops: map[OpCode]*CapConfig{
			OpWrite: {
				ProviderID: "p",
				Action:     "raw.write",
				ParamsFn: func(pathParams map[string]string, payload []byte, _ OpContext) (map[string]interface{}, error) {
					params := make(map[string]interface{}, len(pathParams)+1)
					for k, v := range pathParams {
						params[k] = v
					}
					params["_payload"] = func() string {
						s := string(payload)
						for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
							s = s[:len(s)-1]
						}
						for len(s) > 0 && (s[0] == '\n' || s[0] == '\r' || s[0] == ' ') {
							s = s[1:]
						}
						return s
					}()
					return params, nil
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Write(context.Background(), "/raw-cmd/42", []byte("hello\n"), CallerIdentity{}); err != nil {
		t.Fatal(err)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(provider.calls))
	}
	payload, ok := provider.calls[0].params["_payload"]
	if !ok {
		t.Fatal("expected _payload in params")
	}
	if payload != "hello" {
		t.Fatalf("expected trimmed payload 'hello', got %q", payload)
	}
	id, ok := provider.calls[0].params["id"]
	if !ok || id != "42" {
		t.Fatalf("expected path param id=42, got %v", id)
	}
}
