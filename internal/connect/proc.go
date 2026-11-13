package connect

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ProcIdle is how long a host MCP process stays up after the last call.
// The 512 MB control plane does not start these processes unless a command
// is configured. The adapter is the default.
const ProcIdle = 45 * time.Second

// Commands are optional host binaries. Empty means the in-process adapter.
type Commands struct {
	GitHub       []string
	DigitalOcean []string
	Idle         time.Duration
}

// FromEnv reads CASTBOT_MCP_GITHUB and CASTBOT_MCP_DIGITALOCEAN.
// They are unset on the small control plane, so the adapter is used.
func FromEnv() Commands {
	return Commands{
		GitHub:       fields(os.Getenv("CASTBOT_MCP_GITHUB")),
		DigitalOcean: fields(os.Getenv("CASTBOT_MCP_DIGITALOCEAN")),
		Idle:         ProcIdle,
	}
}

func fields(s string) []string {
	return strings.Fields(strings.TrimSpace(s))
}

// Session runs one official MCP process on the host, with the token only in
// that process. The guest never sees the child environment.
type Session struct {
	mu    sync.Mutex
	cmd   *exec.Cmd
	in    io.WriteCloser
	out   *bufio.Reader
	wait  chan struct{}
	idle  time.Duration
	timer *time.Timer
	argv  []string
	envk  string
}

// StartSession starts argv with a clean environment plus one token variable.
// The token is not retained on Session after the child is started.
func StartSession(argv []string, envKey, token string, idle time.Duration) (*Session, error) {
	if len(argv) == 0 || strings.TrimSpace(token) == "" || envKey == "" {
		return nil, errors.New("mcp command")
	}
	if idle <= 0 {
		idle = ProcIdle
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"LANG=C",
		envKey + "=" + token,
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := &Session{
		cmd: cmd, in: stdin, out: bufio.NewReader(stdout),
		wait: make(chan struct{}), idle: idle, argv: argv, envk: envKey,
	}
	go func() {
		_ = cmd.Wait()
		close(s.wait)
	}()
	s.bump()
	return s, nil
}

// Call writes one JSON-RPC message with Content-Length framing and reads one reply.
func (s *Session) Call(ctx context.Context, msg []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.wait:
		return nil, errors.New("mcp stopped")
	default:
	}
	s.bump()
	frame := "Content-Length: " + strconv.Itoa(len(msg)) + "\r\n\r\n" + string(msg)
	if _, err := io.WriteString(s.in, frame); err != nil {
		return nil, err
	}
	type result struct {
		b   []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		b, err := readFrame(s.out)
		ch <- result{b, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.wait:
		return nil, errors.New("mcp stopped")
	case got := <-ch:
		return got.b, got.err
	}
}

func (s *Session) bump() {
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(s.idle, func() { s.Stop() })
}

// Stop ends the child. It is safe to call more than once.
func (s *Session) Stop() {
	s.mu.Lock()
	cmd := s.cmd
	timer := s.timer
	s.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// Alive reports whether the child is still running.
func (s *Session) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.wait:
		return false
	default:
		return s.cmd != nil
	}
}

func readFrame(r *bufio.Reader) ([]byte, error) {
	var length int
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			n, err := strconv.Atoi(strings.TrimSpace(line[len("content-length:"):]))
			if err != nil || n < 0 || n > 1<<20 {
				return nil, errors.New("mcp frame")
			}
			length = n
		}
	}
	if length == 0 {
		return nil, errors.New("mcp frame")
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	if !json.Valid(buf) {
		return nil, errors.New("mcp frame")
	}
	return buf, nil
}

// EnvKey is the child variable for a connection. It is not a guest variable.
func EnvKey(connection string) string {
	switch connection {
	case GitHub:
		return "GITHUB_PERSONAL_ACCESS_TOKEN"
	case DigitalOcean:
		return "DIGITALOCEAN_API_TOKEN"
	default:
		return ""
	}
}

// Has reports whether a host process is configured for this connection.
func (p *Pool) Has(connection string) bool {
	if p == nil {
		return false
	}
	return len(p.cmds.Argv(connection)) > 0
}

// Argv returns the configured command for a connection.
func (c Commands) Argv(connection string) []string {
	switch connection {
	case GitHub:
		return c.GitHub
	case DigitalOcean:
		return c.DigitalOcean
	default:
		return nil
	}
}

// Pool reuses one session per connection and stops it when idle.
type Pool struct {
	mu       sync.Mutex
	cmds     Commands
	sessions map[string]*Session
}

// NewPool builds a pool. An empty command uses no process.
func NewPool(cmds Commands) *Pool {
	if cmds.Idle <= 0 {
		cmds.Idle = ProcIdle
	}
	return &Pool{cmds: cmds, sessions: map[string]*Session{}}
}

// RoundTrip sends rpc to the host process. The token is passed only into the child.
func (p *Pool) RoundTrip(ctx context.Context, connection, token string, rpc []byte) ([]byte, error) {
	argv := p.cmds.Argv(connection)
	if len(argv) == 0 {
		return nil, errors.New("no command")
	}
	p.mu.Lock()
	s := p.sessions[connection]
	if s != nil && !s.Alive() {
		s = nil
	}
	if s == nil {
		var err error
		s, err = StartSession(argv, EnvKey(connection), token, p.cmds.Idle)
		if err != nil {
			p.mu.Unlock()
			return nil, err
		}
		p.sessions[connection] = s
	}
	p.mu.Unlock()
	out, err := s.Call(ctx, rpc)
	if err != nil {
		s.Stop()
		return nil, err
	}
	if bytes.Contains(out, []byte(token)) {
		return []byte(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"redacted"}}`), nil
	}
	return out, nil
}
