package bench

import (
	"context"
	"testing"

	"github.com/skills-fs/skills-fs/core"
)

func BenchmarkStatCacheHit(b *testing.B) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, BlobData: []byte("hello")}); err != nil {
		b.Fatal(err)
	}
	caller := core.CallerIdentity{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := fs.Stat("/blob", caller); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteImmediate(b *testing.B) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o222}); err != nil {
		b.Fatal(err)
	}
	caller := core.CallerIdentity{}
	payload := []byte("x")
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := fs.Write(ctx, "/blob", payload, caller); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSkillGenerate(b *testing.B) {
	root := b.TempDir()
	gen := core.NewSkillGenerator(root)
	cfg := core.SkillConfig{
		Enabled:      true,
		Name:         "bench-skill",
		Description:  "Benchmark skill generation.",
		BodyTemplate: "Use this skill in benchmarks.",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := gen.Generate(cfg); err != nil {
			b.Fatal(err)
		}
	}
}
