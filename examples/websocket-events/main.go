// websocket-events demonstrates subscribing to filesystem events over WebSocket.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/skills-fs/skills-fs/adapter"
	wsadapter "github.com/skills-fs/skills-fs/adapter/websocket"
	"github.com/skills-fs/skills-fs/core"
	"golang.org/x/net/websocket"
)

func main() {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/counter", core.MountEntry{
		Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("0"),
	}); err != nil {
		log.Fatal(err)
	}

	server := wsadapter.New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		log.Fatal(err)
	}
	addr := server.Addr()
	fmt.Println("WebSocket listening on", addr)

	// Connect from the client side.
	origin := "http://" + addr
	url := "ws://" + addr + "/"
	ws, err := websocket.Dial(url, "", origin)
	if err != nil {
		log.Fatal(err)
	}
	defer ws.Close()

	// Subscribe to all events under /counter.
	if err := websocket.JSON.Send(ws, wsadapter.WsMsg{Op: "subscribe", Prefix: "/counter"}); err != nil {
		log.Fatal(err)
	}
	var ack wsadapter.WsReply
	if err := websocket.JSON.Receive(ws, &ack); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Subscribed, ack:", ack)

	// Read current value.
	if err := websocket.JSON.Send(ws, wsadapter.WsMsg{Op: "read", Path: "/counter"}); err != nil {
		log.Fatal(err)
	}
	var reply wsadapter.WsReply
	if err := websocket.JSON.Receive(ws, &reply); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Current value:", reply.Data)

	// Start a goroutine that listens for events.
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			ws.SetReadDeadline(time.Now().Add(2 * time.Second))
			var evt wsadapter.WsReply
			if err := websocket.JSON.Receive(ws, &evt); err != nil {
				continue
			}
			if evt.Op == "event" && evt.Event != nil {
				fmt.Printf("Event: %s %s\n", evt.Event.Kind, evt.Event.Path)
			}
		}
	}()

	// Write to the blob from the server side to trigger events.
	ctx := context.Background()
	caller := core.CallerIdentity{}
	for i := 1; i <= 3; i++ {
		time.Sleep(500 * time.Millisecond)
		val := fmt.Sprintf("%d", i)
		if err := fs.Write(ctx, "/counter", []byte(val), caller); err != nil {
			log.Println("write err:", err)
		}
	}

	time.Sleep(500 * time.Millisecond)
	close(done)

	fmt.Println("\nShutting down...")
	server.Unmount(context.Background())
	fs.Shutdown(context.Background())
}
