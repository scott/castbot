package castlet

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/scott/castbot/internal/tooliso"
)

// archiveCap is the small machine disk. A larger header is cut down to this.
const archiveCap = 50 << 30

const archiveLimitHeader = "X-Castbot-Archive-Limit"

// localRoot is /app/.local when the agent dir is /app/agent.
// A test agent dir that is not named agent keeps .local inside that dir.
func (s *Supervisor) localRoot() string {
	if s.cfg.LocalDir != "" {
		return s.cfg.LocalDir
	}
	if filepath.Base(s.cfg.AgentDir) == "agent" {
		return filepath.Join(filepath.Dir(s.cfg.AgentDir), ".local")
	}
	return filepath.Join(s.cfg.AgentDir, ".local")
}

func (s *Supervisor) archiveDirs() []archiveDir {
	return []archiveDir{
		{key: "memory", dir: filepath.Join(s.cfg.AgentDir, "memory")},
		{key: "workspace", dir: filepath.Join(s.cfg.AgentDir, "workspace")},
		{key: "local", dir: s.localRoot()},
	}
}

type archiveDir struct {
	key string
	dir string
}

func (s *Supervisor) exportArchive(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Cache-Control", "no-store")
	tw := tar.NewWriter(w)
	for _, root := range s.archiveDirs() {
		if _, err := os.Lstat(root.dir); err != nil {
			continue
		}
		err := filepath.WalkDir(root.dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d == nil || p == root.dir {
				return err
			}
			rel, err := filepath.Rel(root.dir, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if !plainRel(rel) {
				return errOutside
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			hdr, err := archiveHeader(root.key+"/"+rel, info, p)
			if err != nil {
				return err
			}
			if hdr == nil {
				return nil
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
				return nil
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			return err
		})
		if err != nil {
			_ = tw.Close()
			return
		}
	}
	_ = tw.Close()
}

func archiveHeader(name string, info os.FileInfo, path string) (*tar.Header, error) {
	mode := info.Mode()
	hdr := &tar.Header{
		Name:    name,
		Mode:    int64(mode.Perm()),
		ModTime: info.ModTime().UTC(),
		Format:  tar.FormatPAX,
	}
	switch {
	case mode&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return nil, err
		}
		if strings.ContainsRune(target, 0) {
			return nil, errOutside
		}
		hdr.Typeflag = tar.TypeSymlink
		hdr.Linkname = target
		if hdr.Mode == 0 {
			hdr.Mode = 0o777
		}
		return hdr, nil
	case mode.IsDir():
		hdr.Typeflag = tar.TypeDir
		hdr.Name = strings.TrimRight(name, "/") + "/"
		if hdr.Mode == 0 {
			hdr.Mode = 0o755
		}
		return hdr, nil
	case mode.IsRegular():
		hdr.Typeflag = tar.TypeReg
		hdr.Size = info.Size()
		if hdr.Mode == 0 {
			hdr.Mode = 0o644
		}
		return hdr, nil
	default:
		return nil, nil
	}
}

func (s *Supervisor) importArchive(w http.ResponseWriter, r *http.Request) {
	limit := archiveLimit(r)
	if limit < 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	for _, root := range s.archiveDirs() {
		if root.key == "local" {
			continue
		}
		if info, err := os.Lstat(root.dir); err == nil && info.Mode()&os.ModeSymlink != 0 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := os.MkdirAll(root.dir, 0o755); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
	}
	h := sha256.New()
	body := &capRead{r: r.Body, limit: limit}
	tr := tar.NewReader(io.TeeReader(body, h))
	var files int
	var memory int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if errors.Is(err, errTooBig) || body.tooBig {
			http.Error(w, "too big", http.StatusRequestEntityTooLarge)
			return
		}
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		n, mem, err := s.extractEntry(tr, hdr, limit)
		if errors.Is(err, errTooBig) {
			http.Error(w, "too big", http.StatusRequestEntityTooLarge)
			return
		}
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		files += n
		memory += mem
	}
	if _, err := io.Copy(h, body); err != nil {
		if errors.Is(err, errTooBig) || body.tooBig {
			http.Error(w, "too big", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.tooBig {
		http.Error(w, "too big", http.StatusRequestEntityTooLarge)
		return
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(archiveResult{
		SHA256: hex.EncodeToString(sum[:]),
		Files:  files,
		Memory: memory,
		Bytes:  body.n,
	})
}

type archiveResult struct {
	SHA256 string `json:"sha256"`
	Files  int    `json:"files"`
	Memory int64  `json:"memory"`
	Bytes  int64  `json:"bytes"`
}

func archiveLimit(r *http.Request) int64 {
	v := strings.TrimSpace(r.Header.Get(archiveLimitHeader))
	if v == "" {
		return archiveCap
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return -1
	}
	if n > archiveCap {
		return archiveCap
	}
	return n
}

func (s *Supervisor) extractEntry(tr *tar.Reader, hdr *tar.Header, limit int64) (files int, memory int64, err error) {
	if hdr.Size < 0 || hdr.Size > limit {
		return 0, 0, errTooBig
	}
	key, rel, ok := splitArchive(hdr.Name)
	if !ok {
		return 0, 0, errOutside
	}
	root := s.dirFor(key)
	if rel == "" {
		if hdr.Typeflag == tar.TypeDir {
			return 0, 0, os.MkdirAll(root, 0o755)
		}
		return 0, 0, errOutside
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	if !under(root, full) {
		return 0, 0, errOutside
	}
	if err := ensureParent(root, full); err != nil {
		return 0, 0, err
	}
	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := existingLink(full); err != nil {
			return 0, 0, err
		}
		if relPath, ok := agentRel(s.cfg.AgentDir, full); ok {
			if err := tooliso.MkdirAllUnder(s.cfg.AgentDir, relPath, uint32(agentDirMode)); err != nil {
				return 0, 0, err
			}
			return 0, 0, nil
		}
		mode := dirMode(hdr.Mode)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return 0, 0, err
		}
		if err := os.MkdirAll(full, mode); err != nil {
			return 0, 0, err
		}
		if err := os.Chmod(full, mode); err != nil {
			return 0, 0, err
		}
		return 0, 0, nil
	case tar.TypeSymlink:
		if strings.ContainsRune(hdr.Linkname, 0) || hdr.Linkname == "" {
			return 0, 0, errOutside
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return 0, 0, err
		}
		if err := ensureParent(root, full); err != nil {
			return 0, 0, err
		}
		_ = os.Remove(full)
		return 0, 0, os.Symlink(hdr.Linkname, full)
	case tar.TypeReg, tar.TypeRegA:
		if err := existingLink(full); err != nil {
			return 0, 0, err
		}
		if relPath, ok := agentRel(s.cfg.AgentDir, full); ok {
			if err := tooliso.MkdirAllUnder(s.cfg.AgentDir, filepath.Dir(relPath), uint32(agentDirMode)); err != nil {
				return 0, 0, err
			}
			mode := uint32(agentSharedMode(fileMode(hdr.Mode)))
			f, err := openShared(s.cfg.AgentDir, relPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return 0, 0, err
			}
			n, err := io.Copy(f, io.LimitReader(tr, hdr.Size))
			cerr := f.Close()
			if err != nil || cerr != nil || n != hdr.Size {
				if err == nil {
					err = io.ErrUnexpectedEOF
				}
				return 0, 0, err
			}
			if key == "workspace" {
				files = 1
			}
			if key == "memory" {
				memory = n
			}
			return files, memory, nil
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return 0, 0, err
		}
		if err := ensureParent(root, full); err != nil {
			return 0, 0, err
		}
		mode := fileMode(hdr.Mode)
		n, err := writeFreshStream(filepath.Dir(full), filepath.Base(full), tr, mode, hdr.Size)
		if err != nil || n != hdr.Size {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return 0, 0, err
		}
		if key == "workspace" {
			files = 1
		}
		if key == "memory" {
			memory = n
		}
		return files, memory, nil
	default:
		return 0, 0, errOutside
	}
}

func (s *Supervisor) dirFor(key string) string {
	for _, root := range s.archiveDirs() {
		if root.key == key {
			return root.dir
		}
	}
	return ""
}

func splitArchive(name string) (key, rel string, ok bool) {
	name = strings.TrimPrefix(name, "./")
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, `\`) || strings.ContainsRune(name, 0) {
		return "", "", false
	}
	name = strings.TrimSuffix(name, "/")
	parts := strings.Split(name, "/")
	switch parts[0] {
	case "memory", "workspace", "local":
	default:
		return "", "", false
	}
	for _, part := range parts[1:] {
		if part == "" || part == "." || part == ".." {
			return "", "", false
		}
	}
	return parts[0], strings.Join(parts[1:], "/"), true
}

func plainRel(rel string) bool {
	if rel == "" || rel == "." || strings.HasPrefix(rel, "/") || strings.Contains(rel, `\`) || strings.ContainsRune(rel, 0) {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func existingLink(full string) error {
	info, err := os.Lstat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errOutside
	}
	return nil
}

func fileMode(mode int64) os.FileMode {
	m := os.FileMode(mode) & os.ModePerm
	if m == 0 {
		return 0o644
	}
	return m
}

func dirMode(mode int64) os.FileMode {
	m := os.FileMode(mode) & os.ModePerm
	if m == 0 {
		return 0o755
	}
	return m
}

var errTooBig = errors.New("too big")

type capRead struct {
	r      io.Reader
	limit  int64
	n      int64
	tooBig bool
}

func (c *capRead) Read(p []byte) (int, error) {
	if c.tooBig {
		return 0, errTooBig
	}
	if c.limit >= 0 && c.n >= c.limit && len(p) > 0 {
		var one [1]byte
		n, err := c.r.Read(one[:])
		if n > 0 {
			c.n += int64(n)
			c.tooBig = true
			return 0, errTooBig
		}
		return 0, err
	}
	remain := int64(len(p))
	if c.limit >= 0 && remain > c.limit-c.n+1 {
		remain = c.limit - c.n + 1
	}
	n, err := c.r.Read(p[:remain])
	c.n += int64(n)
	if c.limit >= 0 && c.n > c.limit {
		c.tooBig = true
		return n, errTooBig
	}
	return n, err
}
