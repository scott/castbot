package tooliso

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RunFile reads or lists a path for a file tool. It prints file bytes to
// stdout. The caller is the tool account. A path outside the roots is refused.
func RunFile(args []string) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if err := RequireToolGroups(); err != nil {
		fmt.Fprintln(os.Stderr, "path refused")
		return 1
	}
	roots := fileRoots()
	os.Clearenv()
	_ = SetNonDumpable()
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "path refused")
		return 1
	}
	op, path := args[0], args[1]
	if len(args) > 2 && args[1] == "--" {
		path = args[2]
	}
	switch op {
	case "read":
		buf, err := readFromRoots(roots, path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "path refused")
			return 1
		}
		if _, err := os.Stdout.Write(buf); err != nil {
			return 1
		}
		return 0
	case "glob":
		names, err := listFromRoots(roots, path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "path refused")
			return 1
		}
		if len(names) == 0 {
			fmt.Fprintln(os.Stderr, "none")
			return 1
		}
		fmt.Fprintln(os.Stdout, strings.Join(names, "\n"))
		return 0
	default:
		fmt.Fprintln(os.Stderr, "path refused")
		return 1
	}
}

func fileRoots() []string {
	agent := os.Getenv("CAST_AGENT_PATH")
	if agent == "" {
		agent = os.Getenv("CAST_AGENT_DIR")
	}
	if agent == "" {
		agent = "/app/agent"
	}
	roots := DefaultRoots(agent)
	extra := os.Getenv("CASTBOT_READ_ROOTS")
	if extra != "" {
		roots = append(roots, strings.Split(extra, ":")...)
	}
	return roots
}

func readFromRoots(roots []string, path string) ([]byte, error) {
	root, rel, err := splitRoot(roots, path)
	if err != nil {
		return nil, err
	}
	return ReadUnder(root, rel)
}

func listFromRoots(roots []string, pattern string) ([]string, error) {
	if len(roots) == 0 {
		return nil, ErrPath
	}
	return ListUnder(roots[0], pattern)
}

func splitRoot(roots []string, path string) (string, string, error) {
	path = strings.TrimSpace(path)
	if path == "" || strings.ContainsRune(path, 0) {
		return "", "", ErrPath
	}
	cleaned, err := cleanRoots(roots)
	if err != nil {
		return "", "", err
	}
	if !filepath.IsAbs(path) {
		return cleaned[0], path, nil
	}
	for _, root := range cleaned {
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return root, rel, nil
	}
	return "", "", ErrPath
}

// ReadFromRoots is the file-tool read used by tests.
func ReadFromRoots(path string, roots []string) ([]byte, error) {
	return readFromRoots(roots, path)
}
