// Package bundle is the agent directory castlet writes at boot.
// The bytes are a tar of agent.yaml, instructions.md, and optional mcp shim
// files. The sha256 is of those bytes, so a reload can tell a new version
// from the one on disk. Shim files name a command. They do not hold a token.
package bundle

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	yamlName = "agent.yaml"
	instName = "instructions.md"
)

// Archive builds a deterministic tar and its sha256.
func Archive(yaml, instructions string) ([]byte, string, error) {
	return ArchiveFiles(yaml, instructions, nil)
}

// ArchiveFiles adds mcp shim files. Names must be mcp/<name>.yaml.
// A nil or empty map matches Archive of the two main files.
func ArchiveFiles(yaml, instructions string, extras map[string]string) ([]byte, string, error) {
	if yaml == "" || instructions == "" {
		return nil, "", errors.New("bundle is missing agent.yaml or instructions.md")
	}
	var names []string
	for name, body := range extras {
		if !safeExtra(name) || strings.Contains(body, "\x00") {
			return nil, "", errors.New("bundle extra")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := write(tw, yamlName, []byte(yaml)); err != nil {
		return nil, "", err
	}
	if err := write(tw, instName, []byte(instructions)); err != nil {
		return nil, "", err
	}
	for _, name := range names {
		if err := write(tw, name, []byte(extras[name])); err != nil {
			return nil, "", err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:]), nil
}

func safeExtra(name string) bool {
	if strings.Contains(name, "..") || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	rest, ok := strings.CutPrefix(name, "mcp/")
	if !ok || rest == "" || strings.Contains(rest, "/") {
		return false
	}
	return strings.HasSuffix(rest, ".yaml")
}

func write(tw *tar.Writer, name string, body []byte) error {
	hdr := &tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(body)),
		ModTime:  time.Unix(0, 0).UTC(),
		Typeflag: tar.TypeReg,
		Format:   tar.FormatUSTAR,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(body)
	return err
}

// Read pulls agent.yaml and instructions.md out of a bundle tar.
func Read(raw []byte) (string, string, error) {
	tr := tar.NewReader(bytes.NewReader(raw))
	var yaml, instructions string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", "", err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != '\x00' {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, 1<<20))
		if err != nil {
			return "", "", err
		}
		switch hdr.Name {
		case yamlName:
			yaml = string(body)
		case instName:
			instructions = string(body)
		}
	}
	if yaml == "" || instructions == "" {
		return "", "", errors.New("bundle is missing agent.yaml or instructions.md")
	}
	return yaml, instructions, nil
}

// Install writes the bundle into dir and creates workspace and memory.
// It leaves files already in those two directories alone.
func Install(dir string, raw []byte) error {
	yaml, instructions, err := Read(raw)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, yamlName), []byte(yaml), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, instName), []byte(instructions), 0o644); err != nil {
		return err
	}
	if err := installExtras(dir, raw); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "workspace"), 0o755); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(dir, "memory"), 0o755)
}

func installExtras(dir string, raw []byte) error {
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if !safeExtra(hdr.Name) {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, 1<<20))
		if err != nil {
			return err
		}
		path := filepath.Join(dir, filepath.FromSlash(hdr.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return err
		}
	}
}

// Sum is the sha256 of the bundle bytes.
func Sum(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
