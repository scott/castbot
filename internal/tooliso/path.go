package tooliso

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrPath is returned when a file tool path is outside the workspace
// or lands in a kernel filesystem.
var ErrPath = errors.New("path refused")

// DefaultRoots are the directories a file tool may touch besides the
// agent directory. CASTBOT_READ_ROOTS can add more, separated by colons.
func DefaultRoots(agentDir string) []string {
	var roots []string
	if agentDir != "" {
		roots = append(roots, agentDir)
	}
	roots = append(roots, "/app/.local", "/tmp")
	return roots
}

// Allow reports whether path may be opened by a file tool.
// Relative paths join the first root. Symlinks are resolved, and a link
// that leaves the roots or enters /proc, /sys, or /dev is refused.
// A missing final component inside a root is allowed so the tool can
// report that the file is not there.
func Allow(path string, roots []string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || strings.ContainsRune(path, 0) || strings.ContainsAny(path, "\n\r") {
		return "", ErrPath
	}
	cleanedRoots, err := cleanRoots(roots)
	if err != nil || len(cleanedRoots) == 0 {
		return "", ErrPath
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cleanedRoots[0], path)
	}
	resolved, err := resolve(path)
	if err != nil {
		return "", ErrPath
	}
	if blockedFS(resolved) {
		return "", ErrPath
	}
	for _, root := range cleanedRoots {
		if inside(root, resolved) {
			return resolved, nil
		}
	}
	return "", ErrPath
}

func cleanRoots(roots []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" || strings.ContainsRune(root, 0) || !filepath.IsAbs(root) {
			continue
		}
		resolved, err := resolve(root)
		if err != nil || blockedFS(resolved) {
			continue
		}
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		out = append(out, resolved)
	}
	if len(out) == 0 {
		return nil, ErrPath
	}
	return out, nil
}

func blockedFS(path string) bool {
	path = filepath.Clean(path)
	switch path {
	case "/proc", "/sys", "/dev", "/app/.cast", "/app/.castlet":
		return true
	}
	for _, prefix := range []string{"/proc/", "/sys/", "/dev/", "/app/.cast/", "/app/.castlet/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func inside(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolve walks path and follows symlinks. A missing tail stays joined
// onto the last real directory so a not-yet-created file can be named.
func resolve(path string) (string, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return "", ErrPath
	}
	cur := string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	hops := 0
	for i := 0; i < len(parts); i++ {
		part := parts[i]
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			parent := filepath.Dir(cur)
			if blockedFS(parent) && parent != cur {
				return "", ErrPath
			}
			cur = parent
			continue
		}
		next := filepath.Join(cur, part)
		info, err := os.Lstat(next)
		if err != nil {
			if os.IsNotExist(err) {
				rest := append([]string{next}, parts[i+1:]...)
				joined := filepath.Join(rest...)
				if strings.Contains(joined, "..") {
					return "", ErrPath
				}
				return filepath.Clean(joined), nil
			}
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			cur = next
			if blockedFS(cur) {
				return "", ErrPath
			}
			continue
		}
		hops++
		if hops > 40 {
			return "", ErrPath
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(cur, target)
		}
		target = filepath.Clean(target)
		// The remaining components are resolved from the link target.
		rest := append(strings.Split(strings.TrimPrefix(target, string(filepath.Separator)), string(filepath.Separator)), parts[i+1:]...)
		parts = rest
		cur = string(filepath.Separator)
		i = -1
	}
	if blockedFS(cur) {
		return "", ErrPath
	}
	return filepath.Clean(cur), nil
}
