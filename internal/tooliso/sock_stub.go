//go:build !linux

package tooliso

import (
	"context"
	"errors"
)

func EnsureServe(ctx context.Context) error { return errors.New("tool server") }

func Proxy(file bool, args []string) int { return 126 }
