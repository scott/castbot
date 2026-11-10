package castlet

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (s *Supervisor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	switch {
	case r.URL.Path == "/castlet/health" && r.Method == http.MethodGet:
		s.health(w, r)
	case r.URL.Path == "/castlet/reload" && r.Method == http.MethodPost:
		s.handleReload(w, r)
	case r.URL.Path == "/castlet/logs" && r.Method == http.MethodGet:
		s.serveLogs(w, r)
	case r.URL.Path == "/castlet/memory" && r.Method == http.MethodGet:
		s.exportMemory(w, r)
	case r.URL.Path == "/castlet/memory" && r.Method == http.MethodPut:
		s.importMemory(w, r)
	case r.URL.Path == "/castlet/files" && r.Method == http.MethodGet:
		s.listFiles(w, r)
	case strings.HasPrefix(r.URL.Path, "/castlet/files/") && (r.Method == http.MethodGet || r.Method == http.MethodPut):
		s.file(w, r)
	case r.URL.Path == "/.well-known/agent.json" && r.Method == http.MethodGet:
		s.proxy(w, r)
	case (r.URL.Path == "/" || r.URL.Path == "") && r.Method == http.MethodPost:
		s.proxy(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Supervisor) authorize(w http.ResponseWriter, r *http.Request) bool {
	got := bearer(r.Header.Get("X-Cast-Authorization"))
	s.mu.Lock()
	want := s.token
	s.mu.Unlock()
	if want == "" || !hashEq(got, want) {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func bearer(h string) string {
	h = strings.TrimSpace(h)
	if len(h) >= 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return h
}

func hashEq(a, b string) bool {
	ah := sha256.Sum256([]byte(a))
	bh := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ah[:], bh[:]) == 1
}

func (s *Supervisor) health(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	ready := s.ready
	version := s.version
	sum := s.sha
	bootErr := s.bootErr
	s.mu.Unlock()
	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ready":   ready,
		"version": version,
		"sha256":  sum,
		"error":   bootErr,
	})
}

func (s *Supervisor) handleReload(w http.ResponseWriter, r *http.Request) {
	if err := s.reload(r.Context()); err != nil {
		http.Error(w, "reload failed", http.StatusBadGateway)
		return
	}
	s.health(w, r)
}

func (s *Supervisor) serveLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(s.logs.Bytes())
}

func (s *Supervisor) proxy(w http.ResponseWriter, r *http.Request) {
	if !s.isReady() {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, s.cfg.RuntimeURL+r.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if accept := r.Header.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}
	if auth := r.Header.Get("X-Cast-Authorization"); auth != "" {
		req.Header.Set("X-Cast-Authorization", auth)
	}
	res, err := s.proxyClient.Do(req)
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(res.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 1024)
	for {
		n, rerr := res.Body.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

func (s *Supervisor) isReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready
}

func (s *Supervisor) listFiles(w http.ResponseWriter, r *http.Request) {
	root := filepath.Join(s.cfg.AgentDir, "workspace")
	var out []map[string]any
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		out = append(out, map[string]any{"name": filepath.ToSlash(rel), "size": info.Size()})
		return nil
	})
	if out == nil {
		out = []map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Supervisor) file(w http.ResponseWriter, r *http.Request) {
	rel, ok := safeRel(strings.TrimPrefix(r.URL.Path, "/castlet/files/"))
	if !ok {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	root := filepath.Join(s.cfg.AgentDir, "workspace")
	full := filepath.Join(root, filepath.FromSlash(rel))
	if !under(root, full) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodPut {
		s.putFile(w, r, full)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, f)
}

func (s *Supervisor) putFile(w http.ResponseWriter, r *http.Request, full string) {
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".upload-*")
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(r.Body, fileCap+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil || n > fileCap {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := os.Rename(tmp.Name(), full); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Supervisor) exportMemory(w http.ResponseWriter, r *http.Request) {
	root := filepath.Join(s.cfg.AgentDir, "memory")
	if err := os.MkdirAll(root, 0o755); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Cache-Control", "no-store")
	tw := tar.NewWriter(w)
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		hdr := &tar.Header{Name: rel, Mode: 0o644, Size: info.Size(), ModTime: time.Unix(0, 0).UTC(), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		return err
	})
	_ = tw.Close()
}

func (s *Supervisor) importMemory(w http.ResponseWriter, r *http.Request) {
	root := filepath.Join(s.cfg.AgentDir, "memory")
	if err := os.MkdirAll(root, 0o755); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	tr := tar.NewReader(io.LimitReader(r.Body, fileCap+1))
	var used int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != '\x00' {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		rel, ok := safeRel(hdr.Name)
		if !ok {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if used+hdr.Size > fileCap {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		full := filepath.Join(root, filepath.FromSlash(rel))
		if !under(root, full) {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		f, err := os.OpenFile(full, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		n, err := io.Copy(f, io.LimitReader(tr, hdr.Size))
		cerr := f.Close()
		if err != nil || cerr != nil || n != hdr.Size {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		used += n
	}
	w.WriteHeader(http.StatusNoContent)
}

func safeRel(name string) (string, bool) {
	name = strings.TrimPrefix(path.Clean("/"+name), "/")
	if name == "" || name == "." || strings.HasPrefix(name, "../") || name == ".." {
		return "", false
	}
	if strings.Contains(name, "\\") {
		return "", false
	}
	return name, true
}

func under(root, full string) bool {
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
