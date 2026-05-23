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

	// Writer goroutine: will block because buffer (capacity 4) is full.
	buf := fs.streams.getOrCreate("/events", nil)
	done := make(chan error, 1)
	go func() {
		done <- fs.Write(context.Background(), "/events", []byte("efgh"), caller)
	}()

	// Detector: acquires buf.mu only after writer enters cond.Wait (which releases it).
	blocked := make(chan struct{})
	go func() {
		buf.mu.Lock()
		close(blocked)
		buf.mu.Unlock()
	}()
	<-blocked

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

func TestStreamReadClosedWhileBlocking(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/events", MountEntry{Kind: KindStream, Mode: 0o666}); err != nil {
		t.Fatal(err)
	}

	// Reader goroutine: will block because buffer is empty.
	buf := fs.streams.getOrCreate("/events", nil)
	done := make(chan []byte, 1)
	go func() {
		data, _ := fs.Read(context.Background(), "/events", CallerIdentity{})
		done <- data
	}()

	// Detector: acquires buf.mu only after reader enters cond.Wait (which releases it).
	blocked := make(chan struct{})
	go func() {
		buf.mu.Lock()
		close(blocked)
		buf.mu.Unlock()
	}()
	<-blocked

	if err := fs.Unmount("/events"); err != nil {
		t.Fatal(err)
	}

	select {
	case data := <-done:
		if len(data) != 0 {
			t.Fatalf("expected EOF (empty data), got %q", string(data))
		}
	case <-time.After(time.Second):
		t.Fatal("blocked reader did not wake after unmount/close")
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

func TestStreamWriteChunking(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/events", MountEntry{
		Kind: KindStream,
		Mode: 0o666,
		Stream: &StreamConfig{
			Capacity:     1024,
			Mode:         BackpressureBlock,
			MaxChunkSize: 4,
		},
	}); err != nil {
		t.Fatal(err)
	}
	caller := CallerIdentity{}

	if err := fs.Write(context.Background(), "/events", []byte("0123456789"), caller); err != nil {
		t.Fatal(err)
	}

	data, err := fs.Read(context.Background(), "/events", caller)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "0123" {
		t.Fatalf("expected first chunk '0123', got %q", string(data))
	}

	data, err = fs.Read(context.Background(), "/events", caller)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "4567" {
		t.Fatalf("expected second chunk '4567', got %q", string(data))
	}

	data, err = fs.Read(context.Background(), "/events", caller)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "89" {
		t.Fatalf("expected third chunk '89', got %q", string(data))
	}
}

func TestStreamHandleChunkedWrite(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/events", MountEntry{
		Kind: KindStream,
		Mode: 0o666,
		Stream: &StreamConfig{
			Capacity:     1024,
			Mode:         BackpressureBlock,
			MaxChunkSize: 3,
		},
	}); err != nil {
		t.Fatal(err)
	}

	hw, err := fs.Open("/events", OpenWrite, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	defer hw.Close(context.Background())

	hr, err := fs.Open("/events", OpenRead, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	defer hr.Close(context.Background())

	if err := hw.Write(context.Background(), []byte("abcdefg")); err != nil {
		t.Fatal(err)
	}

	data, err := hr.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abc" {
		t.Fatalf("expected first chunk 'abc', got %q", string(data))
	}

	data, err = hr.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "def" {
		t.Fatalf("expected second chunk 'def', got %q", string(data))
	}

	data, err = hr.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "g" {
		t.Fatalf("expected third chunk 'g', got %q", string(data))
	}
}
