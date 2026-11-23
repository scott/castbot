// Command castbot-toolrun starts one command as the tool account.
// The runtime image installs it setuid and setgid to that account.
// This process stays as the parent so the runtime can signal it, and the
// command runs in its own process group. The parent scrubs the environment
// before the command starts. It does not print the environment.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/scott/castbot/internal/tooliso"
)

func main() {
	_ = tooliso.SetNonDumpable()
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--file" {
		os.Exit(tooliso.RunFile(args[1:]))
	}
	if len(args) > 0 && args[0] == "--dropped" {
		os.Exit(dropped(args[1:]))
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 || args[0] == "" {
		os.Exit(2)
	}
	os.Exit(supervise(args))
}

func supervise(args []string) int {
	if err := tooliso.RequireToolGroups(); err != nil {
		fmt.Fprintln(os.Stderr, "command refused")
		return 126
	}
	explicit := os.Getenv("CASTBOT_TOOL_EXPLICIT") == "1"
	var env []string
	if explicit {
		env = tooliso.ToolEnvExplicit(os.Environ(), tooliso.ParseNames(os.Getenv(tooliso.ToolSecretEnv)))
	} else {
		env = tooliso.ToolEnv(os.Environ(), tooliso.ParseNames(os.Getenv(tooliso.ToolSecretEnv)))
	}
	path := args[0]
	if !filepath.IsAbs(path) {
		path = look(env, path)
	}
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
	childEnv := append([]string{}, env...)
	if os.Getenv("CASTBOT_TOOLRUN_TEST") == "1" {
		childEnv = append(childEnv, "CASTBOT_TOOLRUN_TEST=1")
	}
	cmd := exec.Command(childArgs[0], childArgs[1:]...)
	cmd.Env = childEnv
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "command refused")
		return 126
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-sigs:
		stopGroup(cmd.Process.Pid)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		return 128 + int(syscall.SIGTERM)
	case err := <-done:
		if err == nil {
			return 0
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		return 126
	}
}

func stopGroup(pid int) {
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

func look(env []string, name string) string {
	if name == "" || strings.ContainsAny(name, "/\x00") {
		return ""
	}
	var path string
	for _, item := range env {
		if strings.HasPrefix(item, "PATH=") {
			path = strings.TrimPrefix(item, "PATH=")
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		cand := filepath.Join(dir, name)
		info, err := os.Stat(cand)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		return cand
	}
	return ""
}
