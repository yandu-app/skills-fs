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
	"encoding/json"
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

type fakeProvider struct {
	id string
}

func (p *fakeProvider) ID() string { return p.id }
func (p *fakeProvider) Invoke(ctx context.Context, action string, params map[string]interface{}) (*core.ProviderResult, error) {
	return nil, nil
}

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
	data, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
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
	if resp.Header.Get("DAV") != "1" {
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

func TestWebDAVPropfindDepthInfinity(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/dir", core.MountEntry{Kind: core.KindDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/dir/a.txt", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("a")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/dir/sub", core.MountEntry{Kind: core.KindDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/dir/sub/nested.txt", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("nested")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest("PROPFIND", baseURL+"/dir", nil)
	req.Header.Set("Depth", "infinity")
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
	if len(ms.Responses) != 4 {
		t.Fatalf("expected 4 responses for Depth:infinity, got %d", len(ms.Responses))
	}
	hrefs := make(map[string]bool)
	for _, r := range ms.Responses {
		hrefs[r.Href] = true
	}
	for _, want := range []string{"/dir", "/dir/a.txt", "/dir/sub", "/dir/sub/nested.txt"} {
		if !hrefs[want] {
			t.Fatalf("missing href %s", want)
		}
	}
}

func TestWebDAVPropfindDepthInfinityLimited(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/dir", core.MountEntry{Kind: core.KindDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/dir/a.txt", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("a")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/dir/sub", core.MountEntry{Kind: core.KindDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/dir/sub/nested.txt", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("nested")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{MaxPropfindDepth: 1})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest("PROPFIND", baseURL+"/dir", nil)
	req.Header.Set("Depth", "infinity")
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
		t.Fatalf("expected 3 responses with MaxPropfindDepth=1, got %d", len(ms.Responses))
	}
	hrefs := make(map[string]bool)
	for _, r := range ms.Responses {
		hrefs[r.Href] = true
	}
	for _, want := range []string{"/dir", "/dir/a.txt", "/dir/sub"} {
		if !hrefs[want] {
			t.Fatalf("missing href %s", want)
		}
	}
	if hrefs["/dir/sub/nested.txt"] {
		t.Fatal("expected /dir/sub/nested.txt to be excluded by depth limit")
	}
}

func TestWebDAVPropfindDepthInfinityUnlimited(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/dir", core.MountEntry{Kind: core.KindDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/dir/a.txt", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("a")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/dir/sub", core.MountEntry{Kind: core.KindDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/dir/sub/nested.txt", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("nested")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{MaxPropfindDepth: -1})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest("PROPFIND", baseURL+"/dir", nil)
	req.Header.Set("Depth", "infinity")
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
	if len(ms.Responses) != 4 {
		t.Fatalf("expected 4 responses for unlimited depth, got %d", len(ms.Responses))
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
	fs := core.NewFS(core.GlobalConfig{})
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
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
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("expected JSON content type, got %q", ct)
	}
	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Fatalf("expected status ok, got %+v", body)
	}
	if _, ok := body["providers"]; !ok {
		t.Fatal("missing providers field")
	}
}

func TestWebDAVHealthzWithProvider(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.RegisterProvider(&fakeProvider{id: "p1"}); err != nil {
		t.Fatal(err)
	}
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	resp, err := http.Get("http://" + server.ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	providers, _ := body["providers"].(map[string]interface{})
	if providers["p1"] != "unknown" {
		t.Fatalf("expected p1=unknown, got %+v", providers)
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
	if got := resp.Header.Get("DAV"); got != "1" {
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

func TestWebDAVETag(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("etag-test")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()

	// GET should return ETag.
	resp, err := http.Get(baseURL + "/blob")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	etagHeader := resp.Header.Get("ETag")
	if etagHeader == "" {
		t.Fatal("expected ETag header")
	}

	// GET with matching If-None-Match should return 304.
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/blob", nil)
	req.Header.Set("If-None-Match", etagHeader)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("expected 304, got %d", resp.StatusCode)
	}

	// GET with non-matching If-None-Match should return 200.
	req, _ = http.NewRequest(http.MethodGet, baseURL+"/blob", nil)
	req.Header.Set("If-None-Match", `"bad-etag"`)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// HEAD should return ETag.
	req, _ = http.NewRequest(http.MethodHead, baseURL+"/blob", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if resp.Header.Get("ETag") != etagHeader {
		t.Fatalf("HEAD ETag mismatch: got %q, want %q", resp.Header.Get("ETag"), etagHeader)
	}
}

func TestWebDAVPutIfMatch(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("original")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()

	// Fetch current ETag.
	resp, err := http.Get(baseURL + "/blob")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	curETag := resp.Header.Get("ETag")

	// PUT with matching If-Match should succeed.
	req, _ := http.NewRequest(http.MethodPut, baseURL+"/blob", strings.NewReader("updated"))
	req.Header.Set("If-Match", curETag)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	// Fetch new ETag after write.
	resp, err = http.Get(baseURL + "/blob")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	newETag := resp.Header.Get("ETag")

	// PUT with stale If-Match should return 412.
	req, _ = http.NewRequest(http.MethodPut, baseURL+"/blob", strings.NewReader("stale"))
	req.Header.Set("If-Match", curETag)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %d", resp.StatusCode)
	}

	// PUT with If-Match on non-existent resource should return 412.
	req, _ = http.NewRequest(http.MethodPut, baseURL+"/newblob", strings.NewReader("data"))
	req.Header.Set("If-Match", `"any"`)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected 412 for missing resource, got %d", resp.StatusCode)
	}

	// Verify the stale write did not apply.
	resp, err = http.Get(baseURL + "/blob")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "updated" {
		t.Fatalf("blob was unexpectedly modified: %q", string(body))
	}
	if resp.Header.Get("ETag") != newETag {
		t.Fatal("ETag changed after failed conditional write")
	}
}

func TestWebDAVPropfindETag(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("propfind-etag")}); err != nil {
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
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "getetag") {
		t.Fatalf("PROPFIND response missing getetag property: %s", string(body))
	}
}

func TestWebDAVPropfindQuota(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("quota-test")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest("PROPFIND", baseURL+"/blob", nil)
	req.Header.Set("Depth", "0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("expected 207, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "quota-available-bytes") {
		t.Fatalf("PROPFIND response missing quota-available-bytes: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "quota-used-bytes") {
		t.Fatalf("PROPFIND response missing quota-used-bytes: %s", bodyStr)
	}
}

func TestWebDAVSearch(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/hello.txt", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("hello")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/world.txt", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("world")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/other", core.MountEntry{Kind: core.KindDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	searchBody := `<?xml version="1.0" encoding="UTF-8"?>
<searchrequest xmlns="DAV:">
  <basicsearch>
    <where>
      <like>
        <literal>%hello%</literal>
      </like>
    </where>
  </basicsearch>
</searchrequest>`

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest("SEARCH", baseURL+"/", strings.NewReader(searchBody))
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
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
		t.Fatalf("decode multistatus: %v", err)
	}
	if len(ms.Responses) != 1 {
		t.Fatalf("expected 1 result for '%%hello%%', got %d", len(ms.Responses))
	}
	if ms.Responses[0].Href != "/hello.txt" {
		t.Fatalf("expected /hello.txt, got %s", ms.Responses[0].Href)
	}
}

func TestWebDAVRange(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	data := []byte("0123456789abcdef")
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: data}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()

	// Full range request.
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/blob", nil)
	req.Header.Set("Range", "bytes=0-7")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("expected 206, got %d", resp.StatusCode)
	}
	if string(body) != "01234567" {
		t.Fatalf("expected 01234567, got %q", string(body))
	}
	if resp.Header.Get("Content-Range") != "bytes 0-7/16" {
		t.Fatalf("unexpected Content-Range: %q", resp.Header.Get("Content-Range"))
	}

	// Open-ended range.
	req, _ = http.NewRequest(http.MethodGet, baseURL+"/blob", nil)
	req.Header.Set("Range", "bytes=8-")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if string(body) != "89abcdef" {
		t.Fatalf("expected 89abcdef, got %q", string(body))
	}

	// Suffix range.
	req, _ = http.NewRequest(http.MethodGet, baseURL+"/blob", nil)
	req.Header.Set("Range", "bytes=-4")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if string(body) != "cdef" {
		t.Fatalf("expected cdef, got %q", string(body))
	}

	// Out-of-range request.
	req, _ = http.NewRequest(http.MethodGet, baseURL+"/blob", nil)
	req.Header.Set("Range", "bytes=100-200")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("expected 416, got %d", resp.StatusCode)
	}
}

func TestParseRangeUnit(t *testing.T) {
	tests := []struct {
		rng       string
		total     int64
		wantOk    bool
		wantStart int64
		wantEnd   int64
	}{
		{"bytes=0-3", 10, true, 0, 3},
		{"bytes=5-9", 10, true, 5, 9},
		{"bytes=5-", 10, true, 5, 9},
		{"bytes=-3", 10, true, 7, 9},
		{"bytes=-3", 2, true, 0, 1},
		{"bytes=0-3", 3, true, 0, 2},
		{"bytes=10-15", 10, false, 0, 0},
		{"bytes=5-3", 10, false, 0, 0},
		{"bytes=0-3", 0, false, 0, 0},
		{"lines=0-3", 10, false, 0, 0},
	}
	for _, tc := range tests {
		start, end, ok := parseRange(tc.rng, tc.total)
		if ok != tc.wantOk {
			t.Fatalf("parseRange(%q, %d) ok=%v, want %v", tc.rng, tc.total, ok, tc.wantOk)
		}
		if ok && (start != tc.wantStart || end != tc.wantEnd) {
			t.Fatalf("parseRange(%q, %d) = %d-%d, want %d-%d", tc.rng, tc.total, start, end, tc.wantStart, tc.wantEnd)
		}
	}
}

func TestWebDAVCopyOverwrite(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/src", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("src-data")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/dst", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("dst-data")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()

	// COPY with Overwrite: F to existing destination should fail.
	req, _ := http.NewRequest("COPY", baseURL+"/src", nil)
	req.Header.Set("Destination", baseURL+"/dst")
	req.Header.Set("Overwrite", "F")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %d", resp.StatusCode)
	}

	// COPY with default (no Overwrite header) should succeed.
	req, _ = http.NewRequest("COPY", baseURL+"/src", nil)
	req.Header.Set("Destination", baseURL+"/dst")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	// Verify destination was overwritten.
	resp, err = http.Get(baseURL + "/dst")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "src-data" {
		t.Fatalf("expected src-data, got %q", string(body))
	}
}

func TestWebDAVMoveOverwrite(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/src", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("src-data")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/dst", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("dst-data")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()

	// MOVE with Overwrite: F to existing destination should fail.
	req, _ := http.NewRequest("MOVE", baseURL+"/src", nil)
	req.Header.Set("Destination", baseURL+"/dst")
	req.Header.Set("Overwrite", "F")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %d", resp.StatusCode)
	}

	// MOVE with Overwrite: T should succeed.
	req, _ = http.NewRequest("MOVE", baseURL+"/src", nil)
	req.Header.Set("Destination", baseURL+"/dst")
	req.Header.Set("Overwrite", "T")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	// Source should no longer exist.
	resp, err = http.Get(baseURL + "/src")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for moved source, got %d", resp.StatusCode)
	}
}

func TestWebDAVPropfindCacheInvalidationOnPut(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o666, BlobData: []byte("original")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{PropfindCacheTTL: time.Minute})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()

	// First PROPFIND populates cache.
	req := mustNewRequest(t, "PROPFIND", baseURL+"/blob", nil)
	req.Header.Set("Depth", "0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body1, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("expected 207, got %d", resp.StatusCode)
	}

	// PUT new content should invalidate cache.
	putReq, _ := http.NewRequest(http.MethodPut, baseURL+"/blob", strings.NewReader("updated"))
	resp, err = http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	// Second PROPFIND should reflect new content (cache invalidated).
	req = mustNewRequest(t, "PROPFIND", baseURL+"/blob", nil)
	req.Header.Set("Depth", "0")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("expected 207, got %d", resp.StatusCode)
	}

	// ETag should have changed because content changed.
	if string(body1) == string(body2) {
		t.Fatal("expected PROPFIND body to change after PUT invalidated cache")
	}
}

func TestWebDAVPropfindCacheTTLExpiration(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o666, BlobData: []byte("v1")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{PropfindCacheTTL: 50 * time.Millisecond})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()

	// First PROPFIND populates cache.
	req := mustNewRequest(t, "PROPFIND", baseURL+"/blob", nil)
	req.Header.Set("Depth", "0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body1, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("expected 207, got %d", resp.StatusCode)
	}

	// Change content behind the cache's back (bypass WebDAV so no invalidation).
	if err := fs.Write(context.Background(), "/blob", []byte("v2"), core.CallerIdentity{}); err != nil {
		t.Fatal(err)
	}

	// Wait for TTL to expire.
	time.Sleep(100 * time.Millisecond)

	// Second PROPFIND should see fresh content because cache expired.
	req = mustNewRequest(t, "PROPFIND", baseURL+"/blob", nil)
	req.Header.Set("Depth", "0")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("expected 207, got %d", resp.StatusCode)
	}

	if string(body1) == string(body2) {
		t.Fatal("expected PROPFIND body to change after cache TTL expired")
	}
}

func TestWebDAVPropfindCacheDisabled(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o666, BlobData: []byte("data")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{}) // no PropfindCacheTTL
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	if server.propCache != nil {
		t.Fatal("expected propCache to be nil when TTL is zero")
	}
}

func TestWebDAVCopyIfMatch(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/src", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("src-data")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()

	// Fetch current ETag.
	resp, err := http.Get(baseURL + "/src")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	curETag := resp.Header.Get("ETag")

	// COPY with matching If-Match should succeed.
	req, _ := http.NewRequest("COPY", baseURL+"/src", nil)
	req.Header.Set("Destination", baseURL+"/dst")
	req.Header.Set("If-Match", curETag)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	// COPY with stale If-Match should fail with 412.
	req, _ = http.NewRequest("COPY", baseURL+"/src", nil)
	req.Header.Set("Destination", baseURL+"/dst2")
	req.Header.Set("If-Match", `"stale-etag"`)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %d", resp.StatusCode)
	}
}

func TestWebDAVMoveIfMatch(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/src", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("move-me")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()

	// Fetch current ETag.
	resp, err := http.Get(baseURL + "/src")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	curETag := resp.Header.Get("ETag")

	// MOVE with matching If-Match should succeed.
	req, _ := http.NewRequest("MOVE", baseURL+"/src", nil)
	req.Header.Set("Destination", baseURL+"/dst")
	req.Header.Set("If-Match", curETag)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	// Source should be gone.
	resp, err = http.Get(baseURL + "/src")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for moved source, got %d", resp.StatusCode)
	}
}

func TestWebDAVCopyIfNoneMatch(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/src", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("src-data")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/dst", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("dst-data")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()

	// COPY with If-None-Match: * should fail when destination exists.
	req, _ := http.NewRequest("COPY", baseURL+"/src", nil)
	req.Header.Set("Destination", baseURL+"/dst")
	req.Header.Set("If-None-Match", "*")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %d", resp.StatusCode)
	}

	// COPY with If-None-Match: * should succeed when destination does not exist.
	req, _ = http.NewRequest("COPY", baseURL+"/src", nil)
	req.Header.Set("Destination", baseURL+"/newdst")
	req.Header.Set("If-None-Match", "*")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
}

func TestWebDAVMetricsEndpoint(t *testing.T) {
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

func TestWebDAVUnlockReadOnly(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{ReadOnly: true})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest("UNLOCK", baseURL+"/x", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestWebDAVMoveReadOnly(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/src", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{ReadOnly: true})
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
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestWebDAVMoveBadDestination(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/src", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest("MOVE", baseURL+"/src", nil)
	req.Header.Set("Destination", "://bad-url")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestWebDAVMoveOverwriteFalse(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/src", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("src")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/dst", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("dst")}); err != nil {
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
	req.Header.Set("Overwrite", "F")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %d", resp.StatusCode)
	}
}

func TestWebDAVMoveIfMatchFail(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/src", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("x")}); err != nil {
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
	req.Header.Set("If-Match", `"wrong-etag"`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %d", resp.StatusCode)
	}
}

func TestWebDAVCopyIfNoneMatchStar(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/src", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/dst", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("y")}); err != nil {
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
	req.Header.Set("If-None-Match", "*")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %d", resp.StatusCode)
	}
}

func TestWebDAVCopyLink(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/src", core.MountEntry{Kind: core.KindLink, Mode: 0o777, LinkPath: "/target"}); err != nil {
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
	if stat.Kind != core.KindLink {
		t.Fatalf("expected link, got %s", stat.Kind)
	}
}

func TestWebDAVUnlock(t *testing.T) {
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
	req, _ := http.NewRequest("UNLOCK", baseURL+"/blob", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
}

func TestWebDAVCopyIfMatchSuccess(t *testing.T) {
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
	req.Header.Set("If-Match", etag([]byte("hello")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
}

func TestWebDAVCopyIfNoneMatchSpecific(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/src", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("src")}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/dst", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("dst")}); err != nil {
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
	req.Header.Set("If-None-Match", etag([]byte("dst")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %d", resp.StatusCode)
	}
}

func TestWebDAVWriteErrorMapping(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	// Writing to a non-existent path triggers ENOENT → 404.
	req, _ := http.NewRequest("PUT", baseURL+"/no-parent/missing", strings.NewReader("x"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for ENOENT, got %d", resp.StatusCode)
	}
}

func TestHandleUnlockDirect(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	srv := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	w := httptest.NewRecorder()
	srv.handleUnlock(w, nil, "/x", core.CallerIdentity{})
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
}

func TestHandleUnlockReadOnlyDirect(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	srv := New(fs, "127.0.0.1:0", adapter.MountOptions{ReadOnly: true})
	w := httptest.NewRecorder()
	srv.handleUnlock(w, nil, "/x", core.CallerIdentity{})
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestWriteErrorMapping(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	srv := New(fs, "127.0.0.1:0", adapter.MountOptions{})

	cases := []struct {
		name string
		err  error
		want int
	}{
		{"ENOENT", &core.PosixError{Code: core.ENOENT}, http.StatusNotFound},
		{"EACCES", &core.PosixError{Code: core.EACCES}, http.StatusForbidden},
		{"EEXIST", &core.PosixError{Code: core.EEXIST}, http.StatusConflict},
		{"EINVAL", &core.PosixError{Code: core.EINVAL}, http.StatusBadRequest},
		{"EIO", &core.PosixError{Code: core.EIO}, http.StatusInternalServerError},
		{"plain", io.EOF, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			srv.writeError(w, tc.err)
			if w.Code != tc.want {
				t.Fatalf("expected %d, got %d", tc.want, w.Code)
			}
		})
	}
}

func TestWebDAVDebugPprof(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{Debug: true})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	resp, err := http.Get(baseURL + "/debug/pprof/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for pprof, got %d", resp.StatusCode)
	}
}

func TestWebDAVRateLimit(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{RateLimitRPS: 1, RateLimitBurst: 1})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	// First request should succeed.
	resp1, err := http.Get(baseURL + "/blob")
	if err != nil {
		t.Fatal(err)
	}
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for first request, got %d", resp1.StatusCode)
	}

	// Rapid-fire requests should eventually trigger rate limiting (429).
	var got429 bool
	for i := 0; i < 10; i++ {
		resp, err := http.Get(baseURL + "/blob")
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			got429 = true
			resp.Body.Close()
			break
		}
		resp.Body.Close()
	}
	if !got429 {
		t.Fatal("expected at least one 429 response within 10 rapid requests")
	}
}

func TestWebDAVConnLimit(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o644, BlobData: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{MaxConnections: 10})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	resp, err := http.Get(baseURL + "/blob")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}
