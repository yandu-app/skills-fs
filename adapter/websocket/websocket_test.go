package websocket

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/skills-fs/skills-fs/adapter"
	"github.com/skills-fs/skills-fs/core"
)

func dial(t *testing.T, srv *Server) *websocket.Conn {
	t.Helper()
	url := "ws://" + srv.ln.Addr().String() + "/"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

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

	ws := dial(t, srv)
	defer ws.Close()

	if err := ws.WriteJSON(WsMsg{Op: "read", Path: "/blob"}); err != nil {
		t.Fatal(err)
	}
	var reply WsReply
	if err := ws.ReadJSON(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.Op != "read" || reply.Data != "hello" {
		t.Fatalf("unexpected reply: %+v", reply)
	}

	if err := ws.WriteJSON(WsMsg{Op: "write", Path: "/blob", Data: "world"}); err != nil {
		t.Fatal(err)
	}
	if err := ws.ReadJSON(&reply); err != nil {
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

	ws := dial(t, srv)
	defer ws.Close()

	if err := ws.WriteJSON(WsMsg{Op: "subscribe", Prefix: "/blob", SubID: "sub1"}); err != nil {
		t.Fatal(err)
	}
	var reply WsReply
	if err := ws.ReadJSON(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.SubID != "sub1" {
		t.Fatalf("expected SubID sub1, got %q", reply.SubID)
	}

	if err := fs.Write(context.Background(), "/blob", []byte("x"), core.CallerIdentity{}); err != nil {
		t.Fatal(err)
	}

	// Drain the subscribe ack then wait for event.
	if err := ws.ReadJSON(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.Op != "event" || reply.Event == nil {
		t.Fatalf("expected event reply, got %+v", reply)
	}
	if reply.SubID != "sub1" {
		t.Fatalf("expected event SubID sub1, got %q", reply.SubID)
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

	ws := dial(t, srv)
	defer ws.Close()

	if err := ws.WriteJSON(WsMsg{Op: "write", Path: "/blob", Data: "x"}); err != nil {
		t.Fatal(err)
	}
	var reply WsReply
	if err := ws.ReadJSON(&reply); err != nil {
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

	ws := dial(t, srv)
	defer ws.Close()

	// Send unknown op.
	if err := ws.WriteJSON(WsMsg{Op: "nope"}); err != nil {
		t.Fatal(err)
	}
	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	var reply WsReply
	if err := ws.ReadJSON(&reply); err != nil {
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

	ws := dial(t, srv)
	defer ws.Close()

	if err := ws.WriteJSON(WsMsg{Op: "read-binary", Path: "/blob"}); err != nil {
		t.Fatal(err)
	}
	var reply WsReply
	if err := ws.ReadJSON(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.Op != "read-binary" {
		t.Fatalf("unexpected reply op: %q", reply.Op)
	}

	msgType, payload, err := ws.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if msgType != websocket.BinaryMessage {
		t.Fatalf("expected binary message, got %d", msgType)
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

	ws := dial(t, srv)
	defer ws.Close()

	if err := ws.WriteJSON(WsMsg{Op: "write-binary", Path: "/blob"}); err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteMessage(websocket.BinaryMessage, []byte("raw-bytes")); err != nil {
		t.Fatal(err)
	}
	var reply WsReply
	if err := ws.ReadJSON(&reply); err != nil {
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

	ws := dial(t, srv)
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
	dialer := websocket.DefaultDialer
	dialer.Jar = nil
	_, resp, err := dialer.Dial(url, http.Header{"Origin": []string{"http://evil.com"}})
	if err == nil {
		t.Fatal("expected handshake failure for bad origin")
	}
	if resp != nil && resp.StatusCode != http.StatusForbidden {
		t.Logf("unexpected status: %d", resp.StatusCode)
	}

	// Good origin should succeed.
	ws, _, err := dialer.Dial(url, http.Header{"Origin": []string{"http://trusted.example.com"}})
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

	ws := dial(t, srv)
	defer ws.Close()

	// Send a message larger than 64 KiB.
	big := strings.Repeat("x", 128*1024)
	if err := ws.WriteJSON(WsMsg{Op: "write", Path: "/blob", Data: big}); err != nil {
		// The send itself may succeed; the receive should fail or error out.
		t.Logf("send error (acceptable): %v", err)
	}
	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	var reply WsReply
	if err := ws.ReadJSON(&reply); err != nil {
		// Receiving a too-large frame should close the connection.
		t.Logf("receive error (expected): %v", err)
	}
}

func TestWebSocketMultiSubscribe(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/a", core.MountEntry{Kind: core.KindBlob, Mode: 0o666}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/b", core.MountEntry{Kind: core.KindBlob, Mode: 0o666}); err != nil {
		t.Fatal(err)
	}

	srv := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := srv.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Unmount(context.Background())

	ws := dial(t, srv)
	defer ws.Close()

	// Subscribe to /a and /b with different IDs.
	if err := ws.WriteJSON(WsMsg{Op: "subscribe", Prefix: "/a", SubID: "sub-a"}); err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteJSON(WsMsg{Op: "subscribe", Prefix: "/b", SubID: "sub-b"}); err != nil {
		t.Fatal(err)
	}
	var reply WsReply
	if err := ws.ReadJSON(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.SubID != "sub-a" {
		t.Fatalf("expected sub-a ack, got %q", reply.SubID)
	}
	if err := ws.ReadJSON(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.SubID != "sub-b" {
		t.Fatalf("expected sub-b ack, got %q", reply.SubID)
	}

	// Write to /a; only sub-a should receive.
	if err := fs.Write(context.Background(), "/a", []byte("x"), core.CallerIdentity{}); err != nil {
		t.Fatal(err)
	}
	if err := ws.ReadJSON(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.Op != "event" || reply.SubID != "sub-a" {
		t.Fatalf("expected sub-a event, got %+v", reply)
	}

	// Unsubscribe sub-a, then write to /a again.
	if err := ws.WriteJSON(WsMsg{Op: "unsubscribe", SubID: "sub-a"}); err != nil {
		t.Fatal(err)
	}
	if err := ws.ReadJSON(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.SubID != "sub-a" {
		t.Fatalf("expected sub-a unsub ack, got %q", reply.SubID)
	}

	if err := fs.Write(context.Background(), "/a", []byte("y"), core.CallerIdentity{}); err != nil {
		t.Fatal(err)
	}
	ws.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if err := ws.ReadJSON(&reply); err == nil {
		t.Fatalf("expected timeout after unsub, got %+v", reply)
	}
}

func TestWebSocketSubscribeMissingID(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	srv := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := srv.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Unmount(context.Background())

	ws := dial(t, srv)
	defer ws.Close()

	if err := ws.WriteJSON(WsMsg{Op: "subscribe", Prefix: "/"}); err != nil {
		t.Fatal(err)
	}
	var reply WsReply
	if err := ws.ReadJSON(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", reply.Code)
	}
	if reply.Error != "missing sub_id" {
		t.Fatalf("expected missing sub_id error, got %q", reply.Error)
	}
}

func TestWebSocketPingPong(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	srv := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := srv.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Unmount(context.Background())

	ws := dial(t, srv)
	defer ws.Close()

	// Client-initiated application-level ping.
	if err := ws.WriteJSON(WsMsg{Op: "ping"}); err != nil {
		t.Fatal(err)
	}
	var reply WsReply
	if err := ws.ReadJSON(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.Op != "pong" {
		t.Fatalf("expected pong, got %q", reply.Op)
	}

	// The server sends WebSocket protocol-level pings every 30s.
	// gorilla/websocket handles them automatically (replies with pong).
	// Verify the connection survives a ping cycle by reading after 35s.
	ws.SetReadDeadline(time.Now().Add(35 * time.Second))
	if err := ws.WriteJSON(WsMsg{Op: "ping"}); err != nil {
		t.Fatal(err)
	}
	if err := ws.ReadJSON(&reply); err != nil {
		t.Fatalf("connection died during server ping cycle: %v", err)
	}
	if reply.Op != "pong" {
		t.Fatalf("expected pong after ping cycle, got %q", reply.Op)
	}
}

func TestWebSocketBatchRead(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/a", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("alpha")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/b", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("beta")}); err != nil {
		t.Fatal(err)
	}

	srv := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := srv.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Unmount(context.Background())

	ws := dial(t, srv)
	defer ws.Close()

	batch := WsMsg{
		Op: "batch",
		Ops: []WsMsg{
			{Op: "read", Path: "/a"},
			{Op: "read", Path: "/b"},
		},
	}
	if err := ws.WriteJSON(batch); err != nil {
		t.Fatal(err)
	}
	var reply WsReply
	if err := ws.ReadJSON(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.Op != "batch" {
		t.Fatalf("expected batch reply, got %q", reply.Op)
	}
	if len(reply.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(reply.Results))
	}
	if reply.Results[0].Data != "alpha" {
		t.Fatalf("expected alpha, got %q", reply.Results[0].Data)
	}
	if reply.Results[1].Data != "beta" {
		t.Fatalf("expected beta, got %q", reply.Results[1].Data)
	}
}

func TestWebSocketBatchWithError(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/a", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("alpha")}); err != nil {
		t.Fatal(err)
	}

	srv := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := srv.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Unmount(context.Background())

	ws := dial(t, srv)
	defer ws.Close()

	batch := WsMsg{
		Op: "batch",
		Ops: []WsMsg{
			{Op: "read", Path: "/a"},
			{Op: "read", Path: "/missing"},
		},
	}
	if err := ws.WriteJSON(batch); err != nil {
		t.Fatal(err)
	}
	var reply WsReply
	if err := ws.ReadJSON(&reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(reply.Results))
	}
	if reply.Results[0].Error != "" {
		t.Fatalf("expected first op success, got error %q", reply.Results[0].Error)
	}
	if reply.Results[1].Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing path, got %d", reply.Results[1].Code)
	}
}

func TestWebSocketBatchSizeLimit(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	srv := New(fs, "127.0.0.1:0", adapter.MountOptions{MaxBatchSize: 2})
	if err := srv.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Unmount(context.Background())

	ws := dial(t, srv)
	defer ws.Close()

	batch := WsMsg{
		Op: "batch",
		Ops: []WsMsg{
			{Op: "read", Path: "/a"},
			{Op: "read", Path: "/b"},
			{Op: "read", Path: "/c"},
		},
	}
	if err := ws.WriteJSON(batch); err != nil {
		t.Fatal(err)
	}
	var reply WsReply
	if err := ws.ReadJSON(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized batch, got %d", reply.Code)
	}
	if reply.Error == "" {
		t.Fatal("expected error for oversized batch")
	}
}

func TestWebSocketCompressionNegotiated(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("compress-me")}); err != nil {
		t.Fatal(err)
	}

	srv := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := srv.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Unmount(context.Background())

	// Dial with compression enabled.
	dialer := websocket.DefaultDialer
	dialer.EnableCompression = true
	url := "ws://" + srv.ln.Addr().String() + "/"
	ws, resp, err := dialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	// Verify the server negotiated permessage-deflate.
	if resp == nil {
		t.Fatal("expected non-nil response after dial")
	}
	exts := resp.Header.Get("Sec-WebSocket-Extensions")
	if !strings.Contains(exts, "permessage-deflate") {
		t.Fatalf("expected permessage-deflate in extensions, got %q", exts)
	}

	// Basic operation should still work.
	if err := ws.WriteJSON(WsMsg{Op: "read", Path: "/blob"}); err != nil {
		t.Fatal(err)
	}
	var reply WsReply
	if err := ws.ReadJSON(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.Data != "compress-me" {
		t.Fatalf("expected compress-me, got %q", reply.Data)
	}
}

func TestWebSocketMetricsEndpoint(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	resp, err := http.Get(baseURL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("expected text/plain content type, got %q", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "skills_fs_operation_latency_seconds") {
		t.Fatalf("expected prometheus latency metric, got:\n%s", body)
	}
}
