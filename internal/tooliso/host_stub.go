//go:build !linux

package tooliso

import (
	"errors"
	"io"
	"os"
)

// RuntimeHost is unused off Linux.
type RuntimeHost struct{}

// StartRuntimeHost is refused off Linux.
func StartRuntimeHost(argv []string) (*RuntimeHost, error) {
	return nil, errors.New("supervisor")
}

// RunSupervisor is refused off Linux.
func RunSupervisor() { os.Exit(1) }

// Logs is unused off Linux.
func (h *RuntimeHost) Logs() io.Reader { return nil }

// Start is refused off Linux.
func (h *RuntimeHost) Start(env []string) (<-chan struct{}, error) {
	return nil, errors.New("supervisor")
}

// Stop is a no-op off Linux.
func (h *RuntimeHost) Stop() error { return nil }

// Close is a no-op off Linux.
func (h *RuntimeHost) Close() {}
