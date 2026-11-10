package castlet

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func (s *Supervisor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.observeClock()
	caller, ok := s.authorize(w, r)
	if !ok {
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), callerKey{}, caller))
	if strings.HasPrefix(r.URL.Path, "/castlet/") && caller != "control_plane" {
		http.NotFound(w, r)
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
	case r.URL.Path == "/castlet/journal" && r.Method == http.MethodGet:
		s.serveJournal(w, r)
	case r.URL.Path == "/castlet/cli-tokens" && r.Method == http.MethodPost:
		s.serveCLITokens(w, r)
	case r.URL.Path == "/.well-known/agent.json" && r.Method == http.MethodGet:
		s.proxy(w, r)
	case (r.URL.Path == "/" || r.URL.Path == "") && r.Method == http.MethodPost:
		s.proxy(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Supervisor) authorize(w http.ResponseWriter, r *http.Request) (string, bool) {
	got := bearer(r.Header.Get("X-Cast-Authorization"))
	if got == "" {
		got = bearer(r.Header.Get("Authorization"))
	}
	s.mu.Lock()
	want := s.token
	s.mu.Unlock()
	if want != "" && hashEq(got, want) {
		return "control_plane", true
	}
	if got != "" && s.cfg.ControlURL != "" {
		if !s.refreshCLITokens(r.Context()) {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return "", false
		}
	}
	if got != "" {
		if id, revoked := s.matchCLI(got); id != "" && !revoked {
			s.noteCLIUse(r.Context(), id)
			return "cli:" + id, true
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return "", false
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
	ready, version, sum, bootErr := s.snapshot()
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
		if errors.Is(err, errReloadBusy) {
			http.Error(w, "reload in progress", http.StatusConflict)
			return
		}
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
	const maxProxyBody = 1 << 20
	body, err := io.ReadAll(io.LimitReader(r.Body, maxProxyBody+1))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(body) > maxProxyBody {
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
		return
	}
	caller := callerOf(r)
	rpc := parseRPC(body)
	text := messageText(rpc)
	session := rpc.Params.Metadata.SessionID
	var wrote bool
	if rpc.Method == "message/send" && strings.TrimSpace(text) != "" {
		if _, err := s.appendJournal(journalEntry{
			SessionID: session, Caller: caller, Kind: "request", Text: text,
			MessageID: rpc.Params.Message.MessageID, StreamID: rpc.Params.Metadata.StreamID,
		}); err == nil {
			wrote = true
		}
	}
	if rpc.Method == "permission/respond" {
		decision := "recorded"
		if rpc.Params.Approved != nil {
			if *rpc.Params.Approved {
				decision = "approved"
			} else {
				decision = "denied"
			}
		}
		if session == "" {
			session = s.sessionForPerm(rpc.Params.ID)
		}
		if _, err := s.appendJournal(journalEntry{SessionID: session, Caller: caller, Kind: "decision", RequestID: rpc.Params.ID, Decision: decision}); err == nil {
			wrote = true
		}
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
	s.mu.Lock()
	token := s.token
	s.mu.Unlock()
	if token != "" {
		req.Header.Set("X-Cast-Authorization", "Bearer "+token)
	}
	res, err := s.proxyClient.Do(req)
	if err != nil {
		if wrote && rpc.Method == "message/send" {
			_, _ = s.appendJournal(journalEntry{SessionID: session, Caller: caller, Kind: "reply", Incomplete: true})
		}
		if wrote && strings.HasPrefix(caller, "cli:") {
			s.pushJournal(r.Context())
		}
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
	var cap turnCapture
	buf := make([]byte, 1024)
	for {
		n, rerr := res.Body.Read(buf)
		if n > 0 {
			cap.feed(buf[:n])
			if _, err := w.Write(buf[:n]); err != nil {
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			break
		}
	}
	cap.finish()
	if rpc.Method == "message/send" && strings.TrimSpace(text) != "" {
		for _, perm := range cap.perms {
			perm.SessionID = session
			perm.Caller = caller
			if _, err := s.appendJournal(perm); err == nil {
				wrote = true
			}
		}
		reply := cap.text.String()
		streamed := strings.Contains(res.Header.Get("Content-Type"), "event-stream")
		incomplete := res.StatusCode >= 400 || (streamed && !cap.sawDone)
		if strings.TrimSpace(reply) != "" || incomplete {
			if _, err := s.appendJournal(journalEntry{
				SessionID: session, Caller: caller, Kind: "reply", Text: reply, Incomplete: incomplete,
				MessageID: rpc.Params.Message.MessageID, StreamID: rpc.Params.Metadata.StreamID,
			}); err == nil {
				wrote = true
			}
		}
	}
	if wrote && strings.HasPrefix(caller, "cli:") {
		s.pushJournal(r.Context())
	}
}

func (s *Supervisor) isReady() bool {
	ready, _, _, _ := s.snapshot()
	return ready
}

func (s *Supervisor) snapshot() (ready bool, version, sum, bootErr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.procDone != nil {
		select {
		case <-s.procDone:
			s.ready = false
			if s.bootErr == "" {
				s.bootErr = "runtime exited"
			}
		default:
		}
	}
	return s.ready, s.version, s.sha, s.bootErr
}

func (s *Supervisor) listFiles(w http.ResponseWriter, r *http.Request) {
	root := filepath.Join(s.cfg.AgentDir, "workspace")
	var out []map[string]any
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
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
		s.putFile(w, r, root, full)
		return
	}
	f, err := openUnder(root, full)
	if err != nil {
		if errors.Is(err, errOutside) {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
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

func (s *Supervisor) putFile(w http.ResponseWriter, r *http.Request, root, full string) {
	if err := ensureParent(root, full); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := ensureParent(root, full); err != nil {
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
	cerr := tmp.Close()
	if err != nil || cerr != nil || n > fileCap {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Rename replaces a symlink entry. It does not follow one.
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
		if err != nil || info == nil || info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil || !under(root, resolved) {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		f, err := os.Open(resolved)
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
		if err := ensureParent(root, full); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := ensureParent(root, full); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		f, err := os.OpenFile(full, os.O_CREATE|os.O_TRUNC|os.O_WRONLY|syscall.O_NOFOLLOW, 0o644)
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

var errOutside = errors.New("outside workspace")

func openUnder(root, full string) (*os.File, error) {
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return nil, err
	}
	if !under(root, resolved) {
		return nil, errOutside
	}
	return os.Open(resolved)
}

func ensureParent(root, full string) error {
	cur := filepath.Dir(full)
	for {
		if _, err := os.Lstat(cur); err == nil {
			resolved, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return err
			}
			if resolved != root && !under(root, resolved) {
				return errOutside
			}
			return nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return errOutside
		}
		cur = parent
	}
}

func under(root, full string) bool {
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
