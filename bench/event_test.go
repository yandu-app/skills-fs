package bench

import (
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/skills-fs/skills-fs/core"
)

func BenchmarkEventEmit(b *testing.B) {
	fs := core.NewFS(core.GlobalConfig{})

	for _, listeners := range []int{1, 4, 16} {
		b.Run("listeners="+strconv.Itoa(listeners), func(b *testing.B) {
			var counter atomic.Int64
			for i := 0; i < listeners; i++ {
				fs.RegisterNotifier(func(e core.Event) {
					counter.Add(1)
				})
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := fs.Mount("/tmp-mount", core.MountEntry{Kind: core.KindBlob}); err != nil {
					b.Fatal(err)
				}
				if err := fs.Unmount("/tmp-mount"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
