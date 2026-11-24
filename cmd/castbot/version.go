package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/scott/castbot/internal/store"
)

// version, commit, and guestID are set with -ldflags -X.
// Package main is linked as main, so the symbols are main.version, main.commit, and main.guestID.
// A local build keeps these defaults.
var (
	version = "dev"
	commit  = "unknown"
	guestID = ""
)

func versionCmd(args []string) {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: castbot version")
		os.Exit(2)
	}
	fmt.Fprint(os.Stdout, versionText())
}

func versionText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "castbot %s\n", displayVersion(version))
	fmt.Fprintf(&b, "commit %s\n", commit)
	fmt.Fprintf(&b, "schema %d\n", store.SchemaVersion)
	if guestID != "" {
		fmt.Fprintf(&b, "guest %s\n", guestID)
	}
	return b.String()
}

func displayVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "dev" {
		return "dev"
	}
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}

func logVersion() {
	log.Printf("castbot %s commit %s schema %d", displayVersion(version), commit, store.SchemaVersion)
}
