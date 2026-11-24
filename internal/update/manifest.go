// Package update builds and checks a castbot release manifest.
package update

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

const ManifestVersion = 1

const maxAsset = 100 << 20

var (
	versionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	shaRE     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	guestRE   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	sumRE     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Asset is one file attached to a release.
type Asset struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Manifest is release.json. Manifest is the file format version.
type Manifest struct {
	Manifest           int              `json:"manifest"`
	Version            string           `json:"version"`
	Commit             string           `json:"commit"`
	Schema             int              `json:"schema"`
	GuestID            string           `json:"guest_id"`
	RuntimeImage       string           `json:"runtime_image"`
	RuntimeImageCommit string           `json:"runtime_image_commit"`
	Notes              string           `json:"notes"`
	Assets             map[string]Asset `json:"assets"`
}

// Input is the release the pack command writes.
type Input struct {
	Version            string
	Commit             string
	Schema             int
	GuestID            string
	RuntimeImage       string
	RuntimeImageCommit string
	Notes              string
	AMD64              string
	ARM64              string
}

// Validate checks the fields that do not depend on file bytes.
func (in Input) Validate() error {
	if in.Version == "" || !versionRE.MatchString(in.Version) {
		return errors.New("version is unexpected")
	}
	if !shaRE.MatchString(in.Commit) {
		return errors.New("commit is unexpected")
	}
	if in.Schema < 1 {
		return errors.New("schema is unexpected")
	}
	if !guestRE.MatchString(in.GuestID) {
		return errors.New("guest id is unexpected")
	}
	wantImage := "ghcr.io/scott/castbot-runtime:" + in.RuntimeImageCommit
	if in.RuntimeImage != wantImage || !shaRE.MatchString(in.RuntimeImageCommit) {
		return errors.New("runtime image is unexpected")
	}
	if in.Notes != "https://github.com/scott/castbot/releases/tag/v"+in.Version {
		return errors.New("notes url is unexpected")
	}
	if in.AMD64 == "" || in.ARM64 == "" {
		return errors.New("both binaries are required")
	}
	return nil
}

// HashFile returns the size and sha256 of path. The file must be non-empty
// and at most 100 MiB.
func HashFile(path string) (Asset, error) {
	f, err := os.Open(path)
	if err != nil {
		return Asset{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Asset{}, err
	}
	if !info.Mode().IsRegular() {
		return Asset{}, errors.New("asset is not a file")
	}
	if info.Size() <= 0 || info.Size() > maxAsset {
		return Asset{}, errors.New("asset size is unexpected")
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return Asset{}, err
	}
	if n != info.Size() {
		return Asset{}, errors.New("asset size changed")
	}
	sum := hex.EncodeToString(h.Sum(nil))
	return Asset{Name: filepath.Base(path), SHA256: sum, Size: n}, nil
}

// Build fills a manifest from the two binaries.
func Build(in Input) (Manifest, error) {
	if err := in.Validate(); err != nil {
		return Manifest{}, err
	}
	amd, err := HashFile(in.AMD64)
	if err != nil {
		return Manifest{}, err
	}
	arm, err := HashFile(in.ARM64)
	if err != nil {
		return Manifest{}, err
	}
	amd.Name = "castbot_linux_amd64"
	arm.Name = "castbot_linux_arm64"
	return Manifest{
		Manifest:           ManifestVersion,
		Version:            in.Version,
		Commit:             in.Commit,
		Schema:             in.Schema,
		GuestID:            in.GuestID,
		RuntimeImage:       in.RuntimeImage,
		RuntimeImageCommit: in.RuntimeImageCommit,
		Notes:              in.Notes,
		Assets: map[string]Asset{
			"linux-amd64": amd,
			"linux-arm64": arm,
		},
	}, nil
}

// WriteManifest writes release.json.
func WriteManifest(path string, m Manifest) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return writeNew(path, raw)
}

// EncodeSums writes a sha256sum file for the named files. Names are basenames.
func EncodeSums(dir string, names []string) ([]byte, error) {
	sort.Strings(names)
	var buf bytes.Buffer
	seen := map[string]bool{}
	for _, name := range names {
		base := filepath.Base(name)
		if seen[base] || base == "" || base != name && filepath.Dir(name) != dir {
			return nil, errors.New("sum name is unexpected")
		}
		if !fileNameOK(base) {
			return nil, errors.New("sum name is unexpected")
		}
		seen[base] = true
		asset, err := HashFile(filepath.Join(dir, base))
		if err != nil {
			return nil, err
		}
		if asset.Name != base || !sumRE.MatchString(asset.SHA256) {
			return nil, errors.New("sum is unexpected")
		}
		fmt.Fprintf(&buf, "%s  %s\n", asset.SHA256, base)
	}
	if buf.Len() == 0 {
		return nil, errors.New("sums are empty")
	}
	return buf.Bytes(), nil
}

func fileNameOK(name string) bool {
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '.' && r != '-' {
			return false
		}
	}
	return name != "" && name != "." && name != ".."
}

func writeNew(path string, raw []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
