package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/scott/castbot/internal/store"
	"github.com/scott/castbot/internal/update"
)

func updateCmd(args []string) {
	if len(args) != 1 || args[0] != "apply" {
		fmt.Fprintln(os.Stderr, "usage: castbot update apply")
		os.Exit(2)
	}
	cfg, err := applyConfigFromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "update apply: refused")
		os.Exit(1)
	}
	if err := update.Apply(context.Background(), cfg); err != nil {
		os.Exit(1)
	}
}

// startUpdateHelper asks sudo to run the root helper and returns without waiting.
// The service account is not root. sudoers allows this one command and passes no arguments.
// The request context is not used, so a disconnected browser does not stop the install.
func startUpdateHelper() error {
	cmd := helperCommand()
	return cmd.Start()
}

func helperCommand() *exec.Cmd {
	cmd := exec.Command("sudo", "-n", "--", update.WrapperPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}

func startHelper(path string, args ...string) (*exec.Cmd, error) {
	cmd := exec.Command(path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func applyConfigFromEnv() (update.ApplyConfig, error) {
	cfg := update.ApplyConfig{
		Version: version,
		Commit:  commit,
		Schema:  store.SchemaVersion,
	}
	root := os.Getenv("CASTBOT_APPLY_ROOT")
	if root == "" {
		return cfg, nil
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return cfg, errors.New("refused")
	}
	releaseBase := os.Getenv("CASTBOT_APPLY_RELEASE_BASE")
	compareBase := os.Getenv("CASTBOT_APPLY_COMPARE_BASE")
	if err := loopbackBase(releaseBase); err != nil {
		return cfg, errors.New("refused")
	}
	if err := loopbackBase(compareBase); err != nil {
		return cfg, errors.New("refused")
	}
	pubPath := os.Getenv("CASTBOT_APPLY_PUBKEY")
	key, err := os.ReadFile(pubPath)
	if err != nil || len(key) == 0 || len(key) > 4096 {
		return cfg, errors.New("refused")
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("redirect")
			}
			if req.URL.Scheme != "http" || req.URL.Hostname() != "127.0.0.1" || req.URL.User != nil {
				return errors.New("redirect")
			}
			return nil
		},
	}
	cfg.Root = filepath.Join(root, "opt", "castbot")
	cfg.EnvFile = filepath.Join(root, "etc", "castbot", "castbot.env")
	cfg.Checker = update.Checker{
		Client:      client,
		ReleaseBase: releaseBase,
		CompareBase: compareBase,
		Key:         key,
	}
	if raw := os.Getenv("CASTBOT_APPLY_REGISTRY"); raw != "" {
		if err := loopbackBase(raw); err != nil {
			return cfg, errors.New("refused")
		}
		cfg.Checker.RegistryBase = raw
	}
	if raw := os.Getenv("CASTBOT_APPLY_HEALTH"); raw != "" {
		if err := loopbackHealth(raw); err != nil {
			return cfg, errors.New("refused")
		}
		cfg.HealthURL = raw
	}
	if raw := os.Getenv("CASTBOT_APPLY_HEALTH_WAIT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 || d > 30*time.Second {
			return cfg, errors.New("refused")
		}
		cfg.HealthFor = d
	}
	if raw := os.Getenv("CASTBOT_APPLY_NOW"); raw != "" {
		now, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return cfg, errors.New("refused")
		}
		cfg.Now = func() time.Time { return now }
	}
	owner := os.Getenv("CASTBOT_APPLY_OWNER")
	group := os.Getenv("CASTBOT_APPLY_GROUP")
	if (owner == "") != (group == "") {
		return cfg, errors.New("refused")
	}
	if owner != "" {
		cfg.Owner = owner
		cfg.Group = group
	}
	cfg.Restart = func(ctx context.Context) error {
		return testRestart(ctx, root)
	}
	cfg.Exe = func(context.Context) (string, error) {
		return testExe(root)
	}
	return cfg, nil
}

func loopbackBase(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("refused")
	}
	return nil
}

func loopbackHealth(raw string) error {
	if err := loopbackBase(raw); err != nil {
		return err
	}
	u, err := url.Parse(raw)
	if err != nil || u.Path != "/login" {
		return errors.New("refused")
	}
	return nil
}

func testRestart(ctx context.Context, root string) error {
	cmd := exec.CommandContext(ctx, "systemctl", "restart", "castbot")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"LANG=C",
		"CASTBOT_APPLY_ROOT=" + root,
	}
	if err := cmd.Run(); err != nil {
		return errors.New("restart failed")
	}
	return nil
}

func testExe(root string) (string, error) {
	body, err := os.ReadFile(filepath.Join(root, "run", "exe"))
	if err != nil {
		return "", errors.New("running binary was unexpected")
	}
	exe := strings.TrimSpace(string(body))
	if exe == "" || strings.ContainsAny(exe, "\r\n") {
		return "", errors.New("running binary was unexpected")
	}
	return exe, nil
}
