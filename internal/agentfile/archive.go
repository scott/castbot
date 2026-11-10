package agentfile

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path"
	"strings"
)

const (
	maxArchive      = 4 << 20
	maxUncompressed = 8 << 20
	maxFiles        = 200
	maxFile         = 1 << 20
)

var (
	errOversize  = errors.New("archive is too large")
	errTooMany   = errors.New("archive has too many files")
	errTraversal = errors.New("archive path is not allowed")
	errSymlink   = errors.New("archive contains a link")
	errFormat    = errors.New("archive is not a zip or tar")
	errNoAgent   = errors.New("folder has no agent.yaml")
)

// Folder is the agent.yaml and instructions.md from an upload.
// Skipped names the files that were left out because they can hold secrets.
type Folder struct {
	YAML         string
	Instructions string
	SpecName     string
	Skipped      []string
	LeftOut      []string
}

// ReadArchive extracts a zip, tar, or gzip tar. It rejects path traversal,
// absolute paths, links, an oversized archive, and too many files.
// subpath, when set, keeps only that directory. Nothing is executed.
func ReadArchive(raw []byte, subpath string) (Folder, error) {
	if len(raw) == 0 {
		return Folder{}, errFormat
	}
	if len(raw) > maxArchive {
		return Folder{}, errOversize
	}
	files, err := unpack(raw)
	if err != nil {
		return Folder{}, err
	}
	return folderFrom(files, subpath)
}

func unpack(raw []byte) (map[string][]byte, error) {
	if len(raw) >= 2 && raw[0] == 'P' && raw[1] == 'K' {
		return unpackZip(raw)
	}
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, errFormat
		}
		defer zr.Close()
		body, err := io.ReadAll(io.LimitReader(zr, maxUncompressed+1))
		if err != nil {
			return nil, errFormat
		}
		if len(body) > maxUncompressed {
			return nil, errOversize
		}
		return unpackTar(body)
	}
	return unpackTar(raw)
}

func unpackZip(raw []byte) (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, errFormat
	}
	out := map[string][]byte{}
	var total int64
	n := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		mode := f.Mode()
		if mode&os.ModeSymlink != 0 || !mode.IsRegular() {
			return nil, errSymlink
		}
		name, err := cleanEntry(f.Name)
		if err != nil {
			return nil, err
		}
		n++
		if n > maxFiles {
			return nil, errTooMany
		}
		if f.UncompressedSize64 > maxFile {
			return nil, errOversize
		}
		rc, err := f.Open()
		if err != nil {
			return nil, errFormat
		}
		body, err := io.ReadAll(io.LimitReader(rc, maxFile+1))
		rc.Close()
		if err != nil {
			return nil, errFormat
		}
		if len(body) > maxFile {
			return nil, errOversize
		}
		total += int64(len(body))
		if total > maxUncompressed {
			return nil, errOversize
		}
		out[name] = body
	}
	return out, nil
}

func unpackTar(raw []byte) (map[string][]byte, error) {
	tr := tar.NewReader(bytes.NewReader(raw))
	out := map[string][]byte{}
	var total int64
	n := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, errFormat
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != '\x00' {
			return nil, errSymlink
		}
		name, err := cleanEntry(hdr.Name)
		if err != nil {
			return nil, err
		}
		n++
		if n > maxFiles {
			return nil, errTooMany
		}
		if hdr.Size < 0 || hdr.Size > maxFile {
			return nil, errOversize
		}
		body, err := io.ReadAll(io.LimitReader(tr, maxFile+1))
		if err != nil {
			return nil, errFormat
		}
		if int64(len(body)) > maxFile {
			return nil, errOversize
		}
		total += int64(len(body))
		if total > maxUncompressed {
			return nil, errOversize
		}
		out[name] = body
	}
}

func cleanEntry(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\x00") {
		return "", errTraversal
	}
	clean := path.Clean(name)
	if clean == "." || strings.HasPrefix(clean, "../") || clean == ".." || strings.HasPrefix(clean, "/") {
		return "", errTraversal
	}
	return clean, nil
}

func folderFrom(files map[string][]byte, subpath string) (Folder, error) {
	subpath = strings.Trim(path.Clean("/"+subpath), "/")
	if subpath == "." {
		subpath = ""
	}
	yamlPath := ""
	best := 1000
	for name := range files {
		if path.Base(name) != "agent.yaml" || !inSubpath(name, subpath) {
			continue
		}
		depth := strings.Count(name, "/")
		if yamlPath == "" || depth < best {
			yamlPath = name
			best = depth
		}
	}
	if yamlPath == "" {
		return Folder{}, errNoAgent
	}
	dir := path.Dir(yamlPath)
	if dir == "." {
		dir = ""
	}
	var folder Folder
	folder.YAML = string(files[yamlPath])
	inst := "instructions.md"
	if dir != "" {
		inst = dir + "/instructions.md"
	}
	if body, ok := files[inst]; ok {
		folder.Instructions = string(body)
	}
	for name := range files {
		if !under(name, dir) {
			continue
		}
		if name == yamlPath || name == inst {
			continue
		}
		if secretName(name) {
			folder.Skipped = append(folder.Skipped, name)
			continue
		}
		folder.LeftOut = append(folder.LeftOut, name)
	}
	sortStrings(folder.Skipped)
	sortStrings(folder.LeftOut)
	if len(folder.Skipped) > 40 {
		folder.Skipped = append(folder.Skipped[:40], "and more")
	}
	if len(folder.LeftOut) > 40 {
		folder.LeftOut = append(folder.LeftOut[:40], "and more")
	}
	return folder, nil
}

func inSubpath(name, sub string) bool {
	if sub == "" {
		return true
	}
	dir := path.Dir(name)
	return dir == sub || strings.HasSuffix(dir, "/"+sub)
}

func under(name, dir string) bool {
	if dir == "" || dir == "." {
		return !strings.Contains(name, "/")
	}
	return name == dir || strings.HasPrefix(name, dir+"/")
}

func secretName(name string) bool {
	base := strings.ToLower(path.Base(name))
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	switch base {
	case "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", ".netrc", "credentials.json", "secrets.json", "secrets.yaml", "secrets.yml":
		return true
	}
	if strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".p12") {
		return true
	}
	return false
}

func sortStrings(list []string) {
	for i := 1; i < len(list); i++ {
		j := i
		for j > 0 && list[j] < list[j-1] {
			list[j], list[j-1] = list[j-1], list[j]
			j--
		}
	}
}

// Errors so handlers can tell the cases apart.
func IsOversize(err error) bool  { return errors.Is(err, errOversize) }
func IsTooMany(err error) bool   { return errors.Is(err, errTooMany) }
func IsTraversal(err error) bool { return errors.Is(err, errTraversal) }
func IsSymlink(err error) bool   { return errors.Is(err, errSymlink) }
func IsFormat(err error) bool    { return errors.Is(err, errFormat) }
func IsNoAgent(err error) bool   { return errors.Is(err, errNoAgent) }
