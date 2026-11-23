//go:build linux

package tooliso

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	frameIn   = byte(1)
	frameEOF  = byte(2)
	frameOut  = byte(3)
	frameErr  = byte(4)
	frameExit = byte(5)
	frameSig  = byte(6)

	maxFrame = 1 << 20
)

// DefaultToolSock is where the runtime asks for a command.
const DefaultToolSock = "/run/castbot-tool.sock"

type toolRequest struct {
	Argv []string `json:"argv"`
	Env  []string `json:"env"`
	Dir  string   `json:"dir"`
	File bool     `json:"file"`
}

var (
	serveOnce sync.Once
	serveErr  error
)

// ToolSock is the socket path. CASTBOT_TOOL_SOCK overrides the default.
func ToolSock() string {
	if v := os.Getenv("CASTBOT_TOOL_SOCK"); v != "" {
		return v
	}
	return DefaultToolSock
}

// EnsureServe listens for command requests when this process is root.
// A second call reuses the first listener. A non-root process returns an error.
func EnsureServe(ctx context.Context) error {
	if os.Geteuid() != 0 {
		return errors.New("tool server")
	}
	serveOnce.Do(func() {
		serveErr = serve(ctx)
	})
	return serveErr
}

func serve(ctx context.Context) error {
	path := ToolSock()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return err
	}
	if cred, err := credential(RuntimeUser); err == nil {
		_ = os.Chown(path, 0, int(cred.Gid))
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleTool(conn)
		}
	}()
	return nil
}

func peerAllowed(conn *net.UnixConn) bool {
	raw, err := conn.SyscallConn()
	if err != nil {
		return false
	}
	var ucred *unix.Ucred
	var uerr error
	if err := raw.Control(func(fd uintptr) {
		ucred, uerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || uerr != nil || ucred == nil {
		return false
	}
	cred, err := credential(RuntimeUser)
	if err != nil {
		return false
	}
	return ucred.Uid == cred.Uid
}

func handleTool(conn net.Conn) {
	defer conn.Close()
	uc, ok := conn.(*net.UnixConn)
	if !ok || !peerAllowed(uc) {
		_ = writeExit(conn, 126)
		return
	}
	br := bufio.NewReaderSize(conn, 64<<10)
	line, err := br.ReadBytes('\n')
	if err != nil || len(line) == 0 || len(line) > maxFrame {
		_ = writeExit(conn, 126)
		return
	}
	var req toolRequest
	if json.Unmarshal(line, &req) != nil || !requestOK(req) {
		_ = writeExit(conn, 126)
		return
	}
	run, err := startRequest(req)
	if err != nil || run.cmd.Process == nil {
		_ = writeExit(conn, 126)
		return
	}
	var writeMu sync.Mutex
	send := func(kind byte, payload []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return writeFrame(conn, kind, payload)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		copyPipe(run.stdout, frameOut, send)
	}()
	go func() {
		defer wg.Done()
		copyPipe(run.stderr, frameErr, send)
	}()
	go func() {
		err := run.cmd.Wait()
		wg.Wait()
		var buf [4]byte
		code := exitCode(err)
		if code < 0 {
			code = 128 + int(syscall.SIGKILL)
		}
		binary.BigEndian.PutUint32(buf[:], uint32(code))
		_ = send(frameExit, buf[:])
	}()
	for {
		kind, payload, err := readFrame(br)
		if err != nil {
			stopGroup(run.cmd.Process.Pid)
			return
		}
		switch kind {
		case frameIn:
			_, _ = run.stdin.Write(payload)
		case frameEOF:
			_ = run.stdin.Close()
		case frameSig:
			stopGroup(run.cmd.Process.Pid)
		}
	}
}

type runningCmd struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
}

func helperBin() string {
	if v := os.Getenv("CASTBOT_TOOLRUN"); v != "" {
		return v
	}
	return "/usr/local/bin/castbot-toolrun"
}

func startRequest(req toolRequest) (runningCmd, error) {
	self := helperBin()
	if self == "" {
		return runningCmd{}, errors.New("command")
	}
	var cmd *exec.Cmd
	if req.File {
		cmd = exec.Command(self, append([]string{"--file"}, req.Argv...)...)
		cmd.Env = FileEnv(req.Env)
	} else {
		env := CommandEnv(req.Env)
		path := Resolve(env, req.Argv[0])
		if path == "" {
			return runningCmd{}, errors.New("command")
		}
		args := append([]string{"--dropped", "--", path}, req.Argv[1:]...)
		cmd = exec.Command(self, args...)
		cmd.Env = env
		if dir, ok := cleanDir(req.Dir); ok {
			cmd.Dir = dir
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return runningCmd{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return runningCmd{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		stdout.Close()
		return runningCmd{}, err
	}
	if err := ApplyTool(cmd); err != nil {
		stdin.Close()
		stdout.Close()
		stderr.Close()
		return runningCmd{}, err
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		stderr.Close()
		return runningCmd{}, err
	}
	return runningCmd{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

func requestOK(req toolRequest) bool {
	if len(req.Argv) == 0 || len(req.Argv) > 64 {
		return false
	}
	if req.File && len(req.Argv) > 8 {
		return false
	}
	for _, arg := range req.Argv {
		if arg == "" || len(arg) > 1<<16 || containsNUL(arg) {
			return false
		}
	}
	if len(req.Env) > 512 {
		return false
	}
	for _, item := range req.Env {
		if len(item) > 1<<16 || containsNUL(item) {
			return false
		}
	}
	if req.Dir != "" && !dirOK(req.Dir) {
		return false
	}
	return true
}

func dirOK(dir string) bool {
	if !strings.HasPrefix(dir, "/") || len(dir) > 4096 || containsNUL(dir) {
		return false
	}
	if strings.Contains(dir, "/../") || strings.HasSuffix(dir, "/..") || dir == "/.." {
		return false
	}
	return true
}

func cleanDir(dir string) (string, bool) {
	if !dirOK(dir) {
		return "", false
	}
	return filepath.Clean(dir), true
}

func containsNUL(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return true
		}
	}
	return false
}

func copyPipe(r io.Reader, kind byte, send func(byte, []byte) error) {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if send(kind, buf[:n]) != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func stopGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	time.Sleep(200 * time.Millisecond)
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 126
}

func writeExit(w io.Writer, code int) error {
	var buf [4]byte
	if code < 0 {
		code = 126
	}
	binary.BigEndian.PutUint32(buf[:], uint32(code))
	return writeFrame(w, frameExit, buf[:])
}

func writeFrame(w io.Writer, kind byte, payload []byte) error {
	if len(payload) > maxFrame {
		return errors.New("frame")
	}
	buf := make([]byte, 5+len(payload))
	buf[0] = kind
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(payload)))
	copy(buf[5:], payload)
	_, err := w.Write(buf)
	return err
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxFrame {
		return 0, nil, errors.New("frame")
	}
	buf := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, buf); err != nil {
			return 0, nil, err
		}
	}
	return hdr[0], buf, nil
}

// Proxy asks the supervisor to run args and copies the command's streams.
// file is true for a path read. The command always runs as the tool account.
func Proxy(file bool, args []string) int {
	if len(args) == 0 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "command refused")
		return 126
	}
	conn, err := net.Dial("unix", ToolSock())
	if err != nil {
		fmt.Fprintln(os.Stderr, "command refused")
		return 126
	}
	defer conn.Close()
	dir, _ := os.Getwd()
	raw, err := json.Marshal(toolRequest{Argv: args, Env: os.Environ(), Dir: dir, File: file})
	if err != nil {
		fmt.Fprintln(os.Stderr, "command refused")
		return 126
	}
	raw = append(raw, '\n')
	if _, err := conn.Write(raw); err != nil {
		fmt.Fprintln(os.Stderr, "command refused")
		return 126
	}
	var writeMu sync.Mutex
	send := func(kind byte, payload []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return writeFrame(conn, kind, payload)
	}
	go copyInput(send)
	go func() {
		buf := make([]byte, 4)
		binary.BigEndian.PutUint32(buf, uint32(syscall.SIGTERM))
		sigs := make(chan os.Signal, 1)
		signalNotify(sigs)
		<-sigs
		_ = send(frameSig, buf)
	}()
	return readOutput(conn)
}

func copyInput(send func(byte, []byte) error) {
	buf := make([]byte, 32<<10)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			if send(frameIn, append([]byte(nil), buf[:n]...)) != nil {
				return
			}
		}
		if err != nil {
			_ = send(frameEOF, nil)
			return
		}
	}
}

func signalNotify(ch chan<- os.Signal) {
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
}

func readOutput(r io.Reader) int {
	for {
		kind, payload, err := readFrame(r)
		if err != nil {
			fmt.Fprintln(os.Stderr, "command refused")
			return 126
		}
		switch kind {
		case frameOut:
			_, _ = os.Stdout.Write(payload)
		case frameErr:
			_, _ = os.Stderr.Write(payload)
		case frameExit:
			if len(payload) != 4 {
				return 126
			}
			return int(binary.BigEndian.Uint32(payload))
		}
	}
}
