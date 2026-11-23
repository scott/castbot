//go:build linux

package tooliso

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// RuntimeHost is the privileged process. It starts the runtime and serves
// command requests. It does not write the agent tree.
type RuntimeHost struct {
	cmd     *exec.Cmd
	conn    net.Conn
	enc     *json.Encoder
	writeMu sync.Mutex
	mu      sync.Mutex
	logs    io.Reader
	resp    chan hostMsg
	dead    chan struct{}
	done    chan struct{}
	gen     uint64
	exited  map[uint64]struct{}
	once    sync.Once
}

type hostMsg struct {
	Ready bool     `json:"ready"`
	Op    string   `json:"op"`
	Env   []string `json:"env,omitempty"`
	OK    bool     `json:"ok"`
	Event string   `json:"event"`
	Gen   uint64   `json:"gen"`
}

// StartRuntimeHost starts the privileged process when this process is root.
func StartRuntimeHost(argv []string) (*RuntimeHost, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("supervisor")
	}
	if len(argv) == 0 || argv[0] == "" {
		return nil, errors.New("supervisor")
	}
	raw, err := json.Marshal(argv)
	if err != nil {
		return nil, errors.New("supervisor")
	}
	bin, err := os.Executable()
	if err != nil {
		return nil, errors.New("supervisor")
	}
	sp, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("supervisor")
	}
	local := os.NewFile(uintptr(sp[0]), "local")
	remote := os.NewFile(uintptr(sp[1]), "remote")
	logR, logW, err := os.Pipe()
	if err != nil {
		local.Close()
		remote.Close()
		return nil, errors.New("supervisor")
	}
	cmd := exec.Command(bin, SupervisorArg)
	cmd.Env = supervisorEnv(string(raw))
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	cmd.ExtraFiles = []*os.File{remote, logW}
	if err := cmd.Start(); err != nil {
		local.Close()
		remote.Close()
		logR.Close()
		logW.Close()
		return nil, errors.New("supervisor")
	}
	remote.Close()
	logW.Close()
	conn, err := net.FileConn(local)
	local.Close()
	if err != nil {
		cmd.Process.Kill()
		logR.Close()
		return nil, errors.New("supervisor")
	}
	host := &RuntimeHost{
		cmd:    cmd,
		conn:   conn,
		enc:    json.NewEncoder(conn),
		logs:   logR,
		resp:   make(chan hostMsg, 1),
		dead:   make(chan struct{}),
		exited: map[uint64]struct{}{},
	}
	go func() { _ = cmd.Wait() }()
	dec := json.NewDecoder(conn)
	ready := make(chan error, 1)
	go func() {
		var msg hostMsg
		if err := dec.Decode(&msg); err != nil || !msg.Ready {
			ready <- errors.New("supervisor")
			return
		}
		ready <- nil
		host.readLoop(dec)
	}()
	select {
	case err := <-ready:
		if err != nil {
			host.Close()
			return nil, err
		}
	case <-time.After(5 * time.Second):
		host.Close()
		return nil, errors.New("supervisor")
	}
	return host, nil
}

func supervisorEnv(argv string) []string {
	var out []string
	for _, key := range []string{"PATH", "LANG", "LC_ALL", "TZ"} {
		if v, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+v)
		}
	}
	return append(out, "CASTLET_RUNTIME_ARGV="+argv)
}

func (h *RuntimeHost) readLoop(dec *json.Decoder) {
	for {
		var msg hostMsg
		if err := dec.Decode(&msg); err != nil {
			h.mu.Lock()
			if h.done != nil {
				close(h.done)
				h.done = nil
			}
			h.mu.Unlock()
			close(h.dead)
			return
		}
		if msg.Event == "exit" {
			h.mu.Lock()
			h.exited[msg.Gen] = struct{}{}
			if h.done != nil && h.gen == msg.Gen {
				close(h.done)
				h.done = nil
			}
			h.mu.Unlock()
			continue
		}
		select {
		case h.resp <- msg:
		case <-h.dead:
			return
		}
	}
}

// Logs is the runtime output pipe.
func (h *RuntimeHost) Logs() io.Reader {
	if h == nil {
		return nil
	}
	return h.logs
}

// Start asks the privileged process to start the runtime with env.
// The returned channel closes when that process exits.
func (h *RuntimeHost) Start(env []string) (<-chan struct{}, error) {
	if h == nil {
		return nil, errors.New("supervisor")
	}
	done := make(chan struct{})
	if err := h.write(hostMsg{Op: "start", Env: env}); err != nil {
		return nil, err
	}
	select {
	case msg := <-h.resp:
		if !msg.OK || msg.Gen == 0 {
			return nil, errors.New("runtime did not start")
		}
		h.mu.Lock()
		if _, ok := h.exited[msg.Gen]; ok {
			close(done)
		} else {
			h.gen = msg.Gen
			h.done = done
		}
		h.mu.Unlock()
		return done, nil
	case <-h.dead:
		return nil, errors.New("runtime did not start")
	case <-time.After(10 * time.Second):
		return nil, errors.New("runtime did not start")
	}
}

// Stop asks the privileged process to stop the runtime.
func (h *RuntimeHost) Stop() error {
	if h == nil {
		return nil
	}
	if err := h.write(hostMsg{Op: "stop"}); err != nil {
		return err
	}
	select {
	case <-h.resp:
	case <-h.dead:
	case <-time.After(3 * time.Second):
	}
	return nil
}

func (h *RuntimeHost) write(msg hostMsg) error {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	return h.enc.Encode(msg)
}

// Close stops the privileged process.
func (h *RuntimeHost) Close() {
	if h == nil {
		return
	}
	h.once.Do(func() {
		_ = h.conn.Close()
		if h.cmd != nil && h.cmd.Process != nil {
			_ = h.cmd.Process.Kill()
		}
	})
}

// RunSupervisor is the privileged process.
// It serves command requests and starts the runtime. It does not write the agent tree.
func RunSupervisor() {
	runtime.LockOSThread()
	ppid := os.Getppid()
	_, _, errno := unix.RawSyscall6(unix.SYS_PRCTL, unix.PR_SET_PDEATHSIG, uintptr(syscall.SIGKILL), 0, 0, 0, 0)
	if errno != 0 || os.Getppid() != ppid {
		os.Exit(1)
	}
	go func() {
		for {
			time.Sleep(time.Second)
			if os.Getppid() != ppid {
				os.Exit(1)
			}
		}
	}()
	_ = allThreadsPrctl(prSetDumpable, 0)
	var argv []string
	if err := json.Unmarshal([]byte(os.Getenv("CASTLET_RUNTIME_ARGV")), &argv); err != nil || len(argv) == 0 || argv[0] == "" {
		os.Exit(1)
	}
	_ = os.Unsetenv("CASTLET_RUNTIME_ARGV")
	ctrl := os.NewFile(3, "ctrl")
	logs := os.NewFile(4, "log")
	if ctrl == nil || logs == nil {
		os.Exit(1)
	}
	unix.CloseOnExec(3)
	unix.CloseOnExec(4)
	conn, err := net.FileConn(ctrl)
	ctrl.Close()
	if err != nil {
		os.Exit(1)
	}
	defer conn.Close()
	if err := EnsureServe(context.Background()); err != nil {
		os.Exit(1)
	}
	enc := json.NewEncoder(conn)
	var writeMu sync.Mutex
	write := func(msg hostMsg) {
		writeMu.Lock()
		_ = enc.Encode(msg)
		writeMu.Unlock()
	}
	write(hostMsg{Ready: true})
	dec := json.NewDecoder(conn)
	var run *supervised
	var runMu sync.Mutex
	for {
		var msg hostMsg
		if err := dec.Decode(&msg); err != nil {
			os.Exit(0)
		}
		switch msg.Op {
		case "start":
			stopSupervised(&runMu, &run)
			next := startSupervised(argv, msg.Env, logs, write)
			runMu.Lock()
			run = next
			gen := uint64(0)
			if next != nil {
				gen = next.gen
			}
			runMu.Unlock()
			if next == nil {
				write(hostMsg{OK: false})
				continue
			}
			write(hostMsg{OK: true, Gen: gen})
		case "stop":
			stopSupervised(&runMu, &run)
			write(hostMsg{OK: true})
		default:
			write(hostMsg{OK: false})
		}
	}
}

type supervised struct {
	cmd    *exec.Cmd
	gen    uint64
	exited chan struct{}
}

var superviseGen uint64

func startSupervised(argv, env []string, logs *os.File, write func(hostMsg)) *supervised {
	if len(argv) == 0 || logs == nil {
		return nil
	}
	superviseGen++
	gen := superviseGen
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append([]string(nil), env...)
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := ApplyRuntime(cmd); err != nil {
		return nil
	}
	if err := cmd.Start(); err != nil {
		return nil
	}
	run := &supervised{cmd: cmd, gen: gen, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(run.exited)
		write(hostMsg{Event: "exit", Gen: gen})
	}()
	return run
}

func stopSupervised(mu *sync.Mutex, slot **supervised) {
	mu.Lock()
	run := *slot
	*slot = nil
	mu.Unlock()
	if run == nil || run.cmd == nil || run.cmd.Process == nil {
		return
	}
	_ = run.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-run.exited:
	case <-time.After(2 * time.Second):
		_ = run.cmd.Process.Kill()
		<-run.exited
	}
}
