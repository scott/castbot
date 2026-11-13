package guestwrap

import (
	"context"
	"io"
	"os"
	"syscall"

	"github.com/scott/castbot/internal/egresscontract"
)

// Action is what one wrapper invocation does. Argv is executed when set.
// GH is a redirected `gh api` call. Serve names an MCP server to speak
// on stdin. Text fields are written and then the process exits when
// there is nothing to execute.
type Action struct {
	Argv    []string
	Env     []string
	Stdout  string
	Stderr  string
	Code    int
	Prepare string
	GH      *ghCall
	Serve   string
}

// Plan chooses the action for a multicall name. It does not read a
// connection token. A status probe that fails is treated as disabled,
// which leaves git unchanged and runs gh and doctl without credentials.
func Plan(name string, args, env []string) Action {
	base := EgressBase(env)
	st := statusOf(env)
	switch name {
	case "git":
		return planGit(st, base, args, env)
	case "gh":
		return planGH(st, base, args, env)
	case "doctl":
		return planDoctl(st, base, args, env)
	case "castbot-git-credential":
		return planCredential(st)
	case "castbot-mcp":
		if len(args) != 1 {
			return Action{Stderr: "castbot: " + egresscontract.Rejected, Code: 1}
		}
		switch args[0] {
		case "github", "digitalocean", "request_secret":
			return Action{Serve: args[0]}
		default:
			return Action{Stderr: "castbot: " + egresscontract.Rejected, Code: 1}
		}
	default:
		return Action{Stderr: "castbot: " + egresscontract.NotConnected, Code: 1}
	}
}

// statusOf is the liveness probe. Tests replace it.
var statusOf = func(env []string) egresscontract.Status {
	return FetchStatus(context.Background(), EgressBase(env))
}

// execCommand replaces the process. Tests replace it.
var execCommand = func(argv, env []string) int {
	if len(argv) == 0 {
		return 127
	}
	if err := syscall.Exec(argv[0], argv, env); err != nil {
		return 127
	}
	return 0
}

// Run performs an action. It writes the fixed sentences and never prints
// an environment value.
func Run(name string, args, env []string, in io.Reader, out, errw io.Writer) int {
	if name == "castbot-git-credential" {
		_, _ = io.Copy(io.Discard, io.LimitReader(in, 1<<16))
	}
	act := Plan(name, args, env)
	if act.Serve != "" {
		base := EgressBase(env)
		if err := ServeMCP(act.Serve, base, in, out); err != nil {
			writeLine(errw, "castbot: "+egresscontract.NotConnected)
			return 1
		}
		return 0
	}
	if act.GH != nil {
		if act.GH.Base == "" {
			act.GH.Base = EgressBase(env)
		}
		return runGH(*act.GH, in, out, errw)
	}
	if act.Stderr != "" {
		writeLine(errw, act.Stderr)
	}
	if act.Stdout != "" {
		_, _ = io.WriteString(out, act.Stdout)
	}
	if len(act.Argv) == 0 {
		if act.Code == 0 && act.Stderr == "" && act.Stdout == "" {
			return 0
		}
		if act.Code == 0 {
			return 0
		}
		if act.Code != 0 {
			return act.Code
		}
		return 1
	}
	if act.Prepare == "doctl" {
		if err := prepareDoctl(); err != nil {
			writeLine(errw, "castbot: "+egresscontract.NotConnected)
			return 1
		}
	}
	if name == "gh" {
		if err := prepareGH(); err != nil {
			writeLine(errw, "castbot: "+egresscontract.NotConnected)
			return 1
		}
	}
	code := execCommand(act.Argv, act.Env)
	if code != 0 {
		writeLine(errw, "castbot: command failed")
	}
	return code
}

func prepareGH() error {
	if err := os.MkdirAll(ghConfigDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(ghConfigDir+"/config.yml", []byte("prompt: disabled\ngit_protocol: https\n"), 0o600); err != nil {
		return err
	}
	_ = os.Remove(ghConfigDir + "/hosts.yml")
	return nil
}

func prepareDoctl() error {
	if err := os.MkdirAll("/app/.local/share/castbot", 0o700); err != nil {
		return err
	}
	return os.WriteFile(doctlConfigPath, []byte("context: default\n"), 0o600)
}
