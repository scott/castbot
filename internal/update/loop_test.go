package update

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestOnceAutoInstallsOnlyWhenIdle(t *testing.T) {
	running := "1111111111111111111111111111111111111111"
	release := "2222222222222222222222222222222222222222"
	dir, pub := packRelease(t, "0.20261124.2", release, 37)
	srv := serveRelease(t, dir, "ahead")
	defer srv.Close()
	when := time.Date(2026, 11, 24, 18, 0, 0, 0, time.UTC)

	newCfg := func(mode string) (RunConfig, *int) {
		calls := 0
		cfg := RunConfig{
			Version:   "0.20261124.1",
			Commit:    running,
			Schema:    37,
			EnvMode:   mode,
			CachePath: filepath.Join(t.TempDir(), "update.json"),
			Checker:   *checkerFor(srv, pub, when),
			Inflight: func(context.Context) (int, error) {
				return 0, nil
			},
			Apply: func(context.Context) error {
				calls++
				return nil
			},
		}
		return cfg, &calls
	}

	t.Run("idle", func(t *testing.T) {
		cfg, calls := newCfg(ModeAuto)
		status, err := cfg.once(context.Background())
		if err != nil || status != "installing" || *calls != 1 {
			t.Fatalf("status %s err %v calls %d", status, err, *calls)
		}
	})

	t.Run("busy", func(t *testing.T) {
		cfg, calls := newCfg(ModeAuto)
		cfg.Inflight = func(context.Context) (int, error) { return 1, nil }
		status, err := cfg.once(context.Background())
		if err != nil || status != "available" || *calls != 0 {
			t.Fatalf("status %s err %v calls %d", status, err, *calls)
		}
	})

	t.Run("prompt", func(t *testing.T) {
		cfg, calls := newCfg(ModePrompt)
		status, err := cfg.once(context.Background())
		if err != nil || status != "available" || *calls != 0 {
			t.Fatalf("status %s err %v calls %d", status, err, *calls)
		}
	})

	t.Run("nil apply", func(t *testing.T) {
		cfg, _ := newCfg(ModeAuto)
		cfg.Apply = nil
		status, err := cfg.once(context.Background())
		if err != nil || status != "available" {
			t.Fatalf("status %s err %v", status, err)
		}
	})

	t.Run("stored auto", func(t *testing.T) {
		cfg, calls := newCfg("")
		cfg.StoredMode = func(context.Context) (string, error) { return ModeAuto, nil }
		status, err := cfg.once(context.Background())
		if err != nil || status != "installing" || *calls != 1 {
			t.Fatalf("status %s err %v calls %d", status, err, *calls)
		}
	})

	t.Run("env off wins", func(t *testing.T) {
		cfg, calls := newCfg(ModeOff)
		cfg.StoredMode = func(context.Context) (string, error) { return ModeAuto, nil }
		status, err := cfg.once(context.Background())
		if err != nil || status != "skipped" || *calls != 0 {
			t.Fatalf("status %s err %v calls %d", status, err, *calls)
		}
	})

	t.Run("dev", func(t *testing.T) {
		cfg, calls := newCfg(ModeAuto)
		cfg.Version = "dev"
		status, err := cfg.once(context.Background())
		if err != nil || status != "skipped" || *calls != 0 {
			t.Fatalf("status %s err %v calls %d", status, err, *calls)
		}
	})

	t.Run("current", func(t *testing.T) {
		cfg, calls := newCfg(ModeAuto)
		cfg.Version = "0.20261124.2"
		status, err := cfg.once(context.Background())
		if err != nil || status != "current" || *calls != 0 {
			t.Fatalf("status %s err %v calls %d", status, err, *calls)
		}
	})

	t.Run("blocked", func(t *testing.T) {
		cfg, calls := newCfg(ModeAuto)
		cfg.Image = "ghcr.io/scott/castbot-runtime:abcd"
		status, err := cfg.once(context.Background())
		if err != nil || status != "blocked" || *calls != 0 {
			t.Fatalf("status %s err %v calls %d", status, err, *calls)
		}
	})

	t.Run("apply error", func(t *testing.T) {
		cfg, calls := newCfg(ModeAuto)
		cfg.Apply = func(context.Context) error {
			*calls++
			return errors.New("secret-token")
		}
		status, err := cfg.once(context.Background())
		if err == nil || status != "" || *calls != 1 {
			t.Fatalf("status %s err %v calls %d", status, err, *calls)
		}
		if err.Error() != "update did not start" {
			t.Fatalf("err %v", err)
		}
	})

	t.Run("inflight error", func(t *testing.T) {
		cfg, calls := newCfg(ModeAuto)
		cfg.Inflight = func(context.Context) (int, error) {
			return 0, errors.New("secret-token")
		}
		status, err := cfg.once(context.Background())
		if err == nil || status != "" || *calls != 0 {
			t.Fatalf("status %s err %v calls %d", status, err, *calls)
		}
		if err.Error() != "inflight check failed" {
			t.Fatalf("err %v", err)
		}
	})

	t.Run("nil inflight", func(t *testing.T) {
		cfg, calls := newCfg(ModeAuto)
		cfg.Inflight = nil
		status, err := cfg.once(context.Background())
		if err == nil || status != "" || *calls != 0 {
			t.Fatalf("status %s err %v calls %d", status, err, *calls)
		}
	})
}
