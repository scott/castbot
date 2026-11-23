// Command castbot-toolrun starts one command as the tool account.
// The supervisor is root and is the process that changes accounts.
// The runtime asks for a command on the local socket. This process stays
// so the runtime can signal it, and the command runs in its own process
// group. The parent scrubs the environment before the command starts.
// It does not print the environment.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/scott/castbot/internal/tooliso"
)

func main() {
	_ = tooliso.SetNonDumpable()
	args := os.Args[1:]
	if len(args) == 0 {
		os.Exit(2)
	}
	switch args[0] {
	case "--serve":
		if err := tooliso.EnsureServe(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, "tool server did not start")
			os.Exit(1)
		}
		select {}
	case "--as-runtime":
		os.Exit(asRuntime(args[1:]))
	case "--file":
		os.Exit(dispatch(true, args[1:]))
	case "--dropped":
		os.Exit(dropped(args[1:]))
	case "--":
		args = args[1:]
	}
	if len(args) == 0 || args[0] == "" {
		os.Exit(2)
	}
	os.Exit(dispatch(false, args))
}

func dispatch(file bool, args []string) int {
	if len(args) == 0 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "command refused")
		return 126
	}
	if tooliso.DirectRun() {
		if file {
			return tooliso.RunFile(args)
		}
		return supervise(args)
	}
	if os.Geteuid() == 0 {
		return runAsAgent(file, args)
	}
	return tooliso.Proxy(file, args)
}

func asRuntime(args []string) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 || args[0] == "" || os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "command refused")
		return 126
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = os.Environ()
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := tooliso.ApplyRuntime(cmd); err != nil {
		fmt.Fprintln(os.Stderr, "command refused")
		return 126
	}
	return waitCommand(cmd)
}

func runAsAgent(file bool, args []string) int {
	self, err := os.Executable()
	if err != nil || self == "" {
		fmt.Fprintln(os.Stderr, "command refused")
		return 126
	}
	var childArgs []string
	var env []string
	if file {
		childArgs = append([]string{self, "--file"}, args...)
		env = tooliso.FileEnv(os.Environ())
	} else {
		env = tooliso.CommandEnv(os.Environ())
		path := tooliso.Resolve(env, args[0])
		if path == "" {
			fmt.Fprintln(os.Stderr, "command refused")
			return 126
		}
		childArgs = append([]string{self, "--dropped", "--", path}, args[1:]...)
	}
	cmd := exec.Command(childArgs[0], childArgs[1:]...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := tooliso.ApplyTool(cmd); err != nil {
		fmt.Fprintln(os.Stderr, "command refused")
		return 126
	}
	return waitCommand(cmd)
}

func supervise(args []string) int {
	if err := tooliso.RequireToolGroups(); err != nil {
		fmt.Fprintln(os.Stderr, "command refused")
		return 126
	}
	env := tooliso.CommandEnv(os.Environ())
	if os.Getenv("CASTBOT_TOOLRUN_TEST") == "1" {
		env = append(env, "CASTBOT_TOOLRUN_TEST=1")
	}
	path := tooliso.Resolve(env, args[0])
	if path == "" {
		fmt.Fprintln(os.Stderr, "command refused")
		return 126
	}
	self, err := os.Executable()
	if err != nil || self == "" {
		fmt.Fprintln(os.Stderr, "command refused")
		return 126
	}
	childArgs := append([]string{self, "--dropped", "--", path}, args[1:]...)
	cmd := exec.Command(childArgs[0], childArgs[1:]...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	return waitCommand(cmd)
}

func waitCommand(cmd *exec.Cmd) int {
	outW := cmd.Stdout
	errW := cmd.Stderr
	if outW == nil {
		outW = os.Stdout
	}
	if errW == nil {
		errW = os.Stderr
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, "command refused")
		return 126
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		fmt.Fprintln(os.Stderr, "command refused")
		return 126
	}
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	if err := cmd.Start(); err != nil {
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
		fmt.Fprintln(os.Stderr, "command refused")
		return 126
	}
	// The parent must not keep the write ends. The command's own ends
	// close when it exits, so the copy can see the end of the output.
	_ = stdoutW.Close()
	_ = stderrW.Close()
	defer stdoutR.Close()
	defer stderrR.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(outW, stdoutR)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(errW, stderrR)
	}()
	copied := make(chan struct{})
	go func() {
		wg.Wait()
		close(copied)
	}()

	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	proc := make(chan error, 1)
	go func() { proc <- cmd.Wait() }()
	select {
	case err := <-proc:
		// A child can keep a write end after the command exits.
		// Close the read side after a short grace instead of waiting.
		tooliso.FinishReads(copied, tooliso.ReadGrace, stdoutR, stderrR)
		return commandStatus(err)
	case <-sigs:
		stopGroup(cmd.Process.Pid)
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		tooliso.FinishReads(copied, tooliso.ReadGrace, stdoutR, stderrR)
		select {
		case <-proc:
		case <-time.After(tooliso.ReadGrace):
		}
		return 128 + int(syscall.SIGTERM)
	}
}

func commandStatus(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 126
}

func stopGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	time.Sleep(200 * time.Millisecond)
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func dropped(args []string) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 || args[0] == "" {
		return 126
	}
	env := withoutTestFlag(os.Environ())
	if err := tooliso.DropToToolUser(); err != nil {
		fmt.Fprintln(os.Stderr, "command refused")
		return 126
	}
	_ = tooliso.SetNonDumpable()
	if err := syscall.Exec(args[0], args, env); err != nil {
		fmt.Fprintln(os.Stderr, "command refused")
		return 126
	}
	return 126
}

func withoutTestFlag(env []string) []string {
	out := make([]string, 0, len(env))
	for _, item := range env {
		if strings.HasPrefix(item, "CASTBOT_TOOLRUN_TEST=") {
			continue
		}
		out = append(out, item)
	}
	return out
}
