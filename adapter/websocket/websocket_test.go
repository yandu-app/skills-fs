package websocket

import (
	"context"
	"net/http"
	"strings"
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

	if err := websocket.JSON.Send(ws, WsMsg{Op: "read", Path: "/blob"}); err != nil {
		t.Fatal(err)
	}
	var reply WsReply
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Op != "read" || reply.Data != "hello" {
		t.Fatalf("unexpected reply: %+v", reply)
	}

	if err := websocket.JSON.Send(ws, WsMsg{Op: "write", Path: "/blob", Data: "world"}); err != nil {
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

	if err := websocket.JSON.Send(ws, WsMsg{Op: "subscribe", Prefix: "/blob"}); err != nil {
		t.Fatal(err)
	}
	var reply WsReply
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

	if err := websocket.JSON.Send(ws, WsMsg{Op: "write", Path: "/blob", Data: "x"}); err != nil {
		t.Fatal(err)
	}
	var reply WsReply
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Error != "read-only filesystem" {
		t.Fatalf("expected read-only error, got: %+v", reply)
	}
	if reply.Code != http.StatusForbidden {
		t.Fatalf("expected code 403, got %d", reply.Code)
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
	if err := websocket.JSON.Send(ws, WsMsg{Op: "nope"}); err != nil {
		t.Fatal(err)
	}
	ws.SetDeadline(time.Now().Add(2 * time.Second))
	var reply WsReply
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Error != "unknown op" {
		t.Fatalf("expected unknown op error, got: %+v", reply)
	}
	if reply.Code != http.StatusBadRequest {
		t.Fatalf("expected code 400, got %d", reply.Code)
	}
}

func TestWebSocketReadBinary(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o666, BlobData: []byte("binary-data")}); err != nil {
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

	if err := websocket.JSON.Send(ws, WsMsg{Op: "read-binary", Path: "/blob"}); err != nil {
		t.Fatal(err)
	}
	var reply WsReply
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Op != "read-binary" {
		t.Fatalf("unexpected reply op: %q", reply.Op)
	}

	var payload []byte
	if err := websocket.Message.Receive(ws, &payload); err != nil {
		t.Fatal(err)
	}
	if string(payload) != "binary-data" {
		t.Fatalf("expected binary-data, got %q", payload)
	}
}

func TestWebSocketWriteBinary(t *testing.T) {
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

	if err := websocket.JSON.Send(ws, WsMsg{Op: "write-binary", Path: "/blob"}); err != nil {
		t.Fatal(err)
	}
	if err := websocket.Message.Send(ws, []byte("raw-bytes")); err != nil {
		t.Fatal(err)
	}
	var reply WsReply
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Error != "" {
		t.Fatalf("unexpected error: %s", reply.Error)
	}

	data, err := fs.Read(context.Background(), "/blob", core.CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "raw-bytes" {
		t.Fatalf("expected raw-bytes, got %q", data)
	}
}

func TestWebSocketHealthz(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	srv := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := srv.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Unmount(context.Background())

	resp, err := http.Get("http://" + srv.ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestWebSocketConnectionCounter(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o666, BlobData: []byte("hi")}); err != nil {
		t.Fatal(err)
	}

	srv := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := srv.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Unmount(context.Background())

	if srv.ActiveConnections() != 0 {
		t.Fatalf("expected 0 connections, got %d", srv.ActiveConnections())
	}

	origin := "http://" + srv.ln.Addr().String()
	url := "ws://" + srv.ln.Addr().String() + "/"
	ws, err := websocket.Dial(url, "", origin)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	if srv.ActiveConnections() != 1 {
		t.Fatalf("expected 1 connection, got %d", srv.ActiveConnections())
	}
}

func TestWebSocketOriginAllowed(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	opts := adapter.MountOptions{AllowedOrigins: []string{"http://trusted.example.com"}}
	srv := New(fs, "127.0.0.1:0", opts)
	if err := srv.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Unmount(context.Background())

	addr := srv.ln.Addr().String()
	url := "ws://" + addr + "/"

	// Bad origin should fail handshake.
	_, err := websocket.Dial(url, "", "http://evil.com")
	if err == nil {
		t.Fatal("expected handshake failure for bad origin")
	}

	// Good origin should succeed.
	ws, err := websocket.Dial(url, "", "http://trusted.example.com")
	if err != nil {
		t.Fatalf("expected handshake success for allowed origin: %v", err)
	}
	ws.Close()
}

func TestWebSocketMaxPayload(t *testing.T) {
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

	// Send a message larger than 64 KiB.
	big := strings.Repeat("x", 128*1024)
	if err := websocket.JSON.Send(ws, WsMsg{Op: "write", Path: "/blob", Data: big}); err != nil {
		// The send itself may succeed; the receive should fail or error out.
		t.Logf("send error (acceptable): %v", err)
	}
	ws.SetDeadline(time.Now().Add(2 * time.Second))
	var reply WsReply
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		// Receiving a too-large frame should close the connection.
		t.Logf("receive error (expected): %v", err)
	}
}
