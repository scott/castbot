package castlet

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/scott/castbot/internal/runtime"
)

type callerKey struct{}

type cliToken struct {
	ID      string `json:"id"`
	Hash    string `json:"hash"`
	Revoked bool   `json:"revoked"`
}

type journalEntry struct {
	Seq        int    `json:"seq"`
	At         string `json:"at"`
	SessionID  string `json:"session_id"`
	Caller     string `json:"caller"`
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	Tool       string `json:"tool"`
	RequestID  string `json:"request_id"`
	Decision   string `json:"decision"`
	MessageID  string `json:"message_id,omitempty"`
	StreamID   string `json:"stream_id,omitempty"`
	Incomplete bool   `json:"incomplete"`
}

func callerOf(r *http.Request) string {
	v, _ := r.Context().Value(callerKey{}).(string)
	return v
}

func (s *Supervisor) stateDir() string {
	return filepath.Join(s.cfg.AgentDir, ".castlet")
}

const (
	cliCacheTTL = 30 * time.Second
	wakeSlack   = time.Second
)

// monoEpoch is the monotonic base. time.Since on it stops while the machine is paused.
var monoEpoch = time.Now()

// refreshCLITokens reports whether a CLI token may be checked against the list.
// A list newer than cliCacheTTL on both the wall clock and the monotonic clock
// is reused. When either clock is older and the control plane cannot be reached,
// the token is rejected.
func (s *Supervisor) refreshCLITokens(ctx context.Context) bool {
	if s.cfg.ControlURL == "" || s.cfg.AgentID == "" {
		s.ensureTokens()
		return true
	}
	if s.tokenCacheFresh() {
		return true
	}
	s.mu.Lock()
	token := s.token
	s.mu.Unlock()
	if token == "" {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.api("/cli-tokens"), nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := s.do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false
	}
	var body struct {
		Tokens []cliToken `json:"tokens"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
		return false
	}
	if !cleanTokens(body.Tokens) {
		return false
	}
	return s.replaceTokens(body.Tokens) == nil
}

func (s *Supervisor) noteCLIUse(ctx context.Context, id string) {
	if s.cfg.ControlURL == "" || s.cfg.AgentID == "" || id == "" {
		return
	}
	s.mu.Lock()
	token := s.token
	s.mu.Unlock()
	if token == "" {
		return
	}
	raw, err := json.Marshal(map[string]string{"id": id})
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.api("/cli-used"), bytes.NewReader(raw))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := s.do(req)
	if err != nil {
		return
	}
	res.Body.Close()
}

func cleanTokens(list []cliToken) bool {
	seen := map[string]bool{}
	for _, t := range list {
		if len(t.ID) != 32 || len(t.Hash) != 64 || seen[t.ID] {
			return false
		}
		if _, err := hex.DecodeString(t.ID); err != nil {
			return false
		}
		if _, err := hex.DecodeString(t.Hash); err != nil {
			return false
		}
		seen[t.ID] = true
	}
	return true
}

func (s *Supervisor) replaceTokens(list []cliToken) error {
	dir := s.stateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "cli-tokens.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	wall := s.wallClock()
	mono := s.monoClock()
	s.cliMu.Lock()
	s.cliTokens = append([]cliToken(nil), list...)
	s.cliLoaded = true
	s.cliWall = wall
	s.cliMono = mono
	s.cliMonoSet = true
	s.cliMu.Unlock()
	return nil
}

func (s *Supervisor) wallClock() time.Time {
	if s.wallNow != nil {
		return s.wallNow().Round(0)
	}
	return time.Now().Round(0)
}

func (s *Supervisor) monoClock() time.Duration {
	if s.monoNow != nil {
		return s.monoNow()
	}
	return time.Since(monoEpoch)
}

// observeClock drops the token list when the wall clock jumps ahead of the
// time the machine was running, which is what a wake looks like.
func (s *Supervisor) observeClock() {
	wall := s.wallClock()
	mono := s.monoClock()
	s.cliMu.Lock()
	s.noteWakeLocked(wall, mono)
	s.cliMu.Unlock()
}

func (s *Supervisor) noteWakeLocked(wall time.Time, mono time.Duration) {
	if s.clockInit {
		wallGap := wall.Sub(s.clockWall)
		monoGap := mono - s.clockMono
		if wallGap < 0 || wallGap > monoGap+wakeSlack {
			s.dropCLICacheLocked()
		}
	}
	s.clockWall = wall
	s.clockMono = mono
	s.clockInit = true
}

func (s *Supervisor) dropCLICacheLocked() {
	s.cliTokens = nil
	s.cliLoaded = false
	s.cliWall = time.Time{}
	s.cliMono = 0
	s.cliMonoSet = false
}

func (s *Supervisor) tokenCacheFresh() bool {
	wall := s.wallClock()
	mono := s.monoClock()
	s.cliMu.Lock()
	defer s.cliMu.Unlock()
	s.noteWakeLocked(wall, mono)
	return s.cliLoaded && cliCacheFresh(s.cliWall, wall, s.cliMono, mono, s.cliMonoSet)
}

func cliCacheFresh(storedWall, nowWall time.Time, storedMono, nowMono time.Duration, monoSet bool) bool {
	if storedWall.IsZero() || !monoSet {
		return false
	}
	wallAge := nowWall.Round(0).Sub(storedWall.Round(0))
	monoAge := nowMono - storedMono
	return wallAge >= 0 && wallAge < cliCacheTTL && monoAge >= 0 && monoAge < cliCacheTTL
}

func (s *Supervisor) ensureTokens() {
	s.cliMu.Lock()
	defer s.cliMu.Unlock()
	if s.cliLoaded {
		return
	}
	raw, err := os.ReadFile(filepath.Join(s.stateDir(), "cli-tokens.json"))
	if err == nil {
		var list []cliToken
		if json.Unmarshal(raw, &list) == nil && cleanTokens(list) {
			s.cliTokens = list
		}
	}
	s.cliLoaded = true
}

func (s *Supervisor) matchCLI(plain string) (id string, revoked bool) {
	s.ensureTokens()
	sum := sha256.Sum256([]byte(plain))
	got := hex.EncodeToString(sum[:])
	s.cliMu.Lock()
	defer s.cliMu.Unlock()
	for _, t := range s.cliTokens {
		if subtle.ConstantTimeCompare([]byte(got), []byte(t.Hash)) == 1 {
			return t.ID, t.Revoked
		}
	}
	return "", false
}

func (s *Supervisor) serveCLITokens(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tokens []cliToken `json:"tokens"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || !cleanTokens(body.Tokens) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.replaceTokens(body.Tokens); err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Supervisor) serveJournal(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	entries := s.journalAfter(after)
	if entries == nil {
		entries = []journalEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"entries": entries})
}

func (s *Supervisor) appendJournal(e journalEntry) (journalEntry, error) {
	s.journalMu.Lock()
	defer s.journalMu.Unlock()
	dir := s.stateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return e, err
	}
	if err := s.loadJournalLocked(); err != nil {
		return e, err
	}
	e.Seq = s.journalNext
	if e.At == "" {
		e.At = time.Now().UTC().Format(time.RFC3339)
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return e, err
	}
	line := append(raw, '\n')
	path := filepath.Join(dir, "journal.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return e, err
	}
	_, werr := f.Write(line)
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil {
		return e, werr
	}
	if serr != nil {
		return e, serr
	}
	if cerr != nil {
		return e, cerr
	}
	s.journalOff[e.Seq] = s.journalEnd
	s.journalEnd += int64(len(line))
	s.journalNext++
	s.rememberPermLocked(e)
	if err := writeSeq(dir, s.journalNext); err != nil {
		return e, err
	}
	return e, nil
}

func writeSeq(dir string, next int) error {
	path := filepath.Join(dir, "journal.seq")
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(strconv.Itoa(next))); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	f, err = os.Open(path)
	if err != nil {
		return err
	}
	err = f.Sync()
	cerr := f.Close()
	if err != nil {
		return err
	}
	if cerr != nil {
		return cerr
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	cerr = d.Close()
	if err != nil {
		return err
	}
	return cerr
}

func (s *Supervisor) loadJournalLocked() error {
	if s.journalReady {
		return nil
	}
	s.journalOff = map[int]int64{}
	s.journalNext = 1
	s.journalEnd = 0
	if s.permSession == nil {
		s.permSession = map[string]string{}
	}
	path := filepath.Join(s.stateDir(), "journal.jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.readSeqLocked()
			s.journalReady = true
			return nil
		}
		return err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			var prev journalEntry
			if json.Unmarshal(trimmed, &prev) == nil && prev.Seq > 0 {
				s.journalOff[prev.Seq] = s.journalEnd
				if prev.Seq+1 > s.journalNext {
					s.journalNext = prev.Seq + 1
				}
				s.rememberPermLocked(prev)
			}
		}
		s.journalEnd += int64(len(line))
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	s.readSeqLocked()
	s.journalReady = true
	return nil
}

func (s *Supervisor) readSeqLocked() {
	raw, err := os.ReadFile(filepath.Join(s.stateDir(), "journal.seq"))
	if err != nil {
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || n < s.journalNext {
		return
	}
	s.journalNext = n
}

func (s *Supervisor) rememberPermLocked(e journalEntry) {
	if e.Kind != "permission" || e.RequestID == "" || e.SessionID == "" {
		return
	}
	if s.permSession == nil {
		s.permSession = map[string]string{}
	}
	s.permSession[e.RequestID] = e.SessionID
}

func (s *Supervisor) sessionForPerm(id string) string {
	if id == "" {
		return ""
	}
	s.journalMu.Lock()
	defer s.journalMu.Unlock()
	if err := s.loadJournalLocked(); err != nil {
		return ""
	}
	return s.permSession[id]
}

func (s *Supervisor) journalAfter(after int) []journalEntry {
	s.journalMu.Lock()
	defer s.journalMu.Unlock()
	if err := s.loadJournalLocked(); err != nil {
		return nil
	}
	start := int64(0)
	if after > 0 {
		if off, ok := s.journalOff[after+1]; ok {
			start = off
		} else {
			found := false
			var best int64
			for seq, off := range s.journalOff {
				if seq > after && (!found || off < best) {
					best = off
					found = true
				}
			}
			if !found {
				return nil
			}
			start = best
		}
	}
	f, err := os.Open(filepath.Join(s.stateDir(), "journal.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	r := bufio.NewReader(f)
	var out []journalEntry
	for len(out) < 500 {
		line, err := r.ReadBytes('\n')
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			var e journalEntry
			if json.Unmarshal(trimmed, &e) == nil && e.Seq > after {
				out = append(out, e)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
	}
	return out
}

func (s *Supervisor) readAck() int {
	raw, err := os.ReadFile(filepath.Join(s.stateDir(), "ack"))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return n
}

func (s *Supervisor) writeAck(n int) {
	dir := s.stateDir()
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "ack"), []byte(strconv.Itoa(n)), 0o600)
}

func (s *Supervisor) pushJournal(ctx context.Context) {
	if s.cfg.ControlURL == "" || s.cfg.AgentID == "" {
		return
	}
	ack := s.readAck()
	entries := s.journalAfter(ack)
	if len(entries) == 0 {
		return
	}
	if len(entries) > 200 {
		entries = entries[:200]
	}
	body, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		return
	}
	s.mu.Lock()
	token := s.token
	s.mu.Unlock()
	if token == "" {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.api("/journal"), bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := s.do(req)
	if err != nil {
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return
	}
	var out struct {
		Last int `json:"last"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&out) != nil {
		return
	}
	if out.Last > ack {
		s.writeAck(out.Last)
	}
}

type turnCapture struct {
	buf     bytes.Buffer
	text    strings.Builder
	sawDone bool
	perms   []journalEntry
}

func (c *turnCapture) feed(p []byte) {
	c.buf.Write(p)
	c.drain(false)
}

func (c *turnCapture) finish() {
	c.drain(true)
}

func (c *turnCapture) drain(final bool) {
	raw := c.buf.Bytes()
	for {
		idx := bytes.Index(raw, []byte("\n\n"))
		if idx < 0 {
			break
		}
		c.frame(raw[:idx])
		raw = raw[idx+2:]
	}
	c.buf.Reset()
	if final && len(bytes.TrimSpace(raw)) > 0 {
		c.frame(raw)
		return
	}
	c.buf.Write(raw)
}

func (c *turnCapture) frame(event []byte) {
	var data []string
	for _, line := range bytes.Split(event, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("data:")) {
			v := bytes.TrimPrefix(line, []byte("data:"))
			if len(v) > 0 && v[0] == ' ' {
				v = v[1:]
			}
			data = append(data, string(v))
		}
	}
	if len(data) == 0 {
		trimmed := bytes.TrimSpace(event)
		if len(trimmed) > 0 && trimmed[0] == '{' {
			c.parse(string(trimmed))
		}
		return
	}
	c.parse(strings.Join(data, "\n"))
}

func (c *turnCapture) parse(payload string) {
	parsed, err := runtime.ParseData(payload)
	if err != nil || parsed.Ignored {
		return
	}
	if parsed.Done {
		c.sawDone = true
		return
	}
	c.text.WriteString(parsed.Text)
	if len(parsed.Perm) > 0 {
		id, tool := permFields(parsed.Perm)
		c.perms = append(c.perms, journalEntry{Kind: "permission", Tool: tool, RequestID: id})
	}
}

func permFields(raw json.RawMessage) (id, tool string) {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return "", ""
	}
	id = strField(m, "id", "requestId", "request_id")
	tool = strField(m, "tool", "name", "toolName")
	return id, tool
}

func strField(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, ok := m[key].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

type rpcBody struct {
	Method string `json:"method"`
	Params struct {
		ID       string `json:"id"`
		Approved *bool  `json:"approved"`
		Message  struct {
			MessageID string `json:"messageId"`
			Parts     []struct {
				Kind string `json:"kind"`
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"message"`
		Metadata struct {
			SessionID string `json:"sessionId"`
			StreamID  string `json:"streamId"`
		} `json:"metadata"`
	} `json:"params"`
}

func parseRPC(body []byte) rpcBody {
	var req rpcBody
	_ = json.Unmarshal(body, &req)
	return req
}

func messageText(req rpcBody) string {
	var b strings.Builder
	for _, part := range req.Params.Message.Parts {
		if part.Kind == "text" {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}
