package core

import (
	"context"
	"testing"
	"time"
)

func TestStreamBasicReadWrite(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/events", MountEntry{Kind: KindStream, Mode: 0o666}); err != nil {
		t.Fatal(err)
	}
	caller := CallerIdentity{UID: 1, GID: 1}

	// Write through fs.Write
	if err := fs.Write(context.Background(), "/events", []byte("hello"), caller); err != nil {
		t.Fatal(err)
	}

	// Read through fs.Read
	data, err := fs.Read(context.Background(), "/events", caller)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("expected hello, got %s", string(data))
	}
}

func TestStreamHandleNonblock(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/events", MountEntry{Kind: KindStream, Mode: 0o666}); err != nil {
		t.Fatal(err)
	}

	// Nonblock read on empty stream returns EAGAIN
	hr, err := fs.Open("/events", OpenRead|OpenNonBlock, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	defer hr.Close(context.Background())

	if _, err := hr.ReadAll(context.Background()); !IsCode(err, EAGAIN) {
		t.Fatalf("expected EAGAIN, got %v", err)
	}

	// Nonblock write on full stream returns EAGAIN in block mode
	hw, err := fs.Open("/events", OpenWrite|OpenNonBlock, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	defer hw.Close(context.Background())

	// Fill the buffer
	chunk := make([]byte, defaultStreamCapacity)
	if err := hw.Write(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	if err := hw.Write(context.Background(), []byte("x")); !IsCode(err, EAGAIN) {
		t.Fatalf("expected EAGAIN on full nonblock write, got %v", err)
	}
}

func TestStreamBackpressureDrop(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/events", MountEntry{
		Kind: KindStream,
		Mode: 0o666,
		Stream: &StreamConfig{
			Capacity: 8,
			Mode:     BackpressureDrop,
		},
	}); err != nil {
		t.Fatal(err)
	}
	caller := CallerIdentity{}

	if err := fs.Write(context.Background(), "/events", []byte("abcdefgh"), caller); err != nil {
		t.Fatal(err)
	}
	if err := fs.Write(context.Background(), "/events", []byte("XYZ"), caller); err != nil {
		t.Fatal(err)
	}

	data, err := fs.Read(context.Background(), "/events", caller)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "defghXYZ" {
		t.Fatalf("expected defghXYZ (drop oldest), got %s", string(data))
	}
}

func TestStreamBackpressureError(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/events", MountEntry{
		Kind: KindStream,
		Mode: 0o666,
		Stream: &StreamConfig{
			Capacity: 4,
			Mode:     BackpressureError,
		},
	}); err != nil {
		t.Fatal(err)
	}
	caller := CallerIdentity{}

	if err := fs.Write(context.Background(), "/events", []byte("abcd"), caller); err != nil {
		t.Fatal(err)
	}
	if err := fs.Write(context.Background(), "/events", []byte("x"), caller); !IsCode(err, ENOSPC) {
		t.Fatalf("expected ENOSPC, got %v", err)
	}
}

func TestStreamBlockWriterWaitsForReader(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/events", MountEntry{
		Kind: KindStream,
		Mode: 0o666,
		Stream: &StreamConfig{
			Capacity: 4,
			Mode:     BackpressureBlock,
		},
	}); err != nil {
		t.Fatal(err)
	}
	caller := CallerIdentity{}

	if err := fs.Write(context.Background(), "/events", []byte("abcd"), caller); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- fs.Write(context.Background(), "/events", []byte("efgh"), caller)
	}()

	// Give the goroutine time to block
	time.Sleep(20 * time.Millisecond)

	data, err := fs.Read(context.Background(), "/events", caller)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abcd" {
		t.Fatalf("expected abcd, got %s", string(data))
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("blocked write failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked writer did not unblock")
	}

	data, err = fs.Read(context.Background(), "/events", caller)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "efgh" {
		t.Fatalf("expected efgh, got %s", string(data))
	}
}

func TestStreamStatReportsAvailableBytes(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/events", MountEntry{Kind: KindStream, Mode: 0o666}); err != nil {
		t.Fatal(err)
	}
	caller := CallerIdentity{}

	st, err := fs.Stat("/events", caller)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size != 0 {
		t.Fatalf("expected size 0, got %d", st.Size)
	}

	if err := fs.Write(context.Background(), "/events", []byte("hello"), caller); err != nil {
		t.Fatal(err)
	}

	st, err = fs.Stat("/events", caller)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size != 5 {
		t.Fatalf("expected size 5, got %d", st.Size)
	}
}

func TestStreamUnmountClosesBuffer(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/events", MountEntry{Kind: KindStream, Mode: 0o666}); err != nil {
		t.Fatal(err)
	}
	caller := CallerIdentity{}

	// Pre-create the buffer by writing
	if err := fs.Write(context.Background(), "/events", []byte("x"), caller); err != nil {
		t.Fatal(err)
	}

	if err := fs.Unmount("/events"); err != nil {
		t.Fatal(err)
	}

	// Remount — should get a fresh empty buffer
	if err := fs.Mount("/events", MountEntry{Kind: KindStream, Mode: 0o666}); err != nil {
		t.Fatal(err)
	}
	// Empty stream blocks on read; verify it is fresh by writing then reading.
	if err := fs.Write(context.Background(), "/events", []byte("fresh"), caller); err != nil {
		t.Fatal(err)
	}
	data, err := fs.Read(context.Background(), "/events", caller)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fresh" {
		t.Fatalf("expected fresh buffer after remount, got %s", string(data))
	}
}

func TestStreamMultipleHandlesShareBuffer(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/events", MountEntry{Kind: KindStream, Mode: 0o666}); err != nil {
		t.Fatal(err)
	}

	h1, err := fs.Open("/events", OpenWrite, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	defer h1.Close(context.Background())

	h2, err := fs.Open("/events", OpenRead, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close(context.Background())

	if err := h1.Write(context.Background(), []byte("shared")); err != nil {
		t.Fatal(err)
	}

	data, err := h2.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "shared" {
		t.Fatalf("expected shared, got %s", string(data))
	}
}
