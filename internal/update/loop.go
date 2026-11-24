package update

import (
	"context"
	"log"
	"time"
)

// RunConfig is one installed process checking for a release.
// StoredMode is read when EnvMode is unset. A nil StoredMode means no row.
type RunConfig struct {
	Version      string
	Commit       string
	Schema       int
	Image        string
	EnvMode      string
	CachePath    string
	StoredMode   func(context.Context) (string, error)
	Startup      time.Duration
	SuccessEvery time.Duration
	RetryEvery   time.Duration
	Checker      Checker
}

// Run checks once shortly after startup, then about six hours after each
// success. A failed check keeps the previous cache and tries again after
// RetryEvery, so a short outage does not wait six hours and does not spin.
// off and a dev build do not fetch. This loop does not install a release.
func Run(ctx context.Context, cfg RunConfig) {
	startup := cfg.Startup
	if startup <= 0 {
		startup = 20 * time.Second
	}
	successEvery := cfg.SuccessEvery
	if successEvery <= 0 {
		successEvery = 6 * time.Hour
	}
	retryEvery := cfg.RetryEvery
	if retryEvery <= 0 {
		retryEvery = 15 * time.Minute
	}
	if cfg.CachePath == "" {
		cfg.CachePath = DefaultCachePath
	}
	if !sleep(ctx, startup) {
		return
	}
	for {
		status, err := cfg.once(ctx)
		wait := successEvery
		if err != nil {
			log.Printf("update check failed: %s", err.Error())
			wait = retryEvery
		} else {
			switch status {
			case "available":
				log.Printf("update check: a release is available")
			case "blocked":
				log.Printf("update check: machine image is not published")
			case "current":
				log.Printf("update check: up to date")
			}
		}
		if !sleep(ctx, wait) {
			return
		}
	}
}

func (cfg RunConfig) once(ctx context.Context) (string, error) {
	mode, err := cfg.mode(ctx)
	if err != nil {
		return "", err
	}
	if mode == ModeOff || cfg.Version == "dev" || cfg.Version == "" {
		return "skipped", nil
	}
	cache, err := cfg.Checker.Check(ctx, State{
		Version: cfg.Version,
		Commit:  cfg.Commit,
		Schema:  cfg.Schema,
		Image:   cfg.Image,
		Mode:    mode,
	})
	if err != nil {
		return "", err
	}
	if err := WriteCache(cfg.CachePath, cache); err != nil {
		return "", err
	}
	if cache.Available {
		return "available", nil
	}
	if cache.ImageBlocked {
		return "blocked", nil
	}
	return "current", nil
}

func (cfg RunConfig) mode(ctx context.Context) (string, error) {
	if _, ok := ParseMode(cfg.EnvMode); ok {
		return cfg.EnvMode, nil
	}
	stored := ""
	if cfg.StoredMode != nil {
		var err error
		stored, err = cfg.StoredMode(ctx)
		if err != nil {
			return "", err
		}
	}
	return ResolveMode("", stored), nil
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
