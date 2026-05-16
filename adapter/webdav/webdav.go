package webdav

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/skills-fs/skills-fs/adapter"
	"github.com/skills-fs/skills-fs/core"
)

type Server struct {
	fs   *core.FileSystem
	addr string
	opts adapter.MountOptions
	srv  *http.Server
	ln   net.Listener
}

func New(fs *core.FileSystem, addr string, opts adapter.MountOptions) *Server {
	return &Server{fs: fs, addr: addr, opts: opts}
}

func (s *Server) MountPoint() string {
	return s.addr
}

func (s *Server) FileSystem() *core.FileSystem {
	return s.fs
}

func (s *Server) Options() adapter.MountOptions {
	return s.opts
}

func (s *Server) Mount(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleWebDAV)

	s.srv = &http.Server{
		Addr:    s.addr,
		Handler: mux,
	}

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.ln = ln

	go s.srv.Serve(ln)

	// Wait briefly to ensure the server is listening.
	for i := 0; i < 50; i++ {
		conn, err := net.Dial("tcp", s.ln.Addr().String())
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("webdav server did not start")
}

func (s *Server) Unmount(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}

func (s *Server) handleWebDAV(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		switch r.Method {
		case http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodPost:
			http.Error(w, "read-only filesystem", http.StatusForbidden)
			return
		}
	}

	caller := s.callerFromRequest(r)
	path := r.URL.Path
	if path == "" {
		path = "/"
	}

	switch r.Method {
	case http.MethodGet:
		s.handleGet(w, r, path, caller)
	case http.MethodHead:
		s.handleHead(w, r, path, caller)
	case http.MethodPut:
		s.handlePut(w, r, path, caller)
	case http.MethodDelete:
		s.handleDelete(w, r, path, caller)
	case "PROPFIND":
		s.handlePropfind(w, r, path, caller)
	case http.MethodOptions:
		s.handleOptions(w, r)
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT, DELETE, PROPFIND, OPTIONS")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request, path string, caller core.CallerIdentity) {
	data, err := s.fs.Read(r.Context(), path, caller)
	if err != nil {
		s.writeError(w, err)
		return
	}
	stat, err := s.fs.Stat(path, caller)
	if err == nil {
		w.Header().Set("Content-Type", contentTypeFromKind(stat.Kind))
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.Write(data)
}

func (s *Server) handleHead(w http.ResponseWriter, r *http.Request, path string, caller core.CallerIdentity) {
	stat, err := s.fs.Stat(path, caller)
	if err != nil {
		s.writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", contentTypeFromKind(stat.Kind))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", stat.Size))
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handlePut(w http.ResponseWriter, r *http.Request, path string, caller core.CallerIdentity) {
	if s.opts.ReadOnly {
		http.Error(w, "read-only filesystem", http.StatusForbidden)
		return
	}
	// Limit body size to a reasonable default.
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024*1024)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err := s.fs.Write(r.Context(), path, data, caller); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "GET, HEAD, PUT, DELETE, PROPFIND, OPTIONS")
	w.Header().Set("DAV", "1")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request, path string, caller core.CallerIdentity) {
	if s.opts.ReadOnly {
		http.Error(w, "read-only filesystem", http.StatusForbidden)
		return
	}
	if err := s.fs.Unmount(path); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePropfind(w http.ResponseWriter, r *http.Request, p string, caller core.CallerIdentity) {
	depth := r.Header.Get("Depth")
	if depth == "" {
		depth = "infinity"
	}

	entries, err := s.buildPropfindEntries(p, depth, caller)
	if err != nil {
		s.writeError(w, err)
		return
	}

	ms := multistatus{XmlnsD: "DAV:", Responses: entries}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	xml.NewEncoder(w).Encode(ms)
}

func (s *Server) buildPropfindEntries(p string, depth string, caller core.CallerIdentity) ([]response, error) {
	stat, err := s.fs.Stat(p, caller)
	if err != nil {
		return nil, err
	}

	var entries []response
	entries = append(entries, s.propfindResponse(p, stat))

	if depth != "0" && stat.Kind == core.KindDir {
		children, err := s.fs.Readdir(p, caller)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			childPath := path.Join(p, child.Name)
			if childPath == "" {
				childPath = "/" + child.Name
			}
			childStat, err := s.fs.Stat(childPath, caller)
			if err != nil {
				continue
			}
			entries = append(entries, s.propfindResponse(childPath, childStat))
		}
	}

	return entries, nil
}

func (s *Server) propfindResponse(p string, stat core.Stat) response {
	var rt *resourceType
	if stat.Kind == core.KindDir {
		rt = &resourceType{Collection: ""}
	}
	return response{
		Href: p,
		Propstat: propstat{
			Prop: prop{
				DisplayName:      path.Base(p),
				GetContentLength: stat.Size,
				ResourceType:     rt,
			},
			Status: "HTTP/1.1 200 OK",
		},
	}
}

// WebDAV XML structures.
type multistatus struct {
	XMLName   xml.Name   `xml:"D:multistatus"`
	XmlnsD    string     `xml:"xmlns:D,attr"`
	Responses []response `xml:"D:response"`
}

type response struct {
	XMLName  xml.Name `xml:"D:response"`
	Href     string   `xml:"D:href"`
	Propstat propstat `xml:"D:propstat"`
}

type propstat struct {
	XMLName xml.Name `xml:"D:propstat"`
	Prop    prop     `xml:"D:prop"`
	Status  string   `xml:"D:status"`
}

type prop struct {
	XMLName          xml.Name      `xml:"D:prop"`
	DisplayName      string        `xml:"D:displayname"`
	GetContentLength int64         `xml:"D:getcontentlength"`
	ResourceType     *resourceType `xml:"D:resourcetype"`
}

type resourceType struct {
	XMLName    xml.Name `xml:"D:resourcetype"`
	Collection string   `xml:"D:collection,omitempty"`
}

func (s *Server) callerFromRequest(r *http.Request) core.CallerIdentity {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Basic ") {
		return core.CallerIdentity{}
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, "Basic "))
	if err != nil {
		return core.CallerIdentity{}
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return core.CallerIdentity{}
	}
	// For now map the username to a numeric UID if possible,
	// otherwise default to 0.
	var uid uint32
	fmt.Sscanf(parts[0], "%d", &uid)
	return core.CallerIdentity{UID: uid, GID: uid}
}

func (s *Server) writeError(w http.ResponseWriter, err error) {
	var pe *core.PosixError
	if errors.As(err, &pe) {
		switch pe.Code {
		case core.ENOENT:
			http.Error(w, "not found", http.StatusNotFound)
		case core.EACCES:
			http.Error(w, "forbidden", http.StatusForbidden)
		case core.EEXIST:
			http.Error(w, "conflict", http.StatusConflict)
		case core.EINVAL:
			http.Error(w, "bad request", http.StatusBadRequest)
		default:
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func contentTypeFromKind(kind core.NodeKind) string {
	switch kind {
	case core.KindDir:
		return "httpd/unix-directory"
	case core.KindAPI:
		return "application/json"
	case core.KindStream:
		return "application/octet-stream"
	default:
		return "application/octet-stream"
	}
}
