package guestwrap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/egresscontract"
)

const fixtureValue = "fixture-should-not-appear"

func withStatus(t *testing.T, st egresscontract.Status) {
	t.Helper()
	prev := statusOf
	statusOf = func([]string) egresscontract.Status { return st.Normalize() }
	t.Cleanup(func() { statusOf = prev })
}

func envWithToken() []string {
	return []string{
		"PATH=/usr/bin",
		"HOME=/app",
		"GITHUB_TOKEN=" + fixtureValue,
		"GH_TOKEN=" + fixtureValue,
		"DIGITALOCEAN_ACCESS_TOKEN=" + fixtureValue,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=store",
		"CAST_AGENT_DIR=/app/agent",
	}
}

func joined(act Action) string {
	return strings.Join(act.Argv, "\n") + "\n" + strings.Join(act.Env, "\n") + "\n" + act.Stdout + "\n" + act.Stderr
}

func TestFilterEnvDropsTokens(t *testing.T) {
	out := FilterEnv(envWithToken())
	text := strings.Join(out, "\n")
	if strings.Contains(text, fixtureValue) || strings.Contains(text, "GIT_CONFIG_") || strings.Contains(text, "GITHUB_TOKEN") {
		t.Fatal("filtered env still had a token or git config override")
	}
	if !strings.Contains(text, "CAST_AGENT_DIR=/app/agent") || !strings.Contains(text, "PATH=/usr/bin") {
		t.Fatalf("kept env %s", text)
	}
}

func TestEgressBaseStaysOnLoopback(t *testing.T) {
	if EgressBase(nil) != DefaultEgress {
		t.Fatal(EgressBase(nil))
	}
	if got := EgressBase([]string{"CASTLET_EGRESS_URL=http://127.0.0.1:8091"}); got != "http://127.0.0.1:8091" {
		t.Fatal(got)
	}
	for _, raw := range []string{
		"https://127.0.0.1:8090",
		"http://example.com:8090",
		"http://169.254.169.254:8090",
		"http://127.0.0.1:8090@example.com/",
	} {
		if got := EgressBase([]string{"CASTLET_EGRESS_URL=" + raw}); got != DefaultEgress {
			t.Fatalf("%s -> %s", raw, got)
		}
	}
}

func TestFetchStatusFailClosed(t *testing.T) {
	start := time.Now()
	st := FetchStatus(context.Background(), "http://127.0.0.1:1")
	if st.Enabled || len(st.Providers) != 0 {
		t.Fatalf("%+v", st)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("status probe hung")
	}
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second)
	}))
	defer hang.Close()
	start = time.Now()
	st = FetchStatus(context.Background(), hang.URL)
	if st.Enabled || time.Since(start) > 4*time.Second {
		t.Fatalf("enabled %v after %s", st.Enabled, time.Since(start))
	}
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"enabled":true,"providers":["github","nope"]}`)
	}))
	defer ok.Close()
	st = FetchStatus(context.Background(), ok.URL)
	if !st.Has("github") || st.Has("nope") || st.Has("digitalocean") {
		t.Fatalf("%+v", st)
	}
}

func TestGitStaysInertUntilLive(t *testing.T) {
	withStatus(t, egresscontract.Status{})
	act := planGit(egresscontract.Status{}, DefaultEgress, []string{
		"-c", "user.email=fixture@example.com",
		"-c", "credential.helper=store",
		"-c", "http.extraheader=AUTHORIZATION: bearer " + fixtureValue,
		"status",
	}, envWithToken())
	text := joined(act)
	if strings.Contains(text, "insteadOf") || strings.Contains(text, fixtureValue) || strings.Contains(act.joinedArgs(), "credential.helper") {
		t.Fatal("inert git rewrote, kept a helper, or kept the env value")
	}
	if strings.Contains(text, "NO_PROXY=") {
		t.Fatal("inert git set NO_PROXY")
	}
	if !strings.Contains(strings.Join(act.Argv, " "), "user.email=fixture@example.com") {
		t.Fatalf("args %v", act.Argv)
	}
	if act.Argv[0] != realGit {
		t.Fatal(act.Argv[0])
	}
	live := egresscontract.Status{Enabled: true, Providers: []string{"github"}}
	act = planGit(live, DefaultEgress, []string{"status"}, envWithToken())
	text = strings.Join(act.Argv, " ")
	if !strings.Contains(text, "url."+DefaultEgress+"/git/github/.insteadOf=https://github.com/") {
		t.Fatal(text)
	}
	if !strings.Contains(text, "credential.helper=") || !strings.Contains(text, gitCredentialHelper) {
		t.Fatal(text)
	}
	if strings.Contains(joined(act), fixtureValue) {
		t.Fatal("live git kept the env value")
	}
	if !hasEnv(act.Env, "NO_PROXY", "127.0.0.1,localhost") || !hasEnv(act.Env, "no_proxy", "127.0.0.1,localhost") {
		t.Fatalf("live git env %v", act.Env)
	}
	withProxy := append(envWithToken(), "HTTP_PROXY=http://proxy.example:8080", "NO_PROXY=example.com")
	act = planGit(live, DefaultEgress, []string{"status"}, withProxy)
	if !hasEnv(act.Env, "NO_PROXY", "example.com,127.0.0.1,localhost") || !hasEnv(act.Env, "HTTP_PROXY", "http://proxy.example:8080") {
		t.Fatalf("proxy env %v", act.Env)
	}
}

func hasEnv(env []string, key, want string) bool {
	for _, item := range env {
		if item == key+"="+want {
			return true
		}
	}
	return false
}

func (a Action) joinedArgs() string { return strings.Join(a.Argv, " ") }

func TestCredentialHelperPrintsNoSecret(t *testing.T) {
	withStatus(t, egresscontract.Status{})
	var out, errw strings.Builder
	code := Run("castbot-git-credential", []string{"get"}, envWithToken(), strings.NewReader("protocol=https\nhost=github.com\npassword="+fixtureValue+"\n\n"), &out, &errw)
	if code != 0 {
		t.Fatal(code)
	}
	if strings.Contains(out.String(), fixtureValue) || strings.Contains(errw.String(), fixtureValue) {
		t.Fatal("credential helper echoed the env value")
	}
	if !strings.Contains(errw.String(), egresscontract.NotConnected) {
		t.Fatal(errw.String())
	}
	if strings.Contains(out.String(), "password=") || strings.Contains(out.String(), "username=") {
		t.Fatal(out.String())
	}
}

func TestCLIsFailClosed(t *testing.T) {
	withStatus(t, egresscontract.Status{})
	gh := Plan("gh", []string{"api", "user"}, envWithToken())
	if gh.GH != nil || strings.Contains(joined(gh), fixtureValue) {
		t.Fatal("gh api was redirected or kept the env value while disconnected")
	}
	if !strings.Contains(gh.Stderr, egresscontract.NotConnected) {
		t.Fatal(gh.Stderr)
	}
	if gh.Argv[0] != realGH {
		t.Fatal(gh.Argv)
	}
	refused := Plan("gh", []string{"auth", "token"}, envWithToken())
	if len(refused.Argv) != 0 || strings.Contains(joined(refused), fixtureValue) {
		t.Fatal("gh auth token was executed or echoed the env value")
	}
	do := Plan("doctl", []string{"account", "get", "--access-token", fixtureValue}, envWithToken())
	if strings.Contains(joined(do), fixtureValue) || strings.Contains(strings.Join(do.Argv, " "), "--api-url") {
		t.Fatal("disconnected doctl kept a token or redirected")
	}
	if !strings.Contains(do.Stderr, egresscontract.NotConnected) {
		t.Fatal(do.Stderr)
	}
	auth := Plan("doctl", []string{"auth", "init"}, envWithToken())
	if len(auth.Argv) != 0 {
		t.Fatal("doctl auth was executed")
	}
}

func TestAuthFlagsDoNotBypass(t *testing.T) {
	withStatus(t, egresscontract.Status{})
	refused := []struct {
		name string
		args []string
	}{
		{"doctl output", []string{"doctl", "--output", "json", "auth", "init"}},
		{"doctl context", []string{"doctl", "--context", "default", "auth", "init"}},
		{"doctl dash", []string{"doctl", "--", "auth", "init"}},
		{"doctl short output", []string{"doctl", "-o", "json", "auth", "list"}},
		{"doctl verbose", []string{"doctl", "--verbose", "auth", "list"}},
		{"gh hostname", []string{"gh", "--hostname", "github.com", "auth", "token"}},
		{"gh short hostname", []string{"gh", "-h", "github.com", "auth", "token"}},
		{"gh dash", []string{"gh", "--", "auth", "login"}},
		{"gh between", []string{"gh", "auth", "--hostname", "github.com", "token"}},
		{"gh protocol", []string{"gh", "--git-protocol", "https", "auth", "login"}},
		{"gh with token", []string{"gh", "--with-token"}},
		{"gh with token value", []string{"gh", "auth", "login", "--with-token=true"}},
	}
	for _, tc := range refused {
		act := Plan(tc.name[:strings.Index(tc.name, " ")], tc.args[1:], envWithToken())
		if len(act.Argv) != 0 || !strings.Contains(act.Stderr, "disabled") || strings.Contains(joined(act), fixtureValue) {
			t.Fatalf("%s argv %v stderr %s", tc.name, act.Argv, act.Stderr)
		}
	}
	kept := Plan("gh", []string{"auth", "status"}, envWithToken())
	if len(kept.Argv) == 0 || strings.Contains(kept.Stderr, "disabled") {
		t.Fatal("gh auth status was refused")
	}
	plain := Plan("doctl", []string{"--output", "json", "account", "get"}, envWithToken())
	if len(plain.Argv) == 0 || !strings.Contains(strings.Join(plain.Argv, " "), "account get") {
		t.Fatalf("account get %v", plain.Argv)
	}
	tok := "guest-token-value"
	stripped := Plan("doctl", []string{"account", "get", "--", "--access-token", tok, "-t", tok, "--access-token=" + tok}, envWithToken())
	if strings.Contains(joined(stripped), tok) || strings.Contains(strings.Join(stripped.Argv, " "), "--access-token") || strings.Contains(strings.Join(stripped.Argv, " "), "-t") {
		t.Fatalf("token flag survived %v", stripped.Argv)
	}
}

func TestCLIsRedirectWhenLive(t *testing.T) {
	live := egresscontract.Status{Enabled: true, Providers: []string{"github", "digitalocean"}}
	withStatus(t, live)
	gh := Plan("gh", []string{"api", "user", "-H", "Authorization: Bearer " + fixtureValue}, envWithToken())
	if gh.GH == nil || gh.GH.Path != "/user" {
		t.Fatalf("%+v", gh.GH)
	}
	if _, ok := gh.GH.Headers["authorization"]; ok || strings.Contains(joined(gh), fixtureValue) {
		t.Fatal("gh api kept an authorization header or the env value")
	}
	off := Plan("gh", []string{"api", "https://api.github.com/user"}, envWithToken())
	if off.GH != nil {
		t.Fatal("absolute gh api url was redirected")
	}
	do := Plan("doctl", []string{"account", "get", "--access-token", fixtureValue, "--api-url", "https://api.digitalocean.com"}, envWithToken())
	text := strings.Join(do.Argv, " ")
	if !strings.Contains(text, "--api-url "+DefaultEgress+"/upstream/digitalocean/") {
		t.Fatal(text)
	}
	if !strings.Contains(text, "--access-token "+egresscontract.DummyCredential) {
		t.Fatal(text)
	}
	if strings.Contains(joined(do), fixtureValue) || strings.Contains(text, "api.digitalocean.com") {
		t.Fatal("doctl kept the env value or the public API")
	}
	if !hasEnv(do.Env, "NO_PROXY", "127.0.0.1,localhost") || !hasEnv(do.Env, "no_proxy", "127.0.0.1,localhost") {
		t.Fatalf("live doctl env %v", do.Env)
	}
	for _, item := range do.Env {
		if strings.HasPrefix(item, "DIGITALOCEAN_ACCESS_TOKEN=") && item != "DIGITALOCEAN_ACCESS_TOKEN="+egresscontract.DummyCredential {
			t.Fatal("doctl env token was not the placeholder")
		}
	}
}

func TestMCPFailClosed(t *testing.T) {
	var out strings.Builder
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"cast","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"request_secret","arguments":{"connection":"github","reason":"need a repository"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"request_secret","arguments":{"connection":"aws","reason":"` + fixtureValue + `"}}}`,
	}, "\n") + "\n"
	start := time.Now()
	if err := ServeMCP("request_secret", "http://127.0.0.1:1", strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("request_secret hung")
	}
	text := out.String()
	if strings.Contains(text, fixtureValue) {
		t.Fatal("shim echoed the env value")
	}
	if !strings.Contains(text, egresscontract.NotConnected) || !strings.Contains(text, egresscontract.Rejected) || !strings.Contains(text, "request_secret") {
		t.Fatal(text)
	}
	out.Reset()
	in = `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	if err := ServeMCP("github", "http://127.0.0.1:1", strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), egresscontract.NotConnected) || strings.Contains(out.String(), fixtureValue) {
		t.Fatal(out.String())
	}
}

func TestMCPReturnsHostResultOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if strings.Contains(string(raw), fixtureValue) || strings.Contains(r.Header.Get("Authorization"), fixtureValue) {
			t.Errorf("shim sent the env value")
		}
		if strings.HasSuffix(r.URL.Path, "/secret-request") {
			_, _ = io.WriteString(w, `{"result":"saved","leak":"`+fixtureValue+`"}`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":"not_connected","message":"`+fixtureValue+`"}`)
	}))
	defer srv.Close()
	var out strings.Builder
	line := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"request_secret","arguments":{"connection":"digitalocean","reason":"list droplets"}}}` + "\n"
	if err := ServeMCP("request_secret", srv.URL, strings.NewReader(line), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), fixtureValue) || !strings.Contains(out.String(), "saved") {
		t.Fatal(out.String())
	}
	var msg struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &msg); err != nil {
		t.Fatal(err)
	}
	if len(msg.Result.Content) != 1 || msg.Result.Content[0].Text != "saved" {
		t.Fatal(out.String())
	}
}
