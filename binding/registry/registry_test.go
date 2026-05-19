package registry

import (
	"sync"
	"testing"

	"github.com/skills-fs/skills-fs/core"
)

func TestRegisterReturnsMonotonicHandles(t *testing.T) {
	r := New()
	fs1 := core.NewFS(core.GlobalConfig{})
	fs2 := core.NewFS(core.GlobalConfig{})
	fs3 := core.NewFS(core.GlobalConfig{})

	h1 := r.Register(fs1)
	h2 := r.Register(fs2)
	h3 := r.Register(fs3)

	if h1 == 0 {
		t.Fatalf("handle must not be zero (reserved sentinel)")
	}
	if h2 != h1+1 || h3 != h2+1 {
		t.Fatalf("handles must be monotonic: got %d, %d, %d", h1, h2, h3)
	}
	if r.Len() != 3 {
		t.Fatalf("Len: got %d want 3", r.Len())
	}
}

func TestGetReturnsRegisteredFS(t *testing.T) {
	r := New()
	fs := core.NewFS(core.GlobalConfig{})
	h := r.Register(fs)

	got, ok := r.Get(h)
	if !ok {
		t.Fatalf("Get returned ok=false for valid handle")
	}
	if got != fs {
		t.Fatalf("Get returned a different *FileSystem pointer")
	}
}

func TestGetMissingHandleReturnsFalse(t *testing.T) {
	r := New()
	if _, ok := r.Get(42); ok {
		t.Fatalf("Get returned ok=true for unknown handle")
	}
}

func TestUnregisterRemovesAndReturnsFS(t *testing.T) {
	r := New()
	fs := core.NewFS(core.GlobalConfig{})
	h := r.Register(fs)

	got, ok := r.Unregister(h)
	if !ok {
		t.Fatalf("Unregister returned ok=false for valid handle")
	}
	if got != fs {
		t.Fatalf("Unregister returned a different *FileSystem pointer")
	}
	if _, stillThere := r.Get(h); stillThere {
		t.Fatalf("handle still resolvable after Unregister")
	}
	if r.Len() != 0 {
		t.Fatalf("Len after Unregister: got %d want 0", r.Len())
	}
}

func TestUnregisterMissingHandleReturnsFalse(t *testing.T) {
	r := New()
	if fs, ok := r.Unregister(42); ok || fs != nil {
		t.Fatalf("Unregister(missing): got (%v, %v) want (nil, false)", fs, ok)
	}
}

func TestConcurrentRegisterUnregister(t *testing.T) {
	r := New()
	const goroutines = 32
	const perG = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				h := r.Register(core.NewFS(core.GlobalConfig{}))
				if _, ok := r.Get(h); !ok {
					t.Errorf("freshly registered handle %d not retrievable", h)
					return
				}
				if _, ok := r.Unregister(h); !ok {
					t.Errorf("Unregister failed for handle %d", h)
					return
				}
			}
		}()
	}
	wg.Wait()

	if r.Len() != 0 {
		t.Fatalf("Len after concurrent churn: got %d want 0", r.Len())
	}
}
