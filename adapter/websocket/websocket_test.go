package websocket

import (
	"context"
	"testing"
	"time"

	"golang.org/x/net/websocket"
	"github.com/skills-fs/skills-fs/adapter"
	"github.com/skills-fs/skills-fs/core"
)

func TestWebSocketReadWrite(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o666, BlobData: []byte("hello")}); err != nil {
		t.Fatal(err)
	}

	srv := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := srv.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Unmount(context.Background())

	origin := "http://" + srv.ln.Addr().String()
	url := "ws://" + srv.ln.Addr().String() + "/"
	ws, err := websocket.Dial(url, "", origin)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	if err := websocket.JSON.Send(ws, wsMsg{Op: "read", Path: "/blob"}); err != nil {
		t.Fatal(err)
	}
	var reply wsReply
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Op != "read" || reply.Data != "hello" {
		t.Fatalf("unexpected reply: %+v", reply)
	}

	if err := websocket.JSON.Send(ws, wsMsg{Op: "write", Path: "/blob", Data: "world"}); err != nil {
		t.Fatal(err)
	}
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Error != "" {
		t.Fatalf("unexpected write error: %s", reply.Error)
	}
}

func TestWebSocketSubscribe(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o666}); err != nil {
		t.Fatal(err)
	}

	srv := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := srv.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Unmount(context.Background())

	origin := "http://" + srv.ln.Addr().String()
	url := "ws://" + srv.ln.Addr().String() + "/"
	ws, err := websocket.Dial(url, "", origin)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	if err := websocket.JSON.Send(ws, wsMsg{Op: "subscribe", Prefix: "/blob"}); err != nil {
		t.Fatal(err)
	}
	var reply wsReply
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		t.Fatal(err)
	}

	if err := fs.Write(context.Background(), "/blob", []byte("x"), core.CallerIdentity{}); err != nil {
		t.Fatal(err)
	}

	// Drain the subscribe ack then wait for event.
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Op != "event" || reply.Event == nil {
		t.Fatalf("expected event reply, got %+v", reply)
	}
}

func TestWebSocketReadOnly(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o666, BlobData: []byte("hello")}); err != nil {
		t.Fatal(err)
	}

	srv := New(fs, "127.0.0.1:0", adapter.MountOptions{ReadOnly: true})
	if err := srv.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Unmount(context.Background())

	origin := "http://" + srv.ln.Addr().String()
	url := "ws://" + srv.ln.Addr().String() + "/"
	ws, err := websocket.Dial(url, "", origin)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	if err := websocket.JSON.Send(ws, wsMsg{Op: "write", Path: "/blob", Data: "x"}); err != nil {
		t.Fatal(err)
	}
	var reply wsReply
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Error != "read-only filesystem" {
		t.Fatalf("expected read-only error, got: %+v", reply)
	}
}

func TestWebSocketDialTimeout(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	srv := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := srv.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Unmount(context.Background())

	origin := "http://" + srv.ln.Addr().String()
	url := "ws://" + srv.ln.Addr().String() + "/"
	ws, err := websocket.Dial(url, "", origin)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	// Send unknown op.
	if err := websocket.JSON.Send(ws, wsMsg{Op: "nope"}); err != nil {
		t.Fatal(err)
	}
	ws.SetDeadline(time.Now().Add(2 * time.Second))
	var reply wsReply
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Error != "unknown op" {
		t.Fatalf("expected unknown op error, got: %+v", reply)
	}
}
