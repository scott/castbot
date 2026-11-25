package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/store"
)

func TestDisplayVersion(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "dev"},
		{"dev", "dev"},
		{"0.20261124.1", "v0.20261124.1"},
		{"v0.20261124.2", "v0.20261124.2"},
		{"0.0.0+aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "v0.0.0+aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	for _, tc := range cases {
		if got := displayVersion(tc.in); got != tc.want {
			t.Fatalf("displayVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestVersionTextDev(t *testing.T) {
	prevV, prevC, prevG := version, commit, guestID
	t.Cleanup(func() {
		version, commit, guestID = prevV, prevC, prevG
	})
	version, commit, guestID = "dev", "unknown", ""
	got := versionText()
	want := fmt.Sprintf("castbot dev\ncommit unknown\nschema %d\n", store.SchemaVersion)
	if got != want {
		t.Fatalf("version text:\n%s", got)
	}
	if strings.Contains(got, "guest ") {
		t.Fatalf("guest line on a dev build:\n%s", got)
	}
}

func TestVersionTextRelease(t *testing.T) {
	prevV, prevC, prevG := version, commit, guestID
	t.Cleanup(func() {
		version, commit, guestID = prevV, prevC, prevG
	})
	version = "0.20261124.1"
	commit = "0123456789abcdef0123456789abcdef01234567"
	guestID = "abc123"
	got := versionText()
	for _, line := range []string{
		"castbot v0.20261124.1\n",
		"commit 0123456789abcdef0123456789abcdef01234567\n",
		"guest abc123\n",
	} {
		if !strings.Contains(got, line) {
			t.Fatalf("version text missing %q:\n%s", line, got)
		}
	}
}
