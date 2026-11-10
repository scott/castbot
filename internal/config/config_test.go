package config

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeSessionKey() string {
	return hex.EncodeToString(bytes.Repeat([]byte{0x5a}, 32))
}

func TestRejectsLoosePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "castbot.env")
	body := "CASTBOT_SESSION_KEY=" + fakeSessionKey() + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("expected mode 0644 to be rejected")
	}
}

func TestLoadKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "castbot.env")
	body := "" +
		"# placeholder comment\n" +
		"CASTBOT_SESSION_KEY=" + fakeSessionKey() + "\n" +
		"CASTBOT_ADDR=127.0.0.1:9\n" +
		"CASTBOT_DB=" + filepath.Join(dir, "t.db") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.SessionKey) != 32 || cfg.Addr != "127.0.0.1:9" {
		t.Fatalf("cfg %+v", cfg.Addr)
	}
	t.Setenv("CASTBOT_ADDR", "127.0.0.1:10")
	cfg, err = LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:10" {
		t.Fatalf("addr override %s", cfg.Addr)
	}
}

func TestIgnoresAmbientDigitalOceanToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "castbot.env")
	body := "CASTBOT_SESSION_KEY=" + fakeSessionKey() + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "ambient-access-token")
	t.Setenv("DIGITALOCEAN_TOKEN", "ambient-token")
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MachineToken != "" || cfg.Image != "" || cfg.PublicURL != "" {
		t.Fatalf("ambient token was adopted: %#v", cfg.MachineToken)
	}
	t.Setenv("CASTBOT_DO_TOKEN", "castbot-file-token")
	cfg, err = LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MachineToken != "castbot-file-token" {
		t.Fatalf("env token %q", cfg.MachineToken)
	}
	if strings.Contains(cfg.MachineToken, "ambient") {
		t.Fatal("ambient token leaked into the machine token")
	}
}

func TestRejectDevEchoOnPublicHTTPS(t *testing.T) {
	cfg := Config{DevEcho: true, PublicURL: "https://castbot.example"}
	if err := cfg.RejectDevEcho(); err == nil {
		t.Fatal("expected refusal")
	}
	cfg.PublicURL = "https://localhost:8080"
	if err := cfg.RejectDevEcho(); err != nil {
		t.Fatal(err)
	}
	cfg.PublicURL = "https://127.0.0.1"
	if err := cfg.RejectDevEcho(); err != nil {
		t.Fatal(err)
	}
	cfg.PublicURL = "http://castbot.example"
	if err := cfg.RejectDevEcho(); err != nil {
		t.Fatal(err)
	}
	cfg.DevEcho = false
	cfg.PublicURL = "https://castbot.example"
	if err := cfg.RejectDevEcho(); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsZeroKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "castbot.env")
	body := "CASTBOT_SESSION_KEY=" + strings.Repeat("0", 64) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("expected zero key to be rejected")
	}
}
