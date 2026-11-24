package update

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// DefaultCachePath is the last good check on an installed host.
const DefaultCachePath = "/opt/castbot/update.json"

// Cache is the last successful check. It holds no secrets.
// Available is set only when the release is newer, signed, comparable,
// and the machine image policy allows the update.
// ImageMoves means the public image line would change.
// ImageBlocked means a newer release exists and the machine image has to
// be published before the update can run.
type Cache struct {
	Available    bool   `json:"available"`
	Version      string `json:"version"`
	Commit       string `json:"commit"`
	Notes        string `json:"notes"`
	ImageMoves   bool   `json:"image_moves"`
	ImageBlocked bool   `json:"image_blocked"`
	CheckedAt    string `json:"checked_at"`
}

// ReadCache reads a mode 0600 cache. A missing or empty file is a zero Cache.
func ReadCache(path string) (Cache, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Cache{}, nil
		}
		return Cache{}, err
	}
	if err := cacheOwner(fi); err != nil {
		return Cache{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Cache{}, err
	}
	if len(raw) == 0 {
		return Cache{}, nil
	}
	var c Cache
	if err := json.Unmarshal(raw, &c); err != nil {
		return Cache{}, errors.New("update cache is invalid")
	}
	if err := c.usable(); err != nil {
		return Cache{}, err
	}
	return c, nil
}

// usable rejects a cache that names a release but not the public notes URL.
// A file the service can rewrite must not become a link to somewhere else.
func (c Cache) usable() error {
	if !c.Available && !c.ImageBlocked {
		return nil
	}
	if !versionRE.MatchString(c.Version) || !shaRE.MatchString(c.Commit) {
		return errors.New("update cache is invalid")
	}
	if c.Notes != "https://github.com/scott/castbot/releases/tag/v"+c.Version {
		return errors.New("update cache is invalid")
	}
	return nil
}

// WriteCache stores the check. It replaces the file when the directory is
// writable, and otherwise rewrites an existing mode 0600 file in place.
func WriteCache(path string, c Cache) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := writeAtomic(path, raw); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrPermission) {
		return err
	}
	return writeInPlace(path, raw)
}

func writeAtomic(path string, raw []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".update-*.json")
	if err != nil {
		return err
	}
	name := f.Name()
	done := false
	defer func() {
		if !done {
			os.Remove(name)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	done = true
	return os.Chmod(path, 0o600)
}

func writeInPlace(path string, raw []byte) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := cacheOwner(fi); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func cacheOwner(fi os.FileInfo) error {
	if !fi.Mode().IsRegular() {
		return errors.New("update cache is not a file")
	}
	if fi.Mode().Perm() != 0o600 {
		return errors.New("update cache mode")
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if ok && st.Uid != uint32(os.Getuid()) {
		return errors.New("update cache owner")
	}
	return nil
}
