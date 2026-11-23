//go:build linux

package tooliso

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"
)

const maxToolFile = 4 << 20

// ReadUnder reads one regular file that stays inside root.
// The open uses RESOLVE_BENEATH, RESOLVE_NO_SYMLINKS, and
// RESOLVE_NO_MAGICLINKS, then checks the opened descriptor is still inside
// the root. A symlink swapped into the path is not followed.
func ReadUnder(root, rel string) ([]byte, error) {
	rootFile, rel, err := openRoot(root, rel)
	if err != nil {
		return nil, ErrPath
	}
	defer rootFile.Close()
	file, err := openAt(rootFile, rel, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrPath
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxToolFile {
		return nil, ErrPath
	}
	buf, err := io.ReadAll(io.LimitReader(file, maxToolFile+1))
	if err != nil || len(buf) > maxToolFile {
		return nil, ErrPath
	}
	return buf, nil
}

// ListUnder returns relative names inside root that match pattern.
// Directory walks do not follow symlinks.
func ListUnder(root, pattern string) ([]string, error) {
	pattern = strings.TrimPrefix(strings.TrimSpace(pattern), "./")
	if pattern == "" || strings.Contains(pattern, "\x00") || filepath.IsAbs(pattern) {
		return nil, ErrPath
	}
	rootFile, err := os.Open(root)
	if err != nil {
		return nil, ErrPath
	}
	defer rootFile.Close()
	info, err := rootFile.Stat()
	if err != nil || !info.IsDir() {
		return nil, ErrPath
	}
	var out []string
	if err := walkUnder(rootFile, ".", pattern, &out); err != nil {
		return nil, ErrPath
	}
	return out, nil
}

func openRoot(root, rel string) (*os.File, string, error) {
	rel = cleanRel(rel)
	if rel == "" {
		return nil, "", ErrPath
	}
	rootFile, err := os.Open(root)
	if err != nil {
		return nil, "", err
	}
	info, err := rootFile.Stat()
	if err != nil || !info.IsDir() {
		rootFile.Close()
		return nil, "", ErrPath
	}
	return rootFile, rel, nil
}

func openAt(root *os.File, rel string, flags int, mode uint32) (*os.File, error) {
	fd, err := unix.Openat2(int(root.Fd()), rel, &unix.OpenHow{
		Flags:   uint64(flags),
		Mode:    uint64(mode),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), rel)
	if err := confirmInside(root, file); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func confirmInside(root, file *os.File) error {
	rootPath, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", root.Fd()))
	if err != nil {
		return err
	}
	filePath, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", file.Fd()))
	if err != nil {
		return err
	}
	rootPath = strings.TrimSuffix(rootPath, " (deleted)")
	filePath = strings.TrimSuffix(filePath, " (deleted)")
	if blockedFS(filePath) || !inside(rootPath, filePath) {
		return ErrPath
	}
	var rootStat, fileStat unix.Stat_t
	if err := unix.Fstat(int(root.Fd()), &rootStat); err != nil {
		return err
	}
	if err := unix.Fstat(int(file.Fd()), &fileStat); err != nil {
		return err
	}
	if fileStat.Dev != rootStat.Dev {
		return ErrPath
	}
	return nil
}

func cleanRel(rel string) string {
	rel = strings.TrimSpace(rel)
	if rel == "" || strings.ContainsRune(rel, 0) || strings.ContainsAny(rel, "\n\r") || filepath.IsAbs(rel) {
		return ""
	}
	rel = filepath.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return rel
}

func walkUnder(dir *os.File, rel, pattern string, out *[]string) error {
	entries, err := dir.ReadDir(0)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "." || name == ".." || strings.ContainsRune(name, 0) {
			continue
		}
		child := name
		if rel != "." {
			child = rel + "/" + name
		}
		file, err := openAt(dir, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			continue
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			continue
		}
		if info.IsDir() {
			if matchGlob(pattern, child) {
				*out = append(*out, child)
			}
			err = walkUnder(file, child, pattern, out)
			file.Close()
			if err != nil {
				return err
			}
			continue
		}
		file.Close()
		if info.Mode().IsRegular() && matchGlob(pattern, child) {
			*out = append(*out, child)
		}
	}
	return nil
}

func matchGlob(pattern, name string) bool {
	if ok, err := filepath.Match(pattern, name); err == nil && ok {
		return true
	}
	if !strings.Contains(pattern, "**") {
		return false
	}
	re := regexp.QuoteMeta(pattern)
	re = strings.ReplaceAll(re, `\*\*`, `.*`)
	re = strings.ReplaceAll(re, `\*`, `[^/]*`)
	re = strings.ReplaceAll(re, `\?`, `[^/]`)
	matched, err := regexp.MatchString("^"+re+"$", name)
	return err == nil && matched
}
