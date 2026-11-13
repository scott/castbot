package guestwrap

import (
	"strings"

	"github.com/scott/castbot/internal/egresscontract"
)

func planGH(st egresscontract.Status, base string, args, env []string) Action {
	env = childEnv(env)
	env = setEnv(env, "GH_CONFIG_DIR", ghConfigDir)
	if refusedAuth(args) {
		return Action{
			Stderr: "castbot: this command is disabled so a token cannot be read or stored.",
			Code:   1,
			Env:    env,
		}
	}
	if st.Has("github") {
		if call, ok := parseGHAPI(args); ok {
			call.Base = base
			return Action{GH: &call, Env: env}
		}
		act := Action{Argv: append([]string{realGH}, args...), Env: env}
		if !isQuiet(args) {
			act.Stderr = "castbot: GitHub is connected, but this gh command is not sent through the local egress. It is running without a token."
		}
		return act
	}
	act := Action{Argv: append([]string{realGH}, args...), Env: env}
	if !isQuiet(args) {
		act.Stderr = "castbot: " + egresscontract.NotConnected
	}
	return act
}

func refusedAuth(args []string) bool {
	for _, a := range args {
		if a == "--with-token" {
			return true
		}
	}
	cmd, rest := firstCommand(args)
	if cmd != "auth" {
		return false
	}
	sub, _ := firstCommand(rest)
	switch sub {
	case "login", "token", "setup-git", "refresh":
		return true
	default:
		return false
	}
}

func planDoctl(st egresscontract.Status, base string, args, env []string) Action {
	env = childEnv(env)
	cmd, _ := firstCommand(args)
	if cmd == "auth" {
		return Action{
			Stderr: "castbot: this command is disabled so a token cannot be read or stored.",
			Code:   1,
			Env:    env,
		}
	}
	argv := []string{realDoctl, "--config", doctlConfigPath, "--context", "default"}
	if st.Has("digitalocean") {
		api := strings.TrimRight(base, "/") + "/upstream/digitalocean/"
		argv = append(argv, "--api-url", api, "--access-token", egresscontract.DummyCredential)
		env = setEnv(env, "DIGITALOCEAN_API_URL", api)
		env = setEnv(env, "DIGITALOCEAN_ACCESS_TOKEN", egresscontract.DummyCredential)
	}
	argv = append(argv, stripDoctlFlags(args)...)
	act := Action{Argv: argv, Env: env, Prepare: "doctl"}
	if !st.Has("digitalocean") && !isQuiet(args) {
		act.Stderr = "castbot: " + egresscontract.NotConnected
	}
	return act
}

func stripDoctlFlags(args []string) []string {
	valued := map[string]bool{
		"--access-token": true,
		"--api-url":      true,
		"--config":       true,
		"--context":      true,
		"-t":             true,
		"-u":             true,
		"-c":             true,
	}
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			out = append(out, args[i:]...)
			break
		}
		name, inline := splitFlag(a)
		if valued[name] {
			if !inline && i+1 < len(args) {
				i++
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

func splitFlag(a string) (name string, inline bool) {
	if strings.HasPrefix(a, "--") {
		name, _, ok := strings.Cut(a, "=")
		return name, ok
	}
	if strings.HasPrefix(a, "-") && len(a) > 2 {
		return a[:2], true
	}
	return a, false
}

func firstCommand(args []string) (string, []string) {
	for i, a := range args {
		if a == "--" {
			return "", nil
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		return a, args[i+1:]
	}
	return "", nil
}

func isQuiet(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "--version", "version", "--help", "-h", "help":
		return len(args) == 1
	default:
		return false
	}
}
