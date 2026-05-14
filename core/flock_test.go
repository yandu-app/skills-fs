package core

import (
	"context"
	"testing"
	"time"
)

func TestFlockContextCancellation(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/blob", MountEntry{Kind: KindBlob, Mode: 0o666}); err != nil {
		t.Fatal(err)
	}
	h1, err := fs.Open("/blob", OpenRead|OpenWrite, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	defer h1.Close(context.Background())
	h2, err := fs.Open("/blob", OpenRead|OpenWrite, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close(context.Background())

	if err := h1.Flock(context.Background(), LockExclusive, false); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- h2.Flock(ctx, LockExclusive, false)
	}()

	// Give h2 time to enter the wait loop.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !IsCode(err, ETIMEDOUT) {
			t.Fatalf("expected ETIMEDOUT on cancelled context, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("h2 did not wake after context cancellation")
	}
}

func TestFlockDeadlineTimeout(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/blob", MountEntry{Kind: KindBlob, Mode: 0o666}); err != nil {
		t.Fatal(err)
	}
	h1, err := fs.Open("/blob", OpenRead|OpenWrite, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	defer h1.Close(context.Background())
	h2, err := fs.Open("/blob", OpenRead|OpenWrite, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close(context.Background())

	if err := h1.Flock(context.Background(), LockExclusive, false); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(10*time.Millisecond))
	defer cancel()

	err = h2.Flock(ctx, LockExclusive, false)
	if !IsCode(err, ETIMEDOUT) {
		t.Fatalf("expected ETIMEDOUT on deadline, got %v", err)
	}
}
