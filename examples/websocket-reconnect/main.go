// websocket-reconnect demonstrates a resilient WebSocket client with
// automatic reconnection, exponential backoff, and subscription recovery.
package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/skills-fs/skills-fs/adapter"
	wsadapter "github.com/skills-fs/skills-fs/adapter/websocket"
	"github.com/skills-fs/skills-fs/core"
	"golang.org/x/net/websocket"
)

func main() {
	// Start a local server for the demo.
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
	fmt.Println("WebSocket server listening on", addr)

	// Run the resilient client.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	client := &reconnectClient{
		addr:     addr,
		subID:    "demo-sub",
		prefix:   "/counter",
		maxDelay: 30 * time.Second,
	}
	go client.run(ctx)

	// Simulate server restarts to trigger reconnects.
	go simulateServerRestarts(ctx, server, fs)

	<-ctx.Done()
	fmt.Println("\nShutting down...")
	server.Unmount(context.Background())
	fs.Shutdown(context.Background())
}

// reconnectClient maintains a WebSocket connection with auto-reconnect.
type reconnectClient struct {
	addr     string
	subID    string
	prefix   string
	maxDelay time.Duration

	mu     sync.Mutex
	ws     *websocket.Conn
	closed bool
}

func (c *reconnectClient) run(ctx context.Context) {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := c.connect(); err != nil {
			log.Printf("connect failed: %v, retrying in %v", err, backoff)
			select {
			case <-time.After(backoff):
				backoff = min(backoff*2, c.maxDelay)
				continue
			case <-ctx.Done():
				return
			}
		}

		log.Println("connected")
		backoff = time.Second // reset on successful connection

		if err := c.subscribe(); err != nil {
			log.Printf("subscribe failed: %v", err)
			c.closeConn()
			continue
		}

		// Block until the connection drops.
		c.readLoop(ctx)
		c.closeConn()
		log.Println("connection lost, reconnecting...")
	}
}

func (c *reconnectClient) connect() error {
	origin := "http://" + c.addr
	url := "ws://" + c.addr + "/"
	ws, err := websocket.Dial(url, "", origin)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.ws = ws
	c.closed = false
	c.mu.Unlock()
	return nil
}

func (c *reconnectClient) subscribe() error {
	msg := wsadapter.WsMsg{Op: "subscribe", Prefix: c.prefix, SubID: c.subID}
	if err := websocket.JSON.Send(c.ws, msg); err != nil {
		return err
	}
	var ack wsadapter.WsReply
	if err := websocket.JSON.Receive(c.ws, &ack); err != nil {
		return err
	}
	log.Println("subscribed, ack:", ack)
	return nil
}

func (c *reconnectClient) readLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		c.mu.Lock()
		ws := c.ws
		c.mu.Unlock()
		if ws == nil {
			return
		}
		ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		var msg wsadapter.WsReply
		if err := websocket.JSON.Receive(ws, &msg); err != nil {
			// Timeout is expected from server heartbeats; pong resets it.
			if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
				continue
			}
			log.Printf("receive error: %v", err)
			return
		}
		switch msg.Op {
		case "event":
			if msg.Event != nil {
				fmt.Printf("Event: %s %s\n", msg.Event.Kind, msg.Event.Path)
			}
		case "ping":
			_ = websocket.JSON.Send(ws, wsadapter.WsReply{Op: "pong"})
		}
	}
}

func (c *reconnectClient) closeConn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ws != nil && !c.closed {
		c.closed = true
		c.ws.Close()
	}
	c.ws = nil
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// simulateServerRestarts unmounts and remounts the server to force client reconnects.
func simulateServerRestarts(ctx context.Context, server *wsadapter.Server, fs *core.FileSystem) {
	ticker := time.NewTicker(8 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fmt.Println("[simulator] restarting server...")
			server.Unmount(context.Background())
			// Small jitter to exercise backoff.
			time.Sleep(time.Duration(rand.Intn(500)+200) * time.Millisecond)
			if err := server.Mount(context.Background()); err != nil {
				log.Println("remount err:", err)
				continue
			}
			fmt.Println("[simulator] server restarted on", server.Addr())
		}
	}
}
