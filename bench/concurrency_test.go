package bench

import (
	"context"
	"testing"

	"github.com/skills-fs/skills-fs/core"
)

type benchProvider struct{}

func (benchProvider) ID() string { return "bench" }

func (benchProvider) Invoke(context.Context, string, map[string]interface{}) (*core.ProviderResult, error) {
	return &core.ProviderResult{}, nil
}

func BenchmarkSerialQueue(b *testing.B) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.RegisterProvider(benchProvider{}); err != nil {
		b.Fatal(err)
	}
	if err := fs.Mount("/api", core.MountEntry{
		Kind:   core.KindAPI,
		Mode:   0o222,
		Serial: true,
		Ops: map[core.OpCode]*core.CapConfig{
			core.OpWrite: {ProviderID: "bench", Action: "write"},
		},
	}); err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	caller := core.CallerIdentity{}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := fs.Write(ctx, "/api", []byte("x"), caller); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkLockAcquire(b *testing.B) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o666}); err != nil {
		b.Fatal(err)
	}
	h, err := fs.Open("/blob", core.OpenRead|core.OpenWrite, core.CallerIdentity{})
	if err != nil {
		b.Fatal(err)
	}
	defer func() {
		if err := h.Close(context.Background()); err != nil {
			b.Fatal(err)
		}
	}()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := h.Flock(ctx, core.LockExclusive, false); err != nil {
			b.Fatal(err)
		}
		if err := h.Funlock(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLockContention1000(b *testing.B) {
	fs := core.NewFS(core.GlobalConfig{MaxOpenHandles: 2000})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o666}); err != nil {
		b.Fatal(err)
	}
	handles := make([]*core.Handle, 1000)
	for i := range handles {
		h, err := fs.Open("/blob", core.OpenRead|core.OpenWrite, core.CallerIdentity{})
		if err != nil {
			b.Fatal(err)
		}
		handles[i] = h
	}
	defer func() {
		for _, h := range handles {
			if err := h.Close(context.Background()); err != nil {
				b.Fatal(err)
			}
		}
	}()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			h := handles[i%len(handles)]
			i++
			if err := h.Flock(ctx, core.LockShared, false); err != nil {
				b.Fatal(err)
			}
			if err := h.Funlock(); err != nil {
				b.Fatal(err)
			}
		}
	})
}
