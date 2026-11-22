package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/config"
	"github.com/scott/castbot/internal/store"
	"github.com/scott/castbot/internal/web"
)

func TestAttachMachinePartialConfigStaysOff(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"), store.Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	app := web.New(st, []byte(strings.Repeat("ab", 32)), nil)
	t.Cleanup(app.Close)

	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	attachMachine(app, config.Config{})
	if buf.Len() != 0 {
		t.Fatalf("empty config logged %q", buf.String())
	}

	attachMachine(app, config.Config{Image: "example.invalid/castbot-runtime:abc"})
	if !strings.Contains(buf.String(), "machine host stays off") {
		t.Fatalf("image only: %q", buf.String())
	}
	if strings.Contains(buf.String(), "machine host enabled") {
		t.Fatalf("image only enabled the host: %q", buf.String())
	}

	buf.Reset()
	attachMachine(app, config.Config{PublicURL: "https://bot.example.com"})
	if !strings.Contains(buf.String(), "machine host stays off") || strings.Contains(buf.String(), "machine host enabled") {
		t.Fatalf("url only: %q", buf.String())
	}

	buf.Reset()
	attachMachine(app, config.Config{
		MachineToken: "machine-token-for-tests",
		Image:        "example.invalid/castbot-runtime:abc",
		PublicURL:    "https://bot.example.com",
	})
	if !strings.Contains(buf.String(), "machine host enabled") {
		t.Fatalf("complete config: %q", buf.String())
	}
	if strings.Contains(buf.String(), "machine-token-for-tests") {
		t.Fatal("token was logged")
	}
}
