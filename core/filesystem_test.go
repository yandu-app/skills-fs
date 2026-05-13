package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeProvider struct {
	id       string
	err      error
	calls    []providerCall
	response []byte
}

type providerCall struct {
	action string
	params map[string]interface{}
}

func (p *fakeProvider) ID() string {
	return p.id
}

func (p *fakeProvider) Invoke(_ context.Context, action string, params map[string]interface{}) (*ProviderResult, error) {
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

func TestUnknownKindStatIsEINVAL(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/badkind", MountEntry{Kind: NodeKind("weird")}); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat("/badkind", CallerIdentity{}); !IsCode(err, EINVAL) {
		t.Fatalf("expected EINVAL, got %v", err)
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
