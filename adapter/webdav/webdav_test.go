package webdav

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"encoding/xml"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/skills-fs/skills-fs/adapter"
	"github.com/skills-fs/skills-fs/core"
	httpprovider "github.com/skills-fs/skills-fs/provider/http"
)

func TestWebDAVGetAndPut(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o666, BlobData: []byte("hello")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()

	// GET
	resp, err := http.Get(baseURL + "/blob")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	if string(data) != "hello" {
		t.Fatalf("GET body = %q", string(data))
	}

	// PUT
	req, _ := http.NewRequest(http.MethodPut, baseURL+"/blob", bytes.NewReader([]byte("world")))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT status = %d", resp.StatusCode)
	}

	// GET again
	resp, err = http.Get(baseURL + "/blob")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ = io.ReadAll(resp.Body)
	if string(data) != "world" {
		t.Fatalf("GET after PUT = %q", string(data))
	}
}

func TestWebDAVReadOnlyRejectsPut(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o444, BlobData: []byte("x")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{ReadOnly: true})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest(http.MethodPut, baseURL+"/blob", bytes.NewReader([]byte("y")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestWebDAVHead(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o444, BlobData: []byte("head-test")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	resp, err := http.Head(baseURL + "/blob")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status = %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Length") != "9" {
		t.Fatalf("Content-Length = %q", resp.Header.Get("Content-Length"))
	}
}

func TestWebDAVNotFound(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	resp, err := http.Get(baseURL + "/missing")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestWebDAVOptions(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest(http.MethodOptions, baseURL+"/", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("OPTIONS status = %d", resp.StatusCode)
	}
	if resp.Header.Get("DAV") != "1, 2" {
		t.Fatalf("DAV header = %q", resp.Header.Get("DAV"))
	}
}

// decode-only structs using full namespace URI for proper xml.Unmarshal.
type decodeMultistatus struct {
	XMLName   xml.Name         `xml:"DAV: multistatus"`
	Responses []decodeResponse `xml:"DAV: response"`
}

type decodeResponse struct {
	XMLName  xml.Name       `xml:"DAV: response"`
	Href     string         `xml:"DAV: href"`
	Propstat decodePropstat `xml:"DAV: propstat"`
}

type decodePropstat struct {
	Prop   decodeProp `xml:"DAV: prop"`
	Status string     `xml:"DAV: status"`
}

type decodeProp struct {
	DisplayName      string         `xml:"DAV: displayname"`
	GetContentLength int64          `xml:"DAV: getcontentlength"`
	GetContentType   string         `xml:"DAV: getcontenttype"`
	ResourceType     *decodeResType `xml:"DAV: resourcetype"`
	CreationDate     string         `xml:"DAV: creationdate"`
	GetLastModified  string         `xml:"DAV: getlastmodified"`
}

type decodeResType struct {
	Collection string `xml:"DAV: collection"`
}

func TestWebDAVPropfindDir(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/dir", core.MountEntry{Kind: core.KindDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/dir/a", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("alpha")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/dir/b", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("beta")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest("PROPFIND", baseURL+"/dir", nil)
	req.Header.Set("Depth", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND status = %d", resp.StatusCode)
	}

	var ms decodeMultistatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		t.Fatalf("decode multistatus: %v", err)
	}
	if len(ms.Responses) != 3 {
		t.Fatalf("expected 3 responses, got %d", len(ms.Responses))
	}
	hrefs := make(map[string]bool)
	for _, r := range ms.Responses {
		hrefs[r.Href] = true
		if r.Href == "/dir" {
			if r.Propstat.Prop.ResourceType == nil {
				t.Fatalf("/dir should be a collection")
			}
		}
	}
	if !hrefs["/dir"] || !hrefs["/dir/a"] || !hrefs["/dir/b"] {
		t.Fatalf("missing hrefs: %+v", hrefs)
	}
}

func TestWebDAVPropfindFile(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/file", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("data")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest("PROPFIND", baseURL+"/file", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND status = %d", resp.StatusCode)
	}

	var ms decodeMultistatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		t.Fatalf("decode multistatus: %v", err)
	}
	if len(ms.Responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(ms.Responses))
	}
	if ms.Responses[0].Href != "/file" {
		t.Fatalf("expected href /file, got %s", ms.Responses[0].Href)
	}
	if ms.Responses[0].Propstat.Prop.GetContentLength != 4 {
		t.Fatalf("expected content length 4, got %d", ms.Responses[0].Propstat.Prop.GetContentLength)
	}
	if ms.Responses[0].Propstat.Prop.ResourceType != nil {
		t.Fatalf("file should not have a collection resource type")
	}
}

func TestWebDAVPropfindNotFound(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest("PROPFIND", baseURL+"/missing", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestWebDAVBasicAuthUIDMapping(t *testing.T) {
	server := New(nil, "127.0.0.1:0", adapter.MountOptions{})
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic MTAwMDoxMjM0") // base64("1000:1234")
	caller := server.callerFromRequest(req)
	if caller.UID != 1000 {
		t.Fatalf("expected UID 1000, got %d", caller.UID)
	}
	if caller.GID != 1000 {
		t.Fatalf("expected GID 1000, got %d", caller.GID)
	}
}

func TestWebDAVReadOnlyRejectsDelete(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o444, BlobData: []byte("x")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{ReadOnly: true})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest(http.MethodDelete, baseURL+"/blob", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestWebDAVPropfindDepthZero(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/dir", core.MountEntry{Kind: core.KindDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/dir/a", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("alpha")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest("PROPFIND", baseURL+"/dir", nil)
	req.Header.Set("Depth", "0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND status = %d", resp.StatusCode)
	}

	var ms decodeMultistatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		t.Fatalf("decode multistatus: %v", err)
	}
	if len(ms.Responses) != 1 {
		t.Fatalf("expected 1 response for Depth:0, got %d", len(ms.Responses))
	}
	if ms.Responses[0].Href != "/dir" {
		t.Fatalf("expected href /dir, got %s", ms.Responses[0].Href)
	}
}

func TestWebDAVPropfindEmptyDir(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/empty", core.MountEntry{Kind: core.KindDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest("PROPFIND", baseURL+"/empty", nil)
	req.Header.Set("Depth", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND status = %d", resp.StatusCode)
	}

	var ms decodeMultistatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		t.Fatalf("decode multistatus: %v", err)
	}
	if len(ms.Responses) != 1 {
		t.Fatalf("expected 1 response for empty dir, got %d", len(ms.Responses))
	}
	if ms.Responses[0].Href != "/empty" {
		t.Fatalf("expected href /empty, got %s", ms.Responses[0].Href)
	}
}

func TestWebDAVPutNotFound(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest(http.MethodPut, baseURL+"/newfile", bytes.NewReader([]byte("data")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for PUT to non-existent path, got %d", resp.StatusCode)
	}
}

func TestWebDAVDelete(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("x")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest(http.MethodDelete, baseURL+"/blob", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	// Verify it's gone
	resp, err = http.Get(baseURL + "/blob")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", resp.StatusCode)
	}
}

func TestWebDAVDeleteReadOnly(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o444, BlobData: []byte("x")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{ReadOnly: true})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest(http.MethodDelete, baseURL+"/blob", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestWebDAVDeleteNotFound(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest(http.MethodDelete, baseURL+"/missing", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestWebDAVHTTPProviderIntegration(t *testing.T) {
	// Start a backend HTTP provider.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("from-remote"))
	}))
	defer backend.Close()

	// Set up filesystem with HTTP provider and API mount.
	fs := core.NewFS(core.GlobalConfig{})
	p := httpprovider.NewProvider("remote", backend.URL)
	if err := fs.RegisterProvider(p); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/api/greet", core.MountEntry{
		Kind: core.KindAPI,
		Mode: 0o444,
		Ops: map[core.OpCode]*core.CapConfig{
			core.OpRead: {ProviderID: "remote", Action: "greet"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Start WebDAV server.
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	// Read through WebDAV.
	baseURL := "http://" + server.ln.Addr().String()
	resp, err := http.Get(baseURL + "/api/greet")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	if string(data) != "from-remote" {
		t.Fatalf("GET body = %q", string(data))
	}
}

func TestWebDAVMkcol(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest("MKCOL", baseURL+"/newdir", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	// Verify it exists
	stat, err := fs.Stat("/newdir", core.CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if stat.Kind != core.KindDir {
		t.Fatalf("expected dir, got %s", stat.Kind)
	}
}

func TestWebDAVMkcolConflict(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/dir", core.MountEntry{Kind: core.KindDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest("MKCOL", baseURL+"/dir", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
}

func TestWebDAVMkcolReadOnly(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{ReadOnly: true})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest("MKCOL", baseURL+"/dir", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestWebDAVCopyBlob(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/src", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("hello")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest("COPY", baseURL+"/src", nil)
	req.Header.Set("Destination", baseURL+"/dst")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	data, err := fs.Read(context.Background(), "/dst", core.CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("copy data = %q", data)
	}
}

func TestWebDAVCopyDir(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/src", core.MountEntry{Kind: core.KindDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest("COPY", baseURL+"/src", nil)
	req.Header.Set("Destination", baseURL+"/dst")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	stat, err := fs.Stat("/dst", core.CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if stat.Kind != core.KindDir {
		t.Fatalf("expected dir, got %s", stat.Kind)
	}
}

func TestWebDAVMove(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/src", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("world")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest("MOVE", baseURL+"/src", nil)
	req.Header.Set("Destination", baseURL+"/dst")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	data, err := fs.Read(context.Background(), "/dst", core.CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "world" {
		t.Fatalf("move data = %q", data)
	}

	_, err = fs.Stat("/src", core.CallerIdentity{})
	if err == nil {
		t.Fatal("expected src to be gone after move")
	}
}

func TestWebDAVCopyNotFound(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest("COPY", baseURL+"/missing", nil)
	req.Header.Set("Destination", baseURL+"/dst")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestWebDAVProppatch(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req := mustNewRequest(t, "PROPPATCH", baseURL+"/blob", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("expected 207, got %d", resp.StatusCode)
	}
}

func TestWebDAVProppatchReadOnly(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{ReadOnly: true})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req := mustNewRequest(t, "PROPPATCH", baseURL+"/blob", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestWebDAVSizeLimits(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("hello-world-data")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{MaxRequestSize: 5, MaxResponseSize: 5})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()

	// GET should fail because response exceeds limit.
	resp, err := http.Get(baseURL + "/blob")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 for oversized response, got %d", resp.StatusCode)
	}

	// PUT should fail because request exceeds limit.
	req, _ := http.NewRequest(http.MethodPut, baseURL+"/blob", strings.NewReader("too-large"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized request, got %d", resp.StatusCode)
	}
}

func TestWebDAVDebugEndpoint(t *testing.T) {
	server := New(core.NewFS(core.GlobalConfig{}), "127.0.0.1:0", adapter.MountOptions{Debug: true})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	resp, err := http.Get(baseURL + "/debug/pprof/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestWebDAVHealthz(t *testing.T) {
	server := New(core.NewFS(core.GlobalConfig{}), "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	resp, err := http.Get(baseURL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok\n" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestWebDAVOptionsDAVHeader(t *testing.T) {
	server := New(core.NewFS(core.GlobalConfig{}), "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	resp, err := http.DefaultClient.Do(mustNewRequest(t, "OPTIONS", baseURL+"/", nil))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("DAV"); got != "1, 2" {
		t.Fatalf("expected DAV '1, 2', got %q", got)
	}
}

func TestWebDAVLockUnlockStub(t *testing.T) {
	server := New(core.NewFS(core.GlobalConfig{}), "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	resp, err := http.DefaultClient.Do(mustNewRequest(t, "LOCK", baseURL+"/x", nil))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for LOCK, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Lock-Token") == "" {
		t.Fatal("expected Lock-Token header")
	}

	resp, err = http.DefaultClient.Do(mustNewRequest(t, "UNLOCK", baseURL+"/x", nil))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 for UNLOCK, got %d", resp.StatusCode)
	}
}

func TestWebDAVPropfindContentType(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o666, BlobData: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req := mustNewRequest(t, "PROPFIND", baseURL+"/blob", nil)
	req.Header.Set("Depth", "0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("expected 207, got %d", resp.StatusCode)
	}
	var ms decodeMultistatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		t.Fatal(err)
	}
	if len(ms.Responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(ms.Responses))
	}
	prop := ms.Responses[0].Propstat.Prop
	if prop.GetContentType != "application/octet-stream" {
		t.Fatalf("expected content type, got %q", prop.GetContentType)
	}
	if prop.CreationDate == "" {
		t.Fatal("expected creationdate")
	}
	if prop.GetLastModified == "" {
		t.Fatal("expected getlastmodified")
	}
}

func mustNewRequest(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func writeTempCert(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{{127, 0, 0, 1}},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certF, err := os.CreateTemp(t.TempDir(), "*.pem")
	if err != nil {
		t.Fatal(err)
	}
	pem.Encode(certF, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	certF.Close()
	keyF, err := os.CreateTemp(t.TempDir(), "*.pem")
	if err != nil {
		t.Fatal(err)
	}
	pem.Encode(keyF, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	keyF.Close()
	return certF.Name(), keyF.Name()
}

func TestWebDAVTLS(t *testing.T) {
	certFile, keyFile := writeTempCert(t)
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o666, BlobData: []byte("tls-test")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{TLSCertFile: certFile, TLSKeyFile: keyFile})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	baseURL := "https://" + server.ln.Addr().String()
	resp, err := client.Get(baseURL + "/blob")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "tls-test" {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestWebDAVPathSanitization(t *testing.T) {
	for _, tc := range []struct {
		path string
		want string
	}{
		{"/", "/"},
		{"/foo", "/foo"},
		{"", "/"},
		{"/../etc/passwd", ""},
		{"/foo/../bar", ""},
		{"/foo/./bar", ""},
		{"/foo//bar", ""},
	} {
		got := sanitizePath(tc.path)
		if got != tc.want {
			t.Fatalf("sanitizePath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestWebDAVGzip(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	big := strings.Repeat("hello-webdav-gzip-test-", 100)
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte(big)}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{EnableGzip: true})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()

	// Request without Accept-Encoding should not be compressed.
	resp, err := http.Get(baseURL + "/blob")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != big {
		t.Fatal("uncompressed GET body mismatch")
	}
	if resp.Header.Get("Content-Encoding") == "gzip" {
		t.Fatal("unexpected gzip without Accept-Encoding")
	}

	// Request with Accept-Encoding: gzip should be compressed.
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/blob", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected gzip encoding, got %q", resp.Header.Get("Content-Encoding"))
	}
	gr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	decompressed, err := io.ReadAll(gr)
	gr.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(decompressed) != big {
		t.Fatalf("decompressed body mismatch: got %d bytes, want %d", len(decompressed), len(big))
	}
}
