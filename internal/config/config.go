// Package config loads the castbot env file.
package config

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	defaultAddr = "127.0.0.1:8080"
	defaultDB   = "data/castbot.db"
	defaultFile = "castbot.env"
)

// Config is the process configuration. The session key is read only from the env file.
type Config struct {
	EnvFile    string
	Addr       string
	DBPath     string
	RuntimeURL string
	SessionKey []byte
}

// Load reads CASTBOT_ENV_FILE (or castbot.env). The file must be mode 0600.
// CASTBOT_ADDR and CASTBOT_DB in the process environment override the file.
// The session key is never taken from the process environment.
func Load() (Config, error) {
	path := os.Getenv("CASTBOT_ENV_FILE")
	if path == "" {
		path = defaultFile
	}
	return LoadFile(path)
}

// LoadFile reads one env file.
func LoadFile(path string) (Config, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return Config{}, fmt.Errorf("env file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return Config{}, errors.New("env file must be a regular file")
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		return Config{}, fmt.Errorf("env file %s is mode %o, want 0600", path, perm)
	}
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()

	vals := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return Config{}, errors.New("env file has a line without an equals sign")
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		vals[key] = val
	}
	if err := sc.Err(); err != nil {
		return Config{}, err
	}
	raw := vals["CASTBOT_SESSION_KEY"]
	key, err := hex.DecodeString(raw)
	if err != nil || len(key) != 32 {
		return Config{}, errors.New("CASTBOT_SESSION_KEY must be 64 hex characters")
	}
	if allZero(key) {
		return Config{}, errors.New("CASTBOT_SESSION_KEY must not be all zeros")
	}
	cfg := Config{
		EnvFile:    path,
		Addr:       vals["CASTBOT_ADDR"],
		DBPath:     vals["CASTBOT_DB"],
		RuntimeURL: vals["CASTBOT_RUNTIME_URL"],
		SessionKey: key,
	}
	if cfg.Addr == "" {
		cfg.Addr = defaultAddr
	}
	if cfg.DBPath == "" {
		cfg.DBPath = defaultDB
	}
	if v := os.Getenv("CASTBOT_ADDR"); v != "" {
		cfg.Addr = v
	}
	if v := os.Getenv("CASTBOT_DB"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("CASTBOT_RUNTIME_URL"); v != "" {
		cfg.RuntimeURL = v
	}
	return cfg, nil
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
