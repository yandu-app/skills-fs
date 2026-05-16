package bench

import (
	"context"
	"testing"

	"github.com/skills-fs/skills-fs/core"
)

func BenchmarkHandleOpenClose(b *testing.B) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o666}); err != nil {
		b.Fatal(err)
	}
	caller := core.CallerIdentity{}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h, err := fs.Open("/blob", core.OpenRead, caller)
		if err != nil {
			b.Fatal(err)
		}
		if err := h.Close(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBlobRead(b *testing.B) {
	fs := core.NewFS(core.GlobalConfig{})
	data := make([]byte, 1024)
	for i := range data {
		data[i] = byte(i)
	}
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o444, BlobData: data}); err != nil {
		b.Fatal(err)
	}
	caller := core.CallerIdentity{}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := fs.Read(ctx, "/blob", caller); err != nil {
			b.Fatal(err)
		}
	}
}
