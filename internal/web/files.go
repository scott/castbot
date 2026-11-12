package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

const (
	// fileCap matches castlet. Files can be up to 32 MB.
	fileCap = 32 << 20
	// previewCap is the largest text preview. Larger files are not inlined.
	previewCap = 256 << 10
	// uploadQuota is 2 GB of uploads per workspace.
	uploadQuota = 2 * 1024 * 1024 * 1024
	// lowDiskFree refuses every upload while the data volume is under 2 GB free.
	lowDiskFree = 2 * 1024 * 1024 * 1024

	lowSpaceLead = "The server is low on space."
	lowSpaceRest = "Upload is paused."
	lowSpaceCopy = lowSpaceLead + " " + lowSpaceRest
	tooLargeCopy = "Too large. Files can be up to 32 MB. Split it or share a link in the chat instead."
	quotaCopy    = "This workspace has reached its 2 GB upload limit."
	previewNote  = "Text files up to 256 KB preview here as plain text. Nothing in a file runs in castbot."
)

func isUploadPost(path string) bool {
	rest, ok := strings.CutPrefix(path, "/agents/")
	if !ok {
		return false
	}
	_, action, ok := strings.Cut(rest, "/")
	return ok && action == "uploads"
}

// SetDiskFree replaces the free-space check. Tests use it. Nil restores the host check.
func (s *Server) SetDiskFree(fn func(string) (uint64, error)) {
	s.fileMu.Lock()
	s.diskFreeFn = fn
	s.fileMu.Unlock()
}

// SetUploadHold pauses an upload after it takes the per-session slot.
// Tests use it to prove a second file waits.
func (s *Server) SetUploadHold(ch <-chan struct{}) {
	s.fileMu.Lock()
	s.uploadHold = ch
	s.fileMu.Unlock()
}

// UploadLog is the acquire and finish order of uploads. Tests use it.
func (s *Server) UploadLog() []string {
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	return append([]string(nil), s.uploadLog...)
}

func (s *Server) noteUpload(phase, name string) {
	s.fileMu.Lock()
	s.uploadLog = append(s.uploadLog, phase+" "+name)
	s.fileMu.Unlock()
}

func (s *Server) diskFree(path string) (uint64, error) {
	s.fileMu.Lock()
	fn := s.diskFreeFn
	s.fileMu.Unlock()
	if fn != nil {
		return fn(path)
	}
	return statFree(path)
}

func statFree(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

func (s *Server) dataDirPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dataDir
}

func (s *Server) diskLow() bool {
	dir := s.dataDirPath()
	if dir == "" {
		return false
	}
	free, err := s.diskFree(dir)
	if err != nil {
		return false
	}
	return free < lowDiskFree
}

func (s *Server) acquireUpload(ctxDone <-chan struct{}, key string) (func(), bool) {
	s.fileMu.Lock()
	if s.uploadSlot == nil {
		s.uploadSlot = map[string]chan struct{}{}
	}
	ch := s.uploadSlot[key]
	if ch == nil {
		ch = make(chan struct{}, 1)
		s.uploadSlot[key] = ch
	}
	s.fileMu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, true
	case <-ctxDone:
		return nil, false
	}
}

func (s *Server) postUpload(w http.ResponseWriter, r *http.Request) {
	user, sess, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	name, ok := cleanUploadName(r.Header.Get("X-File-Name"))
	if !ok {
		writeText(w, http.StatusBadRequest, "Bad request\n")
		return
	}
	// Quota and free disk are decided before any byte is stored.
	if s.diskLow() {
		writeText(w, http.StatusConflict, lowSpaceCopy+"\n")
		return
	}
	if r.ContentLength < 0 {
		writeText(w, http.StatusBadRequest, "Bad request\n")
		return
	}
	if r.ContentLength > fileCap {
		writeText(w, http.StatusRequestEntityTooLarge, tooLargeCopy+"\n")
		return
	}
	usage, err := s.store.UploadUsage(r.Context(), user.ID, agent.WorkspaceID)
	if err != nil {
		writeText(w, http.StatusInternalServerError, "Error\n")
		return
	}
	if usage+r.ContentLength > uploadQuota {
		writeText(w, http.StatusConflict, quotaCopy+"\n")
		return
	}
	release, ok := s.acquireUpload(r.Context().Done(), sess.ID)
	if !ok {
		writeText(w, http.StatusBadRequest, stoppedCopy(0, r.ContentLength)+"\n")
		return
	}
	s.noteUpload("acquired", name)
	s.fileMu.Lock()
	hold := s.uploadHold
	s.fileMu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-r.Context().Done():
			release()
			writeText(w, http.StatusBadRequest, stoppedCopy(0, r.ContentLength)+"\n")
			return
		}
	}
	defer release()

	dir := s.dataDirPath()
	if dir == "" {
		writeText(w, http.StatusInternalServerError, "Error\n")
		return
	}
	blobDir := filepath.Join(dir, "files")
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		writeText(w, http.StatusInternalServerError, "Error\n")
		return
	}
	tmp, err := os.CreateTemp(blobDir, ".part-*")
	if err != nil {
		writeText(w, http.StatusInternalServerError, "Error\n")
		return
	}
	tmpName := tmp.Name()
	kept := false
	defer func() {
		tmp.Close()
		if !kept {
			os.Remove(tmpName)
		}
	}()

	sum := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, sum), io.LimitReader(r.Body, fileCap+1))
	if copyErr != nil || n != r.ContentLength {
		writeText(w, http.StatusBadRequest, stoppedCopy(n, r.ContentLength)+"\n")
		return
	}
	if n > fileCap {
		writeText(w, http.StatusRequestEntityTooLarge, tooLargeCopy+"\n")
		return
	}
	if usage+n > uploadQuota {
		writeText(w, http.StatusConflict, quotaCopy+"\n")
		return
	}
	if err := tmp.Close(); err != nil {
		writeText(w, http.StatusInternalServerError, "Error\n")
		return
	}
	digest := hex.EncodeToString(sum.Sum(nil))
	dest := filepath.Join(blobDir, digest)
	if _, err := os.Stat(dest); err == nil {
		os.Remove(tmpName)
	} else if err := os.Rename(tmpName, dest); err != nil {
		writeText(w, http.StatusInternalServerError, "Error\n")
		return
	}
	kept = true

	taken, err := s.store.UploadNames(r.Context(), user.ID, agent.ID)
	if err != nil {
		writeText(w, http.StatusInternalServerError, "Error\n")
		return
	}
	final := nextUploadName(taken, name)
	guestPath := "uploads/" + final
	if !safeGuestPath(guestPath) {
		os.Remove(dest)
		writeText(w, http.StatusBadRequest, "Bad request\n")
		return
	}
	status := "held"
	if !s.replacing(r.Context(), user.ID, agent.ID) {
		if s.writeUpload(r, user.ID, agent.ID, guestPath, dest) {
			status = "copied"
		}
	}
	row, err := s.store.InsertUpload(r.Context(), user.ID, agent.ID, r.Header.Get("X-Thread-ID"), final, mimeByName(final), digest, "workspace/"+guestPath, status, n)
	if err != nil {
		writeText(w, http.StatusInternalServerError, "Error\n")
		return
	}
	s.noteUpload("end", final)
	note := "Copied to workspace/uploads. " + agent.Name + " is told the path in your next message."
	if status != "copied" {
		note = "Saved in castbot. Copied into the machine when " + agent.Name + " wakes."
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"id": row.ID, "name": final, "path": row.Path, "status": status,
		"line": pathLine(agent.Name, final), "note": note,
	})
}

// writeUpload copies a stored blob into a running guest. It does not wake.
func (s *Server) writeUpload(r *http.Request, userID, agentID, guestPath, blob string) bool {
	h, _, _ := s.hostConfig()
	if h == nil {
		return false
	}
	vm, err := s.store.LiveVM(r.Context(), userID, agentID)
	if err != nil || vm.State != "running" || vm.MicroVMID == "" || vm.MicroVMID == "pending" {
		return false
	}
	endpoint, token, err := s.store.VMCredentials(r.Context(), userID, agentID)
	if err != nil || endpoint == "" || token == "" {
		return false
	}
	f, err := os.Open(blob)
	if err != nil {
		return false
	}
	defer f.Close()
	return h.WriteWorkspace(r.Context(), endpoint, token, guestPath, f) == nil
}

func (s *Server) copyHeldUploads(ctx context.Context, userID, agentID string) {
	if s.replacing(ctx, userID, agentID) {
		return
	}
	h, _, _ := s.hostConfig()
	if h == nil {
		return
	}
	vm, err := s.store.LiveVM(ctx, userID, agentID)
	if err != nil || vm.State != "running" || vm.MicroVMID == "" || vm.MicroVMID == "pending" {
		return
	}
	endpoint, token, err := s.store.VMCredentials(ctx, userID, agentID)
	if err != nil || endpoint == "" || token == "" {
		return
	}
	held, err := s.store.ListHeldUploads(ctx, userID, agentID)
	if err != nil {
		return
	}
	for _, row := range held {
		guestPath := "uploads/" + row.Name
		if !safeGuestPath(guestPath) {
			continue
		}
		blob, ok := s.blobPath(row.SHA256)
		if !ok {
			continue
		}
		f, err := os.Open(blob)
		if err != nil {
			continue
		}
		err = h.WriteWorkspace(ctx, endpoint, token, guestPath, f)
		f.Close()
		if err != nil {
			continue
		}
		_ = s.store.MarkUploadStatus(ctx, userID, row.ID, "copied")
	}
}

func (s *Server) blobPath(sum string) (string, bool) {
	if !validSHA(sum) {
		return "", false
	}
	dir := s.dataDirPath()
	if dir == "" {
		return "", false
	}
	return filepath.Join(dir, "files", sum), true
}

func (s *Server) downloadFile(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	if row, err := s.store.GetFile(r.Context(), user.ID, r.PathValue("fileID")); err == nil {
		if row.AgentID != agent.ID || row.Source != "upload" {
			writeText(w, http.StatusNotFound, "Not found\n")
			return
		}
		s.streamBlob(w, row.Name, row.SHA256)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeText(w, http.StatusInternalServerError, "Error\n")
		return
	}
	cached, err := s.store.GetCached(r.Context(), user.ID, r.PathValue("fileID"))
	if s.missing(w, err) {
		return
	}
	if cached.AgentID != agent.ID {
		writeText(w, http.StatusNotFound, "Not found\n")
		return
	}
	if s.replacing(r.Context(), user.ID, agent.ID) {
		writeText(w, http.StatusConflict, replaceSentence+"\n")
		return
	}
	rc, err := s.openWorkspace(r, user.ID, agent.ID, cached.Name)
	if err != nil {
		if errors.Is(err, errReplace) {
			writeText(w, http.StatusConflict, replaceSentence+"\n")
			return
		}
		writeText(w, http.StatusBadGateway, "The machine is paused.\n")
		return
	}
	defer rc.Close()
	s.streamOut(w, filepath.Base(cached.Name), rc)
}

func (s *Server) previewFile(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Cache-Control", "no-store")
	if row, err := s.store.GetFile(r.Context(), user.ID, r.PathValue("fileID")); err == nil {
		if row.AgentID != agent.ID {
			writeText(w, http.StatusNotFound, "Not found\n")
			return
		}
		if !previewKind(row.Name) || row.Size > previewCap {
			writeText(w, http.StatusUnsupportedMediaType, previewNote+"\n")
			return
		}
		blob, ok := s.blobPath(row.SHA256)
		if !ok {
			writeText(w, http.StatusNotFound, "Not found\n")
			return
		}
		f, err := os.Open(blob)
		if err != nil {
			writeText(w, http.StatusNotFound, "Not found\n")
			return
		}
		defer f.Close()
		buf, err := io.ReadAll(io.LimitReader(f, previewCap+1))
		if err != nil || int64(len(buf)) > previewCap {
			writeText(w, http.StatusUnsupportedMediaType, previewNote+"\n")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeText(w, http.StatusInternalServerError, "Error\n")
		return
	}
	cached, err := s.store.GetCached(r.Context(), user.ID, r.PathValue("fileID"))
	if s.missing(w, err) {
		return
	}
	if cached.AgentID != agent.ID || !previewKind(cached.Name) || cached.Size > previewCap {
		writeText(w, http.StatusUnsupportedMediaType, previewNote+"\n")
		return
	}
	if s.workspacePaused(r, user.ID, agent.ID) || s.replacing(r.Context(), user.ID, agent.ID) {
		writeText(w, http.StatusConflict, "The machine is paused.\n")
		return
	}
	rc, err := s.openWorkspace(r, user.ID, agent.ID, cached.Name)
	if err != nil {
		writeText(w, http.StatusBadGateway, "The machine is paused.\n")
		return
	}
	defer rc.Close()
	buf, err := io.ReadAll(io.LimitReader(rc, previewCap+1))
	if err != nil || int64(len(buf)) > previewCap {
		writeText(w, http.StatusUnsupportedMediaType, previewNote+"\n")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf)
}

func (s *Server) streamBlob(w http.ResponseWriter, name, sum string) {
	blob, ok := s.blobPath(sum)
	if !ok {
		writeText(w, http.StatusNotFound, "Not found\n")
		return
	}
	f, err := os.Open(blob)
	if err != nil {
		writeText(w, http.StatusNotFound, "Not found\n")
		return
	}
	defer f.Close()
	s.streamOut(w, name, f)
}

// streamOut copies a file to the response without buffering the whole body.
func (s *Server) streamOut(w http.ResponseWriter, name string, r io.Reader) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", attachmentDisposition(name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, io.LimitReader(r, fileCap+1))
}

var errReplace = errors.New("replacing")

// openWorkspace returns a guest file. A paused machine is woken first, using
// the same wake events as a chat run. Switch this to s.wakeForWork when that
// helper exists.
func (s *Server) openWorkspace(r *http.Request, userID, agentID, name string) (io.ReadCloser, error) {
	if s.replacing(r.Context(), userID, agentID) {
		return nil, errReplace
	}
	if !safeGuestPath(name) {
		return nil, host.ErrGuest
	}
	h, _, _ := s.hostConfig()
	if h == nil {
		return nil, host.ErrGuest
	}
	vm, err := s.store.LiveVM(r.Context(), userID, agentID)
	if err != nil {
		return nil, err
	}
	endpoint, token, err := s.store.VMCredentials(r.Context(), userID, agentID)
	if err != nil {
		return nil, err
	}
	if vm.State == "paused" && vm.MicroVMID != "" && vm.MicroVMID != "pending" {
		s.noteVM(r.Context(), userID, agentID, vm.MicroVMID, store.EventWakeRequested, 0)
		wakeAt := time.Now()
		got, werr := h.Wake(r.Context(), vm.MicroVMID)
		if werr != nil {
			return nil, werr
		}
		if err := s.store.ApplyVMState(r.Context(), userID, agentID, got.State, got.Endpoint, ""); err != nil {
			return nil, err
		}
		if got.State == "running" {
			s.noteVM(r.Context(), userID, agentID, vm.MicroVMID, store.EventWakeRunning, time.Since(wakeAt))
		}
		if got.Endpoint != "" {
			endpoint = got.Endpoint
		}
		if token != "" && endpoint != "" {
			if perr := h.Probe(r.Context(), endpoint, token); perr == nil {
				s.noteVM(r.Context(), userID, agentID, vm.MicroVMID, store.EventWakeReady, time.Since(wakeAt))
			}
		}
		if got.State != "running" {
			return nil, errors.New("not running")
		}
		s.fireAfterRun(r.Context(), userID, agentID)
		endpoint, token, err = s.store.VMCredentials(r.Context(), userID, agentID)
		if err != nil {
			return nil, err
		}
	}
	if vm.State != "running" && vm.State != "paused" {
		return nil, errors.New("not running")
	}
	if endpoint == "" || token == "" {
		return nil, host.ErrGuest
	}
	return h.ReadWorkspace(r.Context(), endpoint, token, name)
}

func (s *Server) workspacePaused(r *http.Request, userID, agentID string) bool {
	vm, err := s.store.LiveVM(r.Context(), userID, agentID)
	return err == nil && vm.State == "paused"
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	p, err := s.shell(r, user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.render(w, http.StatusOK, "searchResults", p)
}

func cleanUploadName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") || strings.Contains(name, "..") {
		return "", false
	}
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	if name == "" {
		return "", false
	}
	if len(name) > 180 {
		name = name[:180]
	}
	return name, true
}

func nextUploadName(taken map[string]struct{}, name string) string {
	if _, ok := taken[name]; !ok {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for n := 2; n < 100; n++ {
		cand := base + " (" + strconv.Itoa(n) + ")" + ext
		if _, ok := taken[cand]; !ok {
			return cand
		}
	}
	return name
}

func pathLine(agentName, fileName string) string {
	return agentName + " got the file at workspace/uploads/" + fileName + ", named in your message."
}

func stoppedCopy(got, total int64) string {
	return "Stopped at " + formatMB(got) + " of " + formatMB(total) + " MB: the connection dropped. Nothing was saved."
}

func formatMB(n int64) string {
	if n < 0 {
		n = 0
	}
	return strconv.FormatFloat(float64(n)/(1024*1024), 'f', 1, 64)
}

func validSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

func mimeByName(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".md", ".csv", ".log":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".html", ".htm":
		return "text/html"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".pdf":
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}

func previewKind(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".md", ".csv", ".log", ".json", ".html", ".htm", ".xml", ".yaml", ".yml", ".css", ".js", ".go", ".py", ".toml":
		return true
	default:
		return false
	}
}

func safeGuestPath(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, `\`) || strings.Contains(name, "..") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
