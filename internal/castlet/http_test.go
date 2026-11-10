package castlet

import (
	"archive/tar"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/scott/castbot/internal/runtime"
)

func TestMissingTokenDoesNotReachTheRuntime(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(up.Close)
	sup, err := New(Config{Token: "agent-token", RuntimeURL: up.URL, AgentDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	sup.mu.Lock()
	sup.ready = true
	sup.mu.Unlock()
	for _, header := range []string{"", "Bearer wrong"} {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"jsonrpc":"2.0"}`))
		if header != "" {
			req.Header.Set("X-Cast-Authorization", header)
		}
		rec := httptest.NewRecorder()
		sup.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status %d", rec.Code)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("runtime hits %d", hits.Load())
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	req.Header.Set("X-Cast-Authorization", "Bearer agent-token")
	rec := httptest.NewRecorder()
	sup.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || hits.Load() != 1 {
		t.Fatalf("proxy %d hits %d", rec.Code, hits.Load())
	}
}

func TestFileAndMemoryRoundTrip(t *testing.T) {
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "Atlas"})
	t.Cleanup(echo.Close)
	up := httptest.NewServer(echo)
	t.Cleanup(up.Close)
	dir := t.TempDir()
	sup, err := New(Config{Token: "agent-token", RuntimeURL: up.URL, AgentDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	sup.mu.Lock()
	sup.ready = true
	sup.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(dir, "workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	put := httptest.NewRequest(http.MethodPut, "/castlet/files/uploads/hello.txt", strings.NewReader("hello file"))
	put.Header.Set("X-Cast-Authorization", "Bearer agent-token")
	rec := httptest.NewRecorder()
	sup.ServeHTTP(rec, put)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("put %d %s", rec.Code, rec.Body.String())
	}
	get := httptest.NewRequest(http.MethodGet, "/castlet/files/uploads/hello.txt", nil)
	get.Header.Set("X-Cast-Authorization", "Bearer agent-token")
	rec = httptest.NewRecorder()
	sup.ServeHTTP(rec, get)
	if rec.Code != http.StatusOK || rec.Body.String() != "hello file" {
		t.Fatalf("get %d %q", rec.Code, rec.Body.String())
	}

	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	body := []byte("remember this")
	if err := tw.WriteHeader(&tar.Header{Name: "note.txt", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	imp := httptest.NewRequest(http.MethodPut, "/castlet/memory", bytes.NewReader(tarBuf.Bytes()))
	imp.Header.Set("X-Cast-Authorization", "Bearer agent-token")
	rec = httptest.NewRecorder()
	sup.ServeHTTP(rec, imp)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("import %d %s", rec.Code, rec.Body.String())
	}
	exp := httptest.NewRequest(http.MethodGet, "/castlet/memory", nil)
	exp.Header.Set("X-Cast-Authorization", "Bearer agent-token")
	rec = httptest.NewRecorder()
	sup.ServeHTTP(rec, exp)
	if rec.Code != http.StatusOK {
		t.Fatalf("export %d", rec.Code)
	}
	tr := tar.NewReader(bytes.NewReader(rec.Body.Bytes()))
	hdr, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Name != "note.txt" {
		t.Fatalf("name %s", hdr.Name)
	}
	got, err := io.ReadAll(tr)
	if err != nil || string(got) != "remember this" {
		t.Fatalf("memory %q %v", got, err)
	}
}
