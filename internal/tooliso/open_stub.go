//go:build !linux

package tooliso

// ReadUnder is refused off Linux.
func ReadUnder(root, rel string) ([]byte, error) { return nil, ErrPath }

// ListUnder is refused off Linux.
func ListUnder(root, pattern string) ([]string, error) { return nil, ErrPath }
