package bench

import (
	"context"
	"testing"

	"github.com/skills-fs/skills-fs/core"
)

func BenchmarkStreamWriteDrop(b *testing.B) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/events", core.MountEntry{
		Kind: core.KindStream,
		Mode: 0o666,
		Stream: &core.StreamConfig{
			Capacity: 64,
			Mode:     core.BackpressureDrop,
		},
	}); err != nil {
		b.Fatal(err)
	}
	caller := core.CallerIdentity{}
	payload := []byte("hello-world-payload-data")
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := fs.Write(ctx, "/events", payload, caller); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStreamReadWrite(b *testing.B) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/events", core.MountEntry{
		Kind: core.KindStream,
		Mode: 0o666,
		Stream: &core.StreamConfig{
			Capacity: 1024,
			Mode:     core.BackpressureBlock,
		},
	}); err != nil {
		b.Fatal(err)
	}
	caller := core.CallerIdentity{}
	payload := []byte("x")
	ctx := context.Background()

	// Drain the stream concurrently so writes never block.
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			_, _ = fs.Read(ctx, "/events", caller)
		}
	}()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := fs.Write(ctx, "/events", payload, caller); err != nil {
			b.Fatal(err)
		}
	}
}
