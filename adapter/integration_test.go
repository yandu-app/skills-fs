package adapter_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
	"github.com/skills-fs/skills-fs/adapter"
	"github.com/skills-fs/skills-fs/adapter/webdav"
	wsadapter "github.com/skills-fs/skills-fs/adapter/websocket"
	"github.com/skills-fs/skills-fs/core"
)

func TestWebDAVAndWebSocketIntegration(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o666, BlobData: []byte("initial")}); err != nil {
		t.Fatal(err)
	}

	webdavSrv := webdav.New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := webdavSrv.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer webdavSrv.Unmount(context.Background())

	wsSrv := wsadapter.New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := wsSrv.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer wsSrv.Unmount(context.Background())

	// Use actual bound addresses.
	webdavAddr := webdavSrv.Addr()
	wsAddr := wsSrv.Addr()

	// Write via WebDAV, read back via WebSocket.
	baseURL := "http://" + webdavAddr
	req, _ := http.NewRequest(http.MethodPut, baseURL+"/blob", strings.NewReader("cross-adapter"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Read via WebSocket.
	wsURL := "ws://" + wsAddr + "/"
	wsOrigin := "http://" + wsAddr
	ws, err := websocket.Dial(wsURL, "", wsOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	if err := websocket.JSON.Send(ws, wsadapter.WsMsg{Op: "read", Path: "/blob"}); err != nil {
		t.Fatal(err)
	}
	var reply wsadapter.WsReply
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Data != "cross-adapter" {
		t.Fatalf("expected 'cross-adapter', got %q", reply.Data)
	}

	// Subscribe to events via WebSocket.
	if err := websocket.JSON.Send(ws, wsadapter.WsMsg{Op: "subscribe", Prefix: "/blob", SubID: "sub1"}); err != nil {
		t.Fatal(err)
	}
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		t.Fatal(err)
	}

	// Trigger event via WebDAV PUT.
	req, _ = http.NewRequest(http.MethodPut, baseURL+"/blob", strings.NewReader("event-triggered"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	ws.SetDeadline(time.Now().Add(2 * time.Second))
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Op != "event" || reply.Event == nil {
		t.Fatalf("expected event, got %+v", reply)
	}
}
