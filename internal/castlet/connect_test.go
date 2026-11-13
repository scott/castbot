package castlet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/connect"
)

func TestPullRejectsAConnectionName(t *testing.T) {
	const canary = "test-connection-value"
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"secrets":[{"name":"` + connect.SecretNameGitHub + `","value":"` + canary + `"}]}`))
	}))
	t.Cleanup(cp.Close)
	sup, err := New(Config{
		Token: "machine-token", ControlURL: cp.URL, AllowHTTP: true,
		AgentID: "agt", AgentDir: t.TempDir(), RuntimeURL: "http://127.0.0.1:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sup.pullSecrets(context.Background(), "machine-token"); err == nil {
		t.Fatal("connection name was accepted")
	}
	sup.mu.Lock()
	sup.secrets = []secret{{Name: connect.SecretNameGitHub, Value: canary}, {Name: "SAFE_TOKEN", Value: "safe-env-value"}}
	sup.mu.Unlock()
	env := strings.Join(sup.childEnv(), "\n")
	if strings.Contains(env, canary) || strings.Contains(env, connect.SecretNameGitHub+"=") {
		t.Fatal("child env")
	}
	if !strings.Contains(env, "SAFE_TOKEN=safe-env-value") {
		t.Fatal("ordinary secret dropped")
	}
	for _, name := range []string{"GH_TOKEN=", "GITHUB_PERSONAL_ACCESS_TOKEN=", "DIGITALOCEAN_API_TOKEN=", "GITHUB_TOKEN="} {
		if strings.Contains(env, name) {
			t.Fatal(name)
		}
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "log")
	cmd := exec.Command("/bin/sh", "-c", `env > "$1"`, "sh", logPath)
	cmd.Env = sup.childEnv()
	cmd.Stdout = sup.logs
	cmd.Stderr = sup.logs
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw) + string(sup.logs.Bytes())
	if strings.Contains(text, canary) || strings.Contains(text, connect.SecretNameGitHub+"=") {
		t.Fatal("runtime log held the connection secret")
	}
}
