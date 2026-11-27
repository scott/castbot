package update

import (
	"context"
	"errors"
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
	// Apply starts the root helper. Nil records the release and does not install.
	Apply func(context.Context) error
	// Inflight is the number of queued, streaming, or waiting runs.
	// Automatic mode does not install when this is greater than zero.
	Inflight func(context.Context) (int, error)
}

// Run checks once shortly after startup, then about six hours after each
// success. A failed check keeps the previous cache and tries again after
// RetryEvery, so a short outage does not wait six hours and does not spin.
// off and a dev build do not fetch. Automatic mode starts Apply only when a
// release is available and Inflight reports zero. A busy agent waits for the
// next pass. A nil Apply does not install.
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
			case "installing":
				log.Printf("update check: installing")
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

// Probe records one release check and does not install.
// off and a dev build do not fetch. A failed check leaves the previous cache.
func Probe(ctx context.Context, cfg RunConfig) error {
	_, _, err := cfg.record(ctx)
	return err
}

func (cfg RunConfig) once(ctx context.Context) (string, error) {
	mode, status, err := cfg.record(ctx)
	if err != nil || status != "available" || mode != ModeAuto || cfg.Apply == nil {
		return status, err
	}
	if cfg.Inflight == nil {
		return "", errors.New("inflight check failed")
	}
	n, err := cfg.Inflight(ctx)
	if err != nil || n < 0 {
		return "", errors.New("inflight check failed")
	}
	if n > 0 {
		return "available", nil
	}
	if err := cfg.Apply(ctx); err != nil {
		return "", errors.New("update did not start")
	}
	return "installing", nil
}

func (cfg RunConfig) record(ctx context.Context) (string, string, error) {
	mode, err := cfg.mode(ctx)
	if err != nil {
		return "", "", err
	}
	if mode == ModeOff || cfg.Version == "dev" || cfg.Version == "" {
		return mode, "skipped", nil
	}
	if cfg.CachePath == "" {
		cfg.CachePath = DefaultCachePath
	}
	cache, err := cfg.Checker.Check(ctx, State{
		Version: cfg.Version,
		Commit:  cfg.Commit,
		Schema:  cfg.Schema,
		Image:   cfg.Image,
		Mode:    mode,
	})
	if err != nil {
		return "", "", err
	}
	if err := WriteCache(cfg.CachePath, cache); err != nil {
		return "", "", err
	}
	status := "current"
	if cache.ImageBlocked {
		status = "blocked"
	} else if cache.Available {
		status = "available"
	}
	return mode, status, nil
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
