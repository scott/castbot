package guestwrap

import (
	"strings"

	"github.com/scott/castbot/internal/egresscontract"
)

func planGit(st egresscontract.Status, base string, args, env []string) Action {
	filtered := stripGitConfig(args)
	argv := []string{realGit}
	if st.Has("github") {
		root := strings.TrimRight(base, "/") + "/git/github/"
		argv = append(argv,
			"-c", "credential.helper=",
			"-c", "credential.helper="+gitCredentialHelper,
			"-c", "url."+root+".insteadOf=https://github.com/",
			"-c", "url."+root+".insteadOf=https://www.github.com/",
		)
	}
	argv = append(argv, filtered...)
	return Action{Argv: argv, Env: childEnv(env)}
}

func stripGitConfig(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			out = append(out, args[i:]...)
			break
		}
		if a == "--config-env" {
			if i+1 < len(args) {
				i++
			}
			continue
		}
		if a == "-c" {
			if i+1 >= len(args) {
				continue
			}
			i++
			if blockedGitConfig(args[i]) {
				continue
			}
			out = append(out, "-c", args[i])
			continue
		}
		if strings.HasPrefix(a, "-c") && len(a) > 2 && blockedGitConfig(a[2:]) {
			continue
		}
		out = append(out, a)
	}
	return out
}

func blockedGitConfig(val string) bool {
	key, _, _ := strings.Cut(val, "=")
	key = strings.ToLower(strings.TrimSpace(key))
	switch {
	case strings.HasPrefix(key, "credential."),
		strings.HasPrefix(key, "url."),
		key == "core.askpass",
		key == "http.extraheader":
		return true
	default:
		return false
	}
}

func planCredential(st egresscontract.Status) Action {
	act := Action{Code: 0}
	if !st.Has("github") {
		act.Stderr = "castbot: " + egresscontract.NotConnected
	}
	return act
}
