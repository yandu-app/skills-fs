package websocket

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
	"github.com/skills-fs/skills-fs/adapter"
	"github.com/skills-fs/skills-fs/adapter/middleware"
	"github.com/skills-fs/skills-fs/core"
)

// Server streams filesystem operations over WebSocket.
type Server struct {
	fs    *core.FileSystem
	addr  string
	opts  adapter.MountOptions
	srv   *http.Server
	ln    net.Listener
	conns atomic.Int32
}

func New(fs *core.FileSystem, addr string, opts adapter.MountOptions) *Server {
	return &Server{fs: fs, addr: addr, opts: opts}
}

func (s *Server) MountPoint() string { return s.addr }
func (s *Server) Addr() string {
	if s.ln != nil {
		return s.ln.Addr().String()
	}
	return s.addr
}
func (s *Server) FileSystem() *core.FileSystem { return s.fs }
func (s *Server) Options() adapter.MountOptions { return s.opts }
func (s *Server) ActiveConnections() int32     { return s.conns.Load() }

func (s *Server) Mount(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok\n"))
	})
	wsSrv := websocket.Server{
		Handshake: s.checkOrigin,
		Handler:   s.handleWS,
	}
	handler := middleware.CORS(s.opts.CORSOrigins)(wsSrv)
	if cl := middleware.NewConnLimiter(s.opts.MaxConnections); cl != nil {
		handler = middleware.ConnLimit(cl)(handler)
	}
	mux.Handle("/", handler)

	s.srv = &http.Server{Addr: s.addr, Handler: mux}
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.ln = ln
	go s.srv.Serve(ln)
	return nil
}

func (s *Server) checkOrigin(cfg *websocket.Config, req *http.Request) error {
	if len(s.opts.AllowedOrigins) == 0 {
		return nil
	}
	origin := req.Header.Get("Origin")
	if origin == "" {
		origin = req.Header.Get("Sec-WebSocket-Origin")
	}
	for _, allowed := range s.opts.AllowedOrigins {
		if strings.EqualFold(origin, allowed) {
			return nil
		}
	}
	return fmt.Errorf("origin %q not allowed", origin)
}

func (s *Server) Unmount(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	ctx, cancel := s.opts.ShutdownContext(ctx)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

type WsMsg struct {
	Op     string `json:"op"`
	Path   string `json:"path"`
	Data   string `json:"data,omitempty"`
	Prefix string `json:"prefix,omitempty"`
}

type WsReply struct {
	Op        string `json:"op"`
	Path      string `json:"path,omitempty"`
	Data      string `json:"data,omitempty"`
	Error     string `json:"error,omitempty"`
	Code      int    `json:"code,omitempty"`
	Event     *Evt   `json:"event,omitempty"`
}

type Evt struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

func errorCode(err error) int {
	var pe *core.PosixError
	if errors.As(err, &pe) {
		switch pe.Code {
		case core.ENOENT:
			return http.StatusNotFound
		case core.EACCES:
			return http.StatusForbidden
		case core.EEXIST:
			return http.StatusConflict
		case core.EINVAL:
			return http.StatusBadRequest
		case core.ETIMEDOUT:
			return http.StatusRequestTimeout
		}
	}
	return http.StatusInternalServerError
}

func (s *Server) handleWS(conn *websocket.Conn) {
	defer conn.Close()
	s.conns.Add(1)
	defer s.conns.Add(-1)
	conn.MaxPayloadBytes = 64 * 1024 // 64 KiB max message size
	var unsub func()
	defer func() {
		if unsub != nil {
			unsub()
		}
	}()

	for {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		var msg WsMsg
		if err := websocket.JSON.Receive(conn, &msg); err != nil {
			return
		}
		reply := WsReply{Op: msg.Op, Path: msg.Path}
		switch msg.Op {
		case "read":
			data, err := s.fs.Read(context.Background(), msg.Path, core.CallerIdentity{})
			if err != nil {
				reply.Error = err.Error()
				reply.Code = errorCode(err)
			} else {
				reply.Data = string(data)
			}
		case "write":
			if s.opts.ReadOnly {
				reply.Error = "read-only filesystem"
				reply.Code = http.StatusForbidden
				break
			}
			if err := s.fs.Write(context.Background(), msg.Path, []byte(msg.Data), core.CallerIdentity{}); err != nil {
				reply.Error = err.Error()
				reply.Code = errorCode(err)
			}
		case "read-binary":
			data, err := s.fs.Read(context.Background(), msg.Path, core.CallerIdentity{})
			if err != nil {
				reply.Error = err.Error()
				reply.Code = errorCode(err)
			} else {
				// Send JSON ack, then binary payload.
				if err := websocket.JSON.Send(conn, reply); err != nil {
					return
				}
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := websocket.Message.Send(conn, data); err != nil {
					return
				}
				continue // skip default JSON reply
			}
		case "write-binary":
			if s.opts.ReadOnly {
				reply.Error = "read-only filesystem"
				reply.Code = http.StatusForbidden
				break
			}
			conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			var payload []byte
			if err := websocket.Message.Receive(conn, &payload); err != nil {
				return
			}
			if err := s.fs.Write(context.Background(), msg.Path, payload, core.CallerIdentity{}); err != nil {
				reply.Error = err.Error()
				reply.Code = errorCode(err)
			}
		case "subscribe":
			if unsub != nil {
				unsub()
			}
			ch := make(chan core.Event, 16)
			unsub = s.fs.RegisterNotifier(func(e core.Event) {
				select {
				case ch <- e:
				default:
				}
			}, msg.Prefix)
			go func() {
				for e := range ch {
					websocket.JSON.Send(conn, WsReply{
						Op: "event",
						Event: &Evt{Path: e.Path, Kind: fmt.Sprint(e.Kind)},
					})
				}
			}()
		case "unsubscribe":
			if unsub != nil {
				unsub()
				unsub = nil
			}
		default:
			reply.Error = "unknown op"
			reply.Code = http.StatusBadRequest
		}
		if err := websocket.JSON.Send(conn, reply); err != nil {
			return
		}
	}
}
