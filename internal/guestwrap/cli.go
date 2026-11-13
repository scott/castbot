package guestwrap

import (
	"strings"

	"github.com/scott/castbot/internal/egresscontract"
)

func planGH(st egresscontract.Status, base string, args, env []string) Action {
	env = childEnv(env, proxyLive(st))
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
		name, _ := splitFlag(a)
		if name == "--with-token" {
			return true
		}
	}
	cmds := commands(args, ghBoolFlag)
	if len(cmds) < 2 || cmds[0] != "auth" {
		return false
	}
	switch cmds[1] {
	case "login", "token", "setup-git", "refresh":
		return true
	default:
		return false
	}
}

func planDoctl(st egresscontract.Status, base string, args, env []string) Action {
	env = childEnv(env, proxyLive(st))
	cmds := commands(args, doctlBoolFlag)
	if len(cmds) > 0 && cmds[0] == "auth" {
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
		name, inline := splitFlag(a)
		// Flags after -- are still stripped when they would carry a token.
		// Other words after -- stay, in order.
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

func ghBoolFlag(name string) bool {
	switch name {
	case "--help", "--version":
		return true
	default:
		return false
	}
}

func doctlBoolFlag(name string) bool {
	switch name {
	case "--interactive", "--show", "--trace", "--verbose", "-v", "--help":
		return true
	default:
		return false
	}
}

// commands returns positional words. A non-boolean flag consumes the
// following word, so that word is not a command. "--" ends flag parsing
// and the words after it are commands. This matches the CLIs: an unknown
// long flag consumes the next word, and a boolean flag does not.
func commands(args []string, boolFlag func(string) bool) []string {
	var cmds []string
	positionals := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !positionals && a == "--" {
			positionals = true
			continue
		}
		if !positionals && a != "-" && strings.HasPrefix(a, "-") {
			name, inline := splitFlag(a)
			if inline || boolFlag(name) {
				continue
			}
			if i+1 < len(args) {
				i++
			}
			continue
		}
		cmds = append(cmds, a)
	}
	return cmds
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
