package guestwrap

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/scott/castbot/internal/egresscontract"
)

const (
	// DefaultEgress is the loopback castlet egress. Wrappers refuse any
	// other host so a guest env cannot point them at a remote proxy.
	DefaultEgress = "http://127.0.0.1:8090"

	realGit   = "/usr/bin/git"
	realGH    = "/opt/castbot/libexec/gh"
	realDoctl = "/opt/castbot/libexec/doctl"

	gitCredentialHelper = "/usr/local/bin/castbot-git-credential"
	ghConfigDir         = "/app/.local/share/castbot/gh"
	doctlConfigPath     = "/app/.local/share/castbot/doctl.yaml"

	// statusWait is how long a wrapper waits for local liveness.
	// Castlet itself waits less than this on the control plane, so a
	// cache miss still returns before the wrapper gives up.
	statusWait = 3 * time.Second
)

// EgressBase returns the local egress URL, or DefaultEgress when the
// environment points anywhere else.
func EgressBase(env []string) string {
	raw := envGet(env, "CASTLET_EGRESS_URL")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil {
		return DefaultEgress
	}
	host := u.Hostname()
	if host != "127.0.0.1" {
		return DefaultEgress
	}
	port := u.Port()
	if port == "" {
		return DefaultEgress
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return DefaultEgress
	}
	return "http://127.0.0.1:" + port
}

// FetchStatus reads liveness from the local egress. Any failure, including
// a timeout, is a disabled status. The body is not returned to the caller.
func FetchStatus(ctx context.Context, base string) egresscontract.Status {
	ctx, cancel := context.WithTimeout(ctx, statusWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/status", nil)
	if err != nil {
		return egresscontract.Status{Providers: []string{}}
	}
	client := &http.Client{
		Timeout: statusWait,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: (&net.Dialer{
				Timeout: 500 * time.Millisecond,
			}).DialContext,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	res, err := client.Do(req)
	if err != nil {
		return egresscontract.Status{Providers: []string{}}
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		return egresscontract.Status{Providers: []string{}}
	}
	var st egresscontract.Status
	if err := json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&st); err != nil {
		return egresscontract.Status{Providers: []string{}}
	}
	return st.Normalize()
}

func envGet(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return item[len(prefix):]
		}
	}
	return ""
}

func blockedEnvKey(key string) bool {
	switch strings.ToUpper(key) {
	case "GH_TOKEN", "GITHUB_TOKEN", "GITHUB_PERSONAL_ACCESS_TOKEN",
		"GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN",
		"DIGITALOCEAN_ACCESS_TOKEN", "DIGITALOCEAN_TOKEN", "DIGITALOCEAN_API_TOKEN",
		"DIGITALOCEAN_API_URL",
		"GIT_ASKPASS", "SSH_ASKPASS", "GIT_CONFIG_COUNT",
		"AUTH_TOKEN", "CASTBOT_BOOTSTRAP_TOKEN":
		return true
	default:
		upper := strings.ToUpper(key)
		return strings.HasPrefix(upper, "GIT_CONFIG_KEY_") || strings.HasPrefix(upper, "GIT_CONFIG_VALUE_")
	}
}

// FilterEnv copies env without connection tokens, git credential overrides,
// or the machine bootstrap token. It does not read the values.
func FilterEnv(env []string) []string {
	var out []string
	for _, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if !ok || blockedEnvKey(key) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func setEnv(env []string, key, val string) []string {
	prefix := key + "="
	var out []string
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			continue
		}
		out = append(out, item)
	}
	return append(out, prefix+val)
}

func childEnv(env []string) []string {
	return setEnv(FilterEnv(env), "GIT_TERMINAL_PROMPT", "0")
}
