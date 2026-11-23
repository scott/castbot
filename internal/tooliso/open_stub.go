//go:build !linux

package tooliso

import "os"

// ReadUnder is refused off Linux.
func ReadUnder(root, rel string) ([]byte, error) { return nil, ErrPath }

// ListUnder is refused off Linux.
func ListUnder(root, pattern string) ([]string, error) { return nil, ErrPath }

// CreateUnder is refused off Linux.
func CreateUnder(root, rel string, flags int, mode uint32) (*os.File, error) {
	return nil, ErrPath
}

// OpenDirUnder is refused off Linux.
func OpenDirUnder(root, rel string) (*os.File, error) { return nil, ErrPath }

// MkdirAllUnder is refused off Linux.
func MkdirAllUnder(root, rel string, mode uint32) error { return ErrPath }

// RenameUnder is refused off Linux.
func RenameUnder(root, oldRel, newRel string) error { return ErrPath }

// RemoveUnder is refused off Linux.
func RemoveUnder(root, rel string) error { return ErrPath }

func mkdirNoFollow(path string, mode os.FileMode) error { return ErrPath }

// WriteFresh is refused off Linux.
func WriteFresh(dir, name string, body []byte, mode uint32) error { return ErrPath }

func listDirNoFollow(path string) ([]os.DirEntry, error) { return nil, ErrPath }

func removeNameNoFollow(dir, name string) error { return ErrPath }
