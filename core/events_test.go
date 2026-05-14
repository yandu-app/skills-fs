package core

import (
	"testing"
	"time"
)

func TestEventBusEmitAndReceive(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	ch := make(chan Event, 4)
	fs.RegisterNotifier(func(e Event) {
		ch <- e
	})

	if err := fs.Mount("/blob", MountEntry{Kind: KindBlob, Mode: 0o666}); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-ch:
		if e.Path != "/blob" || e.Kind != EventCreate {
			t.Fatalf("unexpected event: %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for create event")
	}

	if err := fs.Write(nil, "/blob", []byte("x"), CallerIdentity{}); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-ch:
		if e.Path != "/blob" || e.Kind != EventWrite {
			t.Fatalf("unexpected event: %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for write event")
	}

	if err := fs.Unmount("/blob"); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-ch:
		if e.Path != "/blob" || e.Kind != EventRemove {
			t.Fatalf("unexpected event: %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for remove event")
	}
}

func TestEventBusMultipleListeners(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	ch1 := make(chan Event, 2)
	ch2 := make(chan Event, 2)
	fs.RegisterNotifier(func(e Event) { ch1 <- e })
	fs.RegisterNotifier(func(e Event) { ch2 <- e })

	if err := fs.Mount("/x", MountEntry{Kind: KindBlob, Mode: 0o666}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-ch1:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for listener 1")
	}
	select {
	case <-ch2:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for listener 2")
	}
}
