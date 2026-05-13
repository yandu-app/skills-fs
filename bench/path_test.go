package bench

import (
	"testing"

	"github.com/skills-fs/skills-fs/core"
)

func BenchmarkPathResolveStatic(b *testing.B) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/papers/items/latest/fulltext", core.MountEntry{Kind: core.KindBlob}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := fs.ResolveParams("/papers/items/latest/fulltext"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPathResolveParam(b *testing.B) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/papers/items/:itemId/attachments/:attId", core.MountEntry{Kind: core.KindBlob}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := fs.ResolveParams("/papers/items/p123/attachments/a456"); err != nil {
			b.Fatal(err)
		}
	}
}
