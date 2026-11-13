package main

import (
	"os"
	"path/filepath"

	"github.com/scott/castbot/internal/guestwrap"
)

func main() {
	os.Exit(guestwrap.Run(filepath.Base(os.Args[0]), os.Args[1:], os.Environ(), os.Stdin, os.Stdout, os.Stderr))
}
