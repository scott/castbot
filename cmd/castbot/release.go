package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/scott/castbot/internal/store"
	"github.com/scott/castbot/internal/update"
)

func releaseCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: castbot release pack")
		os.Exit(2)
	}
	switch args[0] {
	case "pack":
		releasePack(args[1:])
	default:
		fmt.Fprintln(os.Stderr, "usage: castbot release pack")
		os.Exit(2)
	}
}

func releasePack(args []string) {
	fs := flag.NewFlagSet("pack", flag.ExitOnError)
	version := fs.String("version", "", "release version, MAJOR.MINOR.PATCH")
	commit := fs.String("commit", "", "full commit")
	guest := fs.String("guest-id", "", "guest input id")
	image := fs.String("runtime-image", "", "public runtime image")
	imageCommit := fs.String("runtime-image-commit", "", "commit of the runtime image")
	notes := fs.String("notes", "", "release notes url")
	amd := fs.String("amd64", "", "linux/amd64 binary")
	arm := fs.String("arm64", "", "linux/arm64 binary")
	dir := fs.String("dir", "", "output directory")
	keyPath := fs.String("key", "", "minisign private key file")
	_ = fs.Parse(args)
	if *dir == "" || *keyPath == "" {
		fmt.Fprintln(os.Stderr, "usage: castbot release pack -dir DIR -key KEY")
		os.Exit(2)
	}
	key, err := os.ReadFile(*keyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "release: could not read the signing key")
		os.Exit(1)
	}
	err = update.Pack(*dir, update.Input{
		Version:            *version,
		Commit:             *commit,
		Schema:             store.SchemaVersion,
		GuestID:            *guest,
		RuntimeImage:       *image,
		RuntimeImageCommit: *imageCommit,
		Notes:              *notes,
		AMD64:              *amd,
		ARM64:              *arm,
	}, key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "release: %s\n", err)
		os.Exit(1)
	}
}
