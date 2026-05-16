package bench

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skills-fs/skills-fs/core"
	httpprovider "github.com/skills-fs/skills-fs/provider/http"
)

func BenchmarkHTTPProviderInvoke(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	p := httpprovider.NewProvider("remote", server.URL)
	ctx := context.Background()
	params := map[string]interface{}{"key": "value"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := p.Invoke(ctx, "action", params); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHTTPProviderThroughFS(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	fs := core.NewFS(core.GlobalConfig{})
	p := httpprovider.NewProvider("remote", server.URL)
	if err := fs.RegisterProvider(p); err != nil {
		b.Fatal(err)
	}
	if err := fs.Mount("/api", core.MountEntry{
		Kind: core.KindAPI,
		Mode: 0o444,
		Ops: map[core.OpCode]*core.CapConfig{
			core.OpRead: {ProviderID: "remote", Action: "greet"},
		},
	}); err != nil {
		b.Fatal(err)
	}
	caller := core.CallerIdentity{}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := fs.Read(ctx, "/api", caller); err != nil {
			b.Fatal(err)
		}
	}
}
