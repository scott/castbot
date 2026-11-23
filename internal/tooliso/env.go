// Package tooliso keeps command processes away from the runtime's secrets.
// The runtime still receives granted values. A command receives an allowlist,
// plus a name the owner explicitly marked for commands.
package tooliso

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/connect"
)

// ToolUser is the unprivileged account that runs bash and other commands.
const ToolUser = "agent"

// ToolSecretEnv is the runtime environment name that lists command-visible
// secret names. The values are not in this variable. It is not passed to
// the command.
const ToolSecretEnv = "CASTBOT_TOOL_SECRET_NAMES"

// defaultSystemPath is the runtime and command PATH with agent-writable
// directories last, so a program under /app/.local cannot shadow git,
// python, or castlet.
var defaultSystemPath = []string{
	"/usr/local/sbin",
	"/usr/local/bin",
	"/usr/sbin",
	"/usr/bin",
	"/sbin",
	"/bin",
	"/opt/node/bin",
	"/usr/local/go/bin",
	"/opt/cast/bin",
}

// agentPathTail is appended after the system directories.
var agentPathTail = []string{"/app/.local/bin"}

var allowedExact = map[string]bool{
	"PATH": true, "HOME": true, "PWD": true, "OLDPWD": true,
	"LANG": true, "LANGUAGE": true, "TERM": true, "TZ": true,
	"TMPDIR": true, "TMP": true, "TEMP": true,
	"USER": true, "LOGNAME": true, "SHELL": true, "SHLVL": true,
	"CAST_AGENT_DIR": true, "CAST_AGENT_PATH": true,
	"PYTHONUSERBASE": true, "PYTHONUNBUFFERED": true, "PYTHONDONTWRITEBYTECODE": true,
	"PIP_DISABLE_PIP_VERSION_CHECK": true, "PIP_NO_CACHE_DIR": true,
	"NPM_CONFIG_PREFIX": true,
	"GOROOT":            true, "GOPATH": true, "GOBIN": true, "GOCACHE": true,
	"GOMODCACHE": true, "GOTOOLCHAIN": true, "GO111MODULE": true, "GOPROXY": true,
	"GIT_PYTHON_REFRESH": true, "GIT_TERMINAL_PROMPT": true, "GIT_CONFIG_COUNT": true,
	"GIT_AUTHOR_NAME": true, "GIT_AUTHOR_EMAIL": true,
	"GIT_COMMITTER_NAME": true, "GIT_COMMITTER_EMAIL": true,
	"NO_PROXY": true, "no_proxy": true,
	"DEBIAN_FRONTEND": true, "NODE_PATH": true,
}

// ParseNames splits the command-visible name list. A blank list means no
// granted secret is copied. Model key names and castlet names are dropped
// even when they appear here.
func ParseNames(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" || seen[name] || DeniedToolName(name) {
			continue
		}
		if !envNameOK(name) {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// DeniedToolName reports a name that must not reach a command, even when
// the owner marks the secret for commands.
func DeniedToolName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return true
	}
	if connect.BlocksGuest(name) {
		return true
	}
	upper := strings.ToUpper(name)
	switch upper {
	case "PATH", "HOME", "HOSTNAME", "AUTH_TOKEN", "CASTBOT_BOOTSTRAP_TOKEN", "CASTBOT_CA_FILE", "CASTLET_SECRET_COUNT":
		return true
	}
	if strings.HasPrefix(upper, "CASTLET_") || strings.HasPrefix(upper, "CASTBOT_") {
		return true
	}
	for _, env := range castcfg.GuestEnvVars() {
		if strings.EqualFold(name, env) {
			return true
		}
	}
	return false
}

func envNameOK(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, c := range name {
		ok := c == '_' || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if i == 0 {
			ok = c >= 'A' && c <= 'Z'
		}
		if !ok {
			return false
		}
	}
	return true
}

// ToolParentMark is set by the runtime when a caller passes its own
// environment. The value is one "name<tab>sha256" line per parent variable.
// It is not passed to the command.
const ToolParentMark = "CASTBOT_TOOL_PARENT"

// ToolEnv is the environment for one command. parent is the runtime
// environment. extra is the owner opt-in list from ParseNames.
func ToolEnv(parent []string, extra []string) []string {
	return toolEnv(parent, extra)
}

// ToolEnvExplicit is the environment for a caller that passed env=.
// caller is that environment and includes ToolParentMark, the hashes of
// the runtime environment. The command receives the allowlist, the owner
// opt-in list, and names whose values are not already in the runtime
// environment. A missing mark passes only the allowlist and the opt-in list.
func ToolEnvExplicit(caller []string, extra []string) []string {
	raw := envValue(caller, ToolParentMark)
	if strings.TrimSpace(raw) == "" {
		return toolEnv(caller, extra)
	}
	marks := parentMarks(raw)
	var added []string
	for _, item := range caller {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" || DeniedToolName(key) || !envNameOK(key) {
			continue
		}
		sum := sha256hex(value)
		prev, found := marks[key]
		if !found || prev != sum {
			added = append(added, key)
		}
	}
	return toolEnv(caller, append(append([]string{}, extra...), added...))
}

func envValue(env []string, name string) string {
	var value string
	for _, item := range env {
		key, rest, ok := strings.Cut(item, "=")
		if ok && key == name {
			value = rest
		}
	}
	return value
}

func parentMarks(raw string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		key, sum, ok := strings.Cut(line, "\t")
		if !ok || key == "" || sum == "" || strings.ContainsAny(key, "=\n\r\x00") {
			continue
		}
		out[key] = sum
	}
	return out
}

func sha256hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func toolEnv(parent []string, extra []string) []string {
	vals := map[string]string{}
	var order []string
	put := func(key, value string) {
		if _, ok := vals[key]; ok {
			vals[key] = value
			return
		}
		vals[key] = value
		order = append(order, key)
	}
	allowedExtra := map[string]bool{}
	for _, name := range extra {
		if !DeniedToolName(name) && envNameOK(name) {
			allowedExtra[name] = true
		}
	}
	for _, env := range parent {
		key, value, ok := strings.Cut(env, "=")
		if !ok || key == "" || DeniedToolName(key) {
			continue
		}
		if !allowed(key) && !allowedExtra[key] {
			continue
		}
		if strings.ContainsAny(value, "\x00\n\r") {
			continue
		}
		put(key, value)
	}
	home := "/app/agent"
	if dir := vals["CAST_AGENT_PATH"]; dir != "" && safeDir(dir) {
		home = dir
	} else if dir := vals["CAST_AGENT_DIR"]; dir != "" && safeDir(dir) {
		home = dir
	}
	put("HOME", home)
	put("USER", ToolUser)
	put("LOGNAME", ToolUser)
	put("SHELL", "/bin/bash")
	put("PATH", commandPath())
	put("TMPDIR", AgentTmp)
	put("TMP", AgentTmp)
	put("TEMP", AgentTmp)
	var out []string
	for _, key := range order {
		out = append(out, key+"="+vals[key])
	}
	return out
}

func allowed(key string) bool {
	if allowedExact[key] {
		return true
	}
	if strings.HasPrefix(key, "LC_") {
		return true
	}
	if strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
		return true
	}
	return false
}

func safeDir(dir string) bool {
	if dir == "" || strings.Contains(dir, "\x00") || !strings.HasPrefix(dir, "/") {
		return false
	}
	if strings.Contains(dir, "/../") || strings.HasSuffix(dir, "/..") || dir == "/.." {
		return false
	}
	return true
}

func commandPath() string {
	return strings.Join(append(append([]string{}, defaultSystemPath...), agentPathTail...), ":")
}

// RewriteRuntimePath moves agent-writable directories to the end of PATH
// in the runtime environment. Other entries stay in order.
func RewriteRuntimePath(env []string) []string {
	out := make([]string, 0, len(env)+1)
	found := false
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key != "PATH" {
			out = append(out, item)
			continue
		}
		found = true
		out = append(out, "PATH="+reorderPath(value))
	}
	if !found {
		out = append(out, "PATH="+reorderPath(""))
	}
	return out
}

func reorderPath(value string) string {
	var system, tail []string
	seen := map[string]bool{}
	add := func(dst *[]string, dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		*dst = append(*dst, dir)
	}
	for _, dir := range strings.Split(value, ":") {
		if agentWritable(dir) {
			add(&tail, dir)
			continue
		}
		add(&system, dir)
	}
	for _, dir := range defaultSystemPath {
		add(&system, dir)
	}
	for _, dir := range agentPathTail {
		add(&tail, dir)
	}
	return strings.Join(append(system, tail...), ":")
}

func agentWritable(dir string) bool {
	dir = strings.TrimRight(strings.TrimSpace(dir), "/")
	if dir == "" {
		return false
	}
	if dir == "/app/.local" || strings.HasPrefix(dir, "/app/.local/") {
		return true
	}
	if dir == "/app/agent" || strings.HasPrefix(dir, "/app/agent/") {
		return true
	}
	return false
}
