package adapter_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/skills-fs/skills-fs/adapter"
	"github.com/skills-fs/skills-fs/adapter/webdav"
	wsadapter "github.com/skills-fs/skills-fs/adapter/websocket"
	"github.com/skills-fs/skills-fs/core"
	"golang.org/x/net/websocket"
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

func TestWebSocketWriteWebDAVRead(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o666, BlobData: []byte("initial")}); err != nil {
		t.Fatal(err)
	}

	wsSrv := wsadapter.New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := wsSrv.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer wsSrv.Unmount(context.Background())

	webdavSrv := webdav.New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := webdavSrv.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer webdavSrv.Unmount(context.Background())

	wsURL := "ws://" + wsSrv.Addr() + "/"
	wsOrigin := "http://" + wsSrv.Addr()
	ws, err := websocket.Dial(wsURL, "", wsOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	// Write via WebSocket.
	if err := websocket.JSON.Send(ws, wsadapter.WsMsg{Op: "write", Path: "/blob", Data: "ws-written"}); err != nil {
		t.Fatal(err)
	}
	var reply wsadapter.WsReply
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Error != "" {
		t.Fatalf("unexpected write error: %s", reply.Error)
	}

	// Read via WebDAV.
	resp, err := http.Get("http://" + webdavSrv.Addr() + "/blob")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := make([]byte, 64)
	n, _ := resp.Body.Read(body)
	if string(body[:n]) != "ws-written" {
		t.Fatalf("expected ws-written, got %q", string(body[:n]))
	}
}

func TestMultiSubscribeEvents(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/a", core.MountEntry{Kind: core.KindBlob, Mode: 0o666, BlobData: []byte("a")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/b", core.MountEntry{Kind: core.KindBlob, Mode: 0o666, BlobData: []byte("b")}); err != nil {
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

	wsURL := "ws://" + wsSrv.Addr() + "/"
	wsOrigin := "http://" + wsSrv.Addr()
	ws, err := websocket.Dial(wsURL, "", wsOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	// Subscribe to /a and /b with separate IDs.
	if err := websocket.JSON.Send(ws, wsadapter.WsMsg{Op: "subscribe", Prefix: "/a", SubID: "sub-a"}); err != nil {
		t.Fatal(err)
	}
	if err := websocket.JSON.Send(ws, wsadapter.WsMsg{Op: "subscribe", Prefix: "/b", SubID: "sub-b"}); err != nil {
		t.Fatal(err)
	}
	var reply wsadapter.WsReply
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		t.Fatal(err)
	}
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		t.Fatal(err)
	}

	baseURL := "http://" + webdavSrv.Addr()

	// Write to /a via WebDAV.
	req, _ := http.NewRequest(http.MethodPut, baseURL+"/a", strings.NewReader("updated-a"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	ws.SetDeadline(time.Now().Add(2 * time.Second))
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Op != "event" || reply.SubID != "sub-a" {
		t.Fatalf("expected sub-a event, got %+v", reply)
	}

	// Write to /b via WebDAV.
	req, _ = http.NewRequest(http.MethodPut, baseURL+"/b", strings.NewReader("updated-b"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Op != "event" || reply.SubID != "sub-b" {
		t.Fatalf("expected sub-b event, got %+v", reply)
	}
}

func TestWebDAVDeleteWebSocketEvent(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o666, BlobData: []byte("x")}); err != nil {
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

	wsURL := "ws://" + wsSrv.Addr() + "/"
	wsOrigin := "http://" + wsSrv.Addr()
	ws, err := websocket.Dial(wsURL, "", wsOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	if err := websocket.JSON.Send(ws, wsadapter.WsMsg{Op: "subscribe", Prefix: "/blob", SubID: "sub1"}); err != nil {
		t.Fatal(err)
	}
	var reply wsadapter.WsReply
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		t.Fatal(err)
	}

	// Delete via WebDAV.
	req, _ := http.NewRequest(http.MethodDelete, "http://"+webdavSrv.Addr()+"/blob", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	ws.SetDeadline(time.Now().Add(2 * time.Second))
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Op != "event" || reply.Event == nil {
		t.Fatalf("expected event, got %+v", reply)
	}
}
