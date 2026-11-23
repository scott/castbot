// Command castbot-toolrun starts one command as the tool account.
// The runtime image installs it setuid to that account. It scrubs the
// environment before the command starts. It does not print the environment.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/scott/castbot/internal/tooliso"
)

func main() {
	_ = tooliso.SetNonDumpable()
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 || args[0] == "" {
		os.Exit(2)
	}
	env := tooliso.ToolEnv(os.Environ(), tooliso.ParseNames(os.Getenv(tooliso.ToolSecretEnv)))
	if err := tooliso.DropToToolUser(); err != nil {
		fmt.Fprintln(os.Stderr, "command refused")
		os.Exit(126)
	}
	path := args[0]
	if !filepath.IsAbs(path) {
		path = look(env, path)
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, "command refused")
		os.Exit(126)
	}
	if err := syscall.Exec(path, args, env); err != nil {
		fmt.Fprintln(os.Stderr, "command refused")
		os.Exit(126)
	}
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
