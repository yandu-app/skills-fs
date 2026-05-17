package webdav

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/skills-fs/skills-fs/adapter"
	"github.com/skills-fs/skills-fs/adapter/middleware"
	"github.com/skills-fs/skills-fs/core"
)

type Server struct {
	fs       *core.FileSystem
	addr     string
	opts     adapter.MountOptions
	srv      *http.Server
	ln       net.Listener
	propCache *propCache
}

type propCacheEntry struct {
	stat    core.Stat
	etag    string
	expires time.Time
}

type propCache struct {
	mu      sync.RWMutex
	entries map[string]propCacheEntry
	ttl     time.Duration
}

func newPropCache(ttl time.Duration) *propCache {
	if ttl <= 0 {
		return nil
	}
	return &propCache{entries: make(map[string]propCacheEntry), ttl: ttl}
}

func (c *propCache) get(path string) (core.Stat, string, bool) {
	if c == nil {
		return core.Stat{}, "", false
	}
	c.mu.RLock()
	ent, ok := c.entries[path]
	c.mu.RUnlock()
	if !ok || time.Now().After(ent.expires) {
		return core.Stat{}, "", false
	}
	return ent.stat, ent.etag, true
}

func (c *propCache) set(path string, stat core.Stat, etag string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries[path] = propCacheEntry{stat: stat, etag: etag, expires: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}

func (c *propCache) invalidate(path string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.entries, path)
	// Also invalidate any entries under this path (descendants).
	for p := range c.entries {
		if p == path || strings.HasPrefix(p, path+"/") {
			delete(c.entries, p)
		}
	}
	c.mu.Unlock()
}

func New(fs *core.FileSystem, addr string, opts adapter.MountOptions) *Server {
	return &Server{fs: fs, addr: addr, opts: opts, propCache: newPropCache(opts.PropfindCacheTTL)}
}

func (s *Server) MountPoint() string {
	return s.addr
}

func (s *Server) Addr() string {
	if s.ln != nil {
		return s.ln.Addr().String()
	}
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
	mux.HandleFunc("/healthz", s.handleHealthz)
	if s.opts.Debug {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
	}
	mux.HandleFunc("/", s.handleWebDAV)

	handler := middleware.RequestID(mux)
	handler = middleware.AccessLog(slog.Default())(handler)
	handler = middleware.CORS(s.opts.CORSOrigins)(handler)
	if cl := middleware.NewConnLimiter(s.opts.MaxConnections); cl != nil {
		handler = middleware.ConnLimit(cl)(handler)
	}
	if s.opts.RateLimitRPS > 0 {
		burst := s.opts.RateLimitBurst
		if burst <= 0 {
			burst = int(s.opts.RateLimitRPS)
		}
		rl := middleware.NewRateLimiter(s.opts.RateLimitRPS, burst)
		handler = middleware.RateLimit(rl)(handler)
	}

	s.srv = &http.Server{
		Addr:    s.addr,
		Handler: handler,
	}

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	if s.opts.TLSCertFile != "" && s.opts.TLSKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(s.opts.TLSCertFile, s.opts.TLSKeyFile)
		if err != nil {
			ln.Close()
			return fmt.Errorf("webdav tls load: %w", err)
		}
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}})
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
	ctx, cancel := s.opts.ShutdownContext(ctx)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

func (s *Server) handleWebDAV(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		switch r.Method {
		case http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodPost, "MKCOL", "COPY", "MOVE", "LOCK", "UNLOCK", "PROPPATCH":
			http.Error(w, "read-only filesystem", http.StatusForbidden)
			return
		}
	}

	caller := s.callerFromRequest(r)
	path := sanitizePath(r.URL.Path)
	if path == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Wrap writer with gzip for compressible methods when enabled.
	if s.opts.EnableGzip && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		switch r.Method {
		case http.MethodGet, "PROPFIND", "SEARCH":
			gzw := &gzipResponseWriter{ResponseWriter: w, Writer: gzip.NewWriter(w)}
			gzw.Header().Set("Content-Encoding", "gzip")
			gzw.Header().Del("Content-Length")
			defer gzw.Close()
			w = gzw
		}
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
	case "MKCOL":
		s.handleMkcol(w, r, path, caller)
	case "COPY":
		s.handleCopy(w, r, path, caller)
	case "MOVE":
		s.handleMove(w, r, path, caller)
	case "PROPFIND":
		s.handlePropfind(w, r, path, caller)
	case "PROPPATCH":
		s.handleProppatch(w, r, path, caller)
	case "LOCK":
		s.handleLock(w, r, path, caller)
	case "UNLOCK":
		s.handleUnlock(w, r, path, caller)
	case "SEARCH":
		s.handleSearch(w, r, path, caller)
	case http.MethodOptions:
		s.handleOptions(w, r)
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT, DELETE, MKCOL, COPY, MOVE, PROPFIND, PROPPATCH, OPTIONS, LOCK, UNLOCK, SEARCH")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request, path string, caller core.CallerIdentity) {
	data, err := s.fs.Read(r.Context(), path, caller)
	if err != nil {
		s.writeError(w, err)
		return
	}
	if s.opts.MaxResponseSize > 0 && int64(len(data)) > s.opts.MaxResponseSize {
		http.Error(w, "response too large", http.StatusInternalServerError)
		return
	}
	tag := etag(data)
	w.Header().Set("ETag", tag)
	if ifNoneMatch := r.Header.Get("If-None-Match"); ifNoneMatch != "" {
		if matchETag(ifNoneMatch, tag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	stat, err := s.fs.Stat(path, caller)
	if err == nil {
		w.Header().Set("Content-Type", contentTypeFromKind(stat.Kind))
	}

	if rng := r.Header.Get("Range"); rng != "" {
		start, end, ok := parseRange(rng, int64(len(data)))
		if !ok {
			http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(data[start : end+1])
		return
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
	if stat.Kind == core.KindBlob || stat.Kind == core.KindLink {
		data, err := s.fs.Read(r.Context(), path, caller)
		if err == nil {
			w.Header().Set("ETag", etag(data))
		}
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
	if ifMatch := r.Header.Get("If-Match"); ifMatch != "" {
		cur, err := s.fs.Read(r.Context(), path, caller)
		if err != nil {
			w.Header().Set("ETag", "")
			http.Error(w, "precondition failed", http.StatusPreconditionFailed)
			return
		}
		if !matchETag(ifMatch, etag(cur)) {
			http.Error(w, "precondition failed", http.StatusPreconditionFailed)
			return
		}
	}
	limit := int64(64 * 1024 * 1024)
	if s.opts.MaxRequestSize > 0 {
		limit = s.opts.MaxRequestSize
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err := s.fs.Write(r.Context(), path, data, caller); err != nil {
		s.writeError(w, err)
		return
	}
	s.propCache.invalidate(path)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	status := map[string]interface{}{
		"status":    "ok",
		"providers": s.fs.ProviderHealth(r.Context()),
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(status)
}

func (s *Server) handleOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "GET, HEAD, PUT, DELETE, MKCOL, COPY, MOVE, PROPFIND, PROPPATCH, OPTIONS")
	w.Header().Set("DAV", "1, 2")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleLock(w http.ResponseWriter, r *http.Request, path string, caller core.CallerIdentity) {
	if s.opts.ReadOnly {
		http.Error(w, "read-only filesystem", http.StatusForbidden)
		return
	}
	// Return a fake lock token so clients that require locking can proceed.
	token := fmt.Sprintf("opaquelocktoken:%d", time.Now().UnixNano())
	lock := lockDiscovery{
		XmlnsD: "DAV:",
		ActiveLock: activeLock{
			LockType:  lockType{Write: ""},
			LockScope: lockScope{Exclusive: ""},
			Depth:     "infinity",
			Owner:     "skills-fs",
			Timeout:   "Second-3600",
			LockToken: lockToken{Href: token},
		},
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.Header().Set("Lock-Token", "<"+token+">")
	w.WriteHeader(http.StatusOK)
	xml.NewEncoder(w).Encode(lock)
}

func (s *Server) handleUnlock(w http.ResponseWriter, r *http.Request, path string, caller core.CallerIdentity) {
	if s.opts.ReadOnly {
		http.Error(w, "read-only filesystem", http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMkcol(w http.ResponseWriter, r *http.Request, path string, caller core.CallerIdentity) {
	if s.opts.ReadOnly {
		http.Error(w, "read-only filesystem", http.StatusForbidden)
		return
	}
	if err := s.fs.Mount(path, core.MountEntry{Kind: core.KindDir, Mode: 0o755, UID: caller.UID, GID: caller.GID}); err != nil {
		s.writeError(w, err)
		return
	}
	s.propCache.invalidate(path)
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleCopy(w http.ResponseWriter, r *http.Request, src string, caller core.CallerIdentity) {
	if s.opts.ReadOnly {
		http.Error(w, "read-only filesystem", http.StatusForbidden)
		return
	}
	dst, err := s.destinationPath(r)
	if err != nil {
		http.Error(w, "bad destination", http.StatusBadRequest)
		return
	}
	if !s.allowOverwrite(r, dst, caller) {
		http.Error(w, "precondition failed", http.StatusPreconditionFailed)
		return
	}
	// Remove existing destination before overwriting.
	if _, stErr := s.fs.Stat(dst, caller); stErr == nil {
		_ = s.fs.Unmount(dst)
	}
	if err := s.copyResource(src, dst, caller); err != nil {
		s.writeError(w, err)
		return
	}
	s.propCache.invalidate(src)
	s.propCache.invalidate(dst)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMove(w http.ResponseWriter, r *http.Request, src string, caller core.CallerIdentity) {
	if s.opts.ReadOnly {
		http.Error(w, "read-only filesystem", http.StatusForbidden)
		return
	}
	dst, err := s.destinationPath(r)
	if err != nil {
		http.Error(w, "bad destination", http.StatusBadRequest)
		return
	}
	if !s.allowOverwrite(r, dst, caller) {
		http.Error(w, "precondition failed", http.StatusPreconditionFailed)
		return
	}
	// Remove existing destination before overwriting.
	if _, stErr := s.fs.Stat(dst, caller); stErr == nil {
		_ = s.fs.Unmount(dst)
	}
	if err := s.copyResource(src, dst, caller); err != nil {
		s.writeError(w, err)
		return
	}
	if err := s.fs.Unmount(src); err != nil {
		s.writeError(w, err)
		return
	}
	s.propCache.invalidate(src)
	s.propCache.invalidate(dst)
	w.WriteHeader(http.StatusNoContent)
}

// allowOverwrite checks the Overwrite header (default "T").
// Returns false only when Overwrite is "F" and the destination exists.
func (s *Server) allowOverwrite(r *http.Request, dst string, caller core.CallerIdentity) bool {
	ov := r.Header.Get("Overwrite")
	if ov == "" || ov == "T" {
		return true
	}
	if ov != "F" {
		return true // unknown value defaults to allow
	}
	_, err := s.fs.Stat(dst, caller)
	return err != nil // allow if destination does not exist
}

func (s *Server) destinationPath(r *http.Request) (string, error) {
	dst := r.Header.Get("Destination")
	if dst == "" {
		return "", fmt.Errorf("missing Destination header")
	}
	u, err := url.Parse(dst)
	if err != nil {
		return "", err
	}
	p := u.Path
	if p == "" {
		p = "/"
	}
	return p, nil
}

func (s *Server) copyResource(src, dst string, caller core.CallerIdentity) error {
	stat, err := s.fs.Stat(src, caller)
	if err != nil {
		return err
	}
	switch stat.Kind {
	case core.KindBlob:
		data, err := s.fs.Read(context.Background(), src, caller)
		if err != nil {
			return err
		}
		return s.fs.Mount(dst, core.MountEntry{Kind: core.KindBlob, Mode: stat.Mode, UID: stat.UID, GID: stat.GID, BlobData: data})
	case core.KindDir:
		return s.fs.Mount(dst, core.MountEntry{Kind: core.KindDir, Mode: stat.Mode, UID: stat.UID, GID: stat.GID})
	case core.KindLink:
		data, err := s.fs.Read(context.Background(), src, caller)
		if err != nil {
			return err
		}
		return s.fs.Mount(dst, core.MountEntry{Kind: core.KindLink, Mode: stat.Mode, UID: stat.UID, GID: stat.GID, LinkPath: string(data)})
	default:
		return &core.PosixError{Code: core.EINVAL, Op: core.OpCode("copy")}
	}
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
	s.propCache.invalidate(path)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePropfind(w http.ResponseWriter, r *http.Request, p string, caller core.CallerIdentity) {
	depth := r.Header.Get("Depth")
	if depth == "" {
		depth = "infinity"
	}

	entries, err := s.buildPropfindEntries(r.Context(), p, depth, caller)
	if err != nil {
		s.writeError(w, err)
		return
	}

	ms := multistatus{XmlnsD: "DAV:", Responses: entries}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	xml.NewEncoder(w).Encode(ms)
}

func (s *Server) handleProppatch(w http.ResponseWriter, r *http.Request, p string, caller core.CallerIdentity) {
	if s.opts.ReadOnly {
		http.Error(w, "read-only filesystem", http.StatusForbidden)
		return
	}
	// Stub: accept all property updates without parsing the request body.
	resp := response{
		Href: p,
		Propstat: propstat{
			Prop:   prop{},
			Status: "HTTP/1.1 200 OK",
		},
	}
	ms := multistatus{XmlnsD: "DAV:", Responses: []response{resp}}
	s.propCache.invalidate(p)
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	xml.NewEncoder(w).Encode(ms)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request, p string, caller core.CallerIdentity) {
	var req searchRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid search body", http.StatusBadRequest)
		return
	}
	pattern := req.BasicSearch.Where.Like.Literal
	// DASL LIKE uses % as wildcard; convert to Go match pattern.
	matchPattern := strings.ReplaceAll(pattern, "%", "*")
	matchPattern = strings.ReplaceAll(matchPattern, "_", "?")

	entries, err := s.searchEntries(r.Context(), matchPattern, caller)
	if err != nil {
		s.writeError(w, err)
		return
	}

	ms := multistatus{XmlnsD: "DAV:", Responses: entries}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	xml.NewEncoder(w).Encode(ms)
}

func (s *Server) searchEntries(ctx context.Context, pattern string, caller core.CallerIdentity) ([]response, error) {
	var results []response
	for _, e := range s.fs.Snapshot() {
		name := path.Base(e.Path)
		matched, _ := path.Match(pattern, name)
		if matched {
			data, _ := s.fs.Read(ctx, e.Path, caller)
			results = append(results, s.propfindResponse(e.Path, core.Stat{
				Path: e.Path,
				Kind: e.Kind,
				Mode: e.Mode,
				UID:  e.UID,
				GID:  e.GID,
				Size: int64(len(data)),
			}, data))
		}
	}
	return results, nil
}

func (s *Server) buildPropfindEntries(ctx context.Context, p string, depth string, caller core.CallerIdentity) ([]response, error) {
	maxDepth := s.opts.MaxPropfindDepth
	if maxDepth == 0 {
		maxDepth = 3
	}

	switch depth {
	case "0":
		return s.propfindRecursive(ctx, p, 0, 0, caller)
	case "1":
		return s.propfindRecursive(ctx, p, 0, 1, caller)
	case "infinity":
		if maxDepth < 0 {
			return s.propfindRecursive(ctx, p, 0, -1, caller)
		}
		return s.propfindRecursive(ctx, p, 0, maxDepth, caller)
	default:
		return s.propfindRecursive(ctx, p, 0, 0, caller)
	}
}

func (s *Server) propfindRecursive(ctx context.Context, p string, currentDepth, maxDepth int, caller core.CallerIdentity) ([]response, error) {
	stat, etagStr, hit := s.propCache.get(p)
	if !hit {
		var err error
		stat, err = s.fs.Stat(p, caller)
		if err != nil {
			return nil, err
		}
		data, _ := s.fs.Read(ctx, p, caller)
		etagStr = ""
		if len(data) > 0 {
			etagStr = etag(data)
		}
		s.propCache.set(p, stat, etagStr)
	}

	var entries []response
	entries = append(entries, s.propfindResponseCached(p, stat, etagStr))

	if maxDepth >= 0 && currentDepth >= maxDepth {
		return entries, nil
	}
	if stat.Kind != core.KindDir {
		return entries, nil
	}

	children, err := s.fs.Readdir(p, caller)
	if err != nil {
		return nil, err
	}
	for _, child := range children {
		childPath := path.Join(p, child.Name)
		if childPath == "" {
			childPath = "/" + child.Name
		}
		childEntries, err := s.propfindRecursive(ctx, childPath, currentDepth+1, maxDepth, caller)
		if err != nil {
			continue
		}
		entries = append(entries, childEntries...)
	}
	return entries, nil
}

func (s *Server) propfindResponse(p string, stat core.Stat, data []byte) response {
	etagStr := ""
	if len(data) > 0 {
		etagStr = etag(data)
	}
	return s.propfindResponseCached(p, stat, etagStr)
}

func (s *Server) propfindResponseCached(p string, stat core.Stat, etagStr string) response {
	var rt *resourceType
	if stat.Kind == core.KindDir {
		rt = &resourceType{Collection: ""}
	}
	pr := prop{
		DisplayName:         path.Base(p),
		GetContentLength:    stat.Size,
		GetContentType:      contentTypeFromKind(stat.Kind),
		ResourceType:        rt,
		CreationDate:        "1970-01-01T00:00:00Z",
		GetLastModified:     "Thu, 01 Jan 1970 00:00:00 GMT",
		QuotaAvailableBytes: 1 << 62,
		QuotaUsedBytes:      0,
	}
	if etagStr != "" {
		pr.GetETag = etagStr
	}
	return response{
		Href:     p,
		Propstat: propstat{Prop: pr, Status: "HTTP/1.1 200 OK"},
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
	XMLName             xml.Name      `xml:"D:prop"`
	DisplayName         string        `xml:"D:displayname"`
	GetContentLength    int64         `xml:"D:getcontentlength"`
	GetContentType      string        `xml:"D:getcontenttype"`
	GetETag             string        `xml:"D:getetag,omitempty"`
	ResourceType        *resourceType `xml:"D:resourcetype"`
	CreationDate        string        `xml:"D:creationdate"`
	GetLastModified     string        `xml:"D:getlastmodified"`
	QuotaAvailableBytes int64         `xml:"D:quota-available-bytes"`
	QuotaUsedBytes      int64         `xml:"D:quota-used-bytes"`
}

type resourceType struct {
	XMLName    xml.Name `xml:"D:resourcetype"`
	Collection string   `xml:"D:collection,omitempty"`
}

// DASL search XML structures.
type searchRequest struct {
	XMLName     xml.Name    `xml:"searchrequest"`
	BasicSearch basicSearch `xml:"basicsearch"`
}

type basicSearch struct {
	Where struct {
		Like struct {
			Literal string `xml:"literal"`
		} `xml:"like"`
	} `xml:"where"`
}

// Lock XML structures.
type lockDiscovery struct {
	XMLName    xml.Name   `xml:"D:prop"`
	XmlnsD     string     `xml:"xmlns:D,attr"`
	ActiveLock activeLock `xml:"D:lockdiscovery>D:activelock"`
}

type activeLock struct {
	LockType  lockType  `xml:"D:locktype>D:write"`
	LockScope lockScope `xml:"D:lockscope>D:exclusive"`
	Depth     string    `xml:"D:depth"`
	Owner     string    `xml:"D:owner"`
	Timeout   string    `xml:"D:timeout"`
	LockToken lockToken `xml:"D:locktoken>D:href"`
}

type lockType struct {
	Write string `xml:"D:write,omitempty"`
}

type lockScope struct {
	Exclusive string `xml:"D:exclusive,omitempty"`
}

type lockToken struct {
	Href string `xml:"D:href"`
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

type gzipResponseWriter struct {
	http.ResponseWriter
	Writer *gzip.Writer
	wrote  bool
}

func (gz *gzipResponseWriter) WriteHeader(code int) {
	if !gz.wrote {
		gz.wrote = true
		gz.ResponseWriter.WriteHeader(code)
	}
}

func (gz *gzipResponseWriter) Write(p []byte) (int, error) {
	if !gz.wrote {
		gz.WriteHeader(http.StatusOK)
	}
	return gz.Writer.Write(p)
}

func (gz *gzipResponseWriter) Close() error {
	return gz.Writer.Close()
}

func etag(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf(`"%s"`, hex.EncodeToString(h[:8]))
}

func matchETag(reqETag, currentETag string) bool {
	return reqETag == currentETag || reqETag == "*"
}

// parseRange parses a single "bytes=start-end" Range header.
// It returns start, end (inclusive), and ok.
func parseRange(rng string, total int64) (int64, int64, bool) {
	const prefix = "bytes="
	if !strings.HasPrefix(rng, prefix) {
		return 0, 0, false
	}
	parts := strings.SplitN(strings.TrimPrefix(rng, prefix), "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	var start, end int64
	if parts[0] == "" {
		// suffix range: bytes=-N means last N bytes
		if _, err := fmt.Sscanf(parts[1], "%d", &end); err != nil {
			return 0, 0, false
		}
		start = total - end
		if start < 0 {
			start = 0
		}
		end = total - 1
	} else {
		if _, err := fmt.Sscanf(parts[0], "%d", &start); err != nil {
			return 0, 0, false
		}
		if parts[1] == "" {
			end = total - 1
		} else {
			if _, err := fmt.Sscanf(parts[1], "%d", &end); err != nil {
				return 0, 0, false
			}
		}
	}
	if start < 0 || start >= total || end < start {
		return 0, 0, false
	}
	if end >= total {
		end = total - 1
	}
	return start, end, true
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

func sanitizePath(p string) string {
	if p == "" || p == "/" {
		return "/"
	}
	if p[0] != '/' {
		return ""
	}
	// Reject paths containing traversal or empty segments.
	for _, seg := range strings.Split(p[1:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return ""
		}
	}
	return p
}
