package replace

import (
	"archive/tar"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

// errUseFiles tells Run to keep the per-file copy. The new guest has no archive.
var errUseFiles = errors.New("use file copy")

func (r *runner) copyState(c host.StateCopier) error {
	if err := r.step("read_old"); err != nil {
		return err
	}
	probe, err := c.ExportState(r.ctx, r.row.NewEndpoint, r.token)
	if errors.Is(err, host.ErrNoArchive) {
		return errUseFiles
	}
	if err != nil || probe == nil {
		return &classError{step: "copy", class: "copy"}
	}
	_, _ = io.Copy(io.Discard, probe)
	probe.Close()
	if r.vm.State == "paused" {
		started := time.Now()
		_ = r.st.AddVMEvent(r.ctx, r.opt.UserID, r.opt.AgentID, store.EventWakeRequested, 0)
		if _, err := r.h.Wake(r.ctx, r.vm.MicroVMID); err != nil {
			return &classError{step: "read_old", class: "read_old"}
		}
		_ = r.st.AddVMEvent(r.ctx, r.opt.UserID, r.opt.AgentID, store.EventWakeRunning, time.Since(started))
		r.res.Woke = true
		r.row.Woke = true
	}
	oldEndpoint, oldToken, err := r.st.VMCredentials(r.ctx, r.opt.UserID, r.opt.AgentID)
	if err != nil || oldEndpoint == "" || oldToken == "" {
		return &classError{step: "read_old", class: "read_old"}
	}
	r.pullJournal(oldEndpoint, oldToken)
	if err := r.halt("read_old"); err != nil {
		return err
	}
	if err := r.step("copy"); err != nil {
		return err
	}
	limit := r.opt.ArchiveLimit
	if limit <= 0 {
		limit = host.DefaultDisk
	}
	src, bridged, err := r.openState(c, oldEndpoint, oldToken)
	if err != nil {
		return err
	}
	sum := sha256.New()
	counted := &byteCap{r: src, limit: limit}
	got, impErr := c.ImportState(r.ctx, r.row.NewEndpoint, r.token, io.TeeReader(counted, sum), limit)
	src.Close()
	bridgeErr := bridged()
	if counted.tooBig || errors.Is(impErr, host.ErrArchiveTooBig) {
		return &classError{step: "copy", class: "disk"}
	}
	if bridgeErr != nil || impErr != nil {
		return &classError{step: "copy", class: "copy"}
	}
	var sent [32]byte
	copy(sent[:], sum.Sum(nil))
	if subtle.ConstantTimeCompare(sent[:], got.Sum[:]) != 1 {
		return &classError{step: "copy", class: "copy"}
	}
	r.res.Files = got.Files
	r.res.MemoryBytes = got.Memory
	_ = r.st.NoteReplacementCopy(r.ctx, r.opt.UserID, r.row.ID, r.res.Woke, got.Memory, got.Files, got.Files, got.Bytes)
	if err := r.h.Reload(r.ctx, r.row.NewEndpoint, r.token); err != nil {
		return &classError{step: "copy", class: "copy"}
	}
	if err := r.halt("copy"); err != nil {
		return err
	}
	if err := r.step("verify"); err != nil {
		return err
	}
	if _, err := r.healthy(r.row.NewEndpoint, r.token); err != nil {
		return &classError{step: "verify", class: "verify"}
	}
	if err := r.halt("verify"); err != nil {
		return err
	}
	return nil
}

// openState returns the old guest's archive, or a tar built from its file API.
// The func reports the bridge error after the reader is closed.
func (r *runner) openState(c host.StateCopier, endpoint, token string) (io.ReadCloser, func() error, error) {
	body, err := c.ExportState(r.ctx, endpoint, token)
	if err == nil && body != nil {
		return body, func() error { return nil }, nil
	}
	if err != nil && !errors.Is(err, host.ErrNoArchive) {
		return nil, nil, &classError{step: "read_old", class: "read_old"}
	}
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		errc <- r.writeBridge(pw, endpoint, token)
		pw.Close()
	}()
	return pr, func() error { return <-errc }, nil
}

func (r *runner) writeBridge(w io.Writer, endpoint, token string) error {
	tw := tar.NewWriter(w)
	mem, err := r.h.ExportMemory(r.ctx, endpoint, token)
	if err != nil {
		_ = tw.Close()
		return err
	}
	err = repackMemory(tw, mem)
	mem.Close()
	if err != nil {
		_ = tw.Close()
		return err
	}
	listed, err := r.h.ListWorkspace(r.ctx, endpoint, token)
	if err != nil {
		_ = tw.Close()
		return err
	}
	for _, item := range listed {
		if !oneName(item.Name) || item.Size < 0 {
			_ = tw.Close()
			return errBadEntry
		}
		body, err := r.h.ReadWorkspace(r.ctx, endpoint, token, item.Name)
		if err != nil {
			_ = tw.Close()
			return err
		}
		hdr := &tar.Header{
			Name:     "workspace/" + item.Name,
			Mode:     0o644,
			Size:     item.Size,
			Typeflag: tar.TypeReg,
			Format:   tar.FormatPAX,
			ModTime:  time.Unix(0, 0).UTC(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			body.Close()
			_ = tw.Close()
			return err
		}
		n, err := io.Copy(tw, body)
		body.Close()
		if err != nil || n != item.Size {
			_ = tw.Close()
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return err
		}
	}
	return tw.Close()
}

func repackMemory(tw *tar.Writer, r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA && hdr.Typeflag != 0 {
			continue
		}
		name := hdr.Name
		if !oneName(name) || hdr.Size < 0 {
			return errBadEntry
		}
		out := &tar.Header{
			Name:     "memory/" + name,
			Mode:     0o644,
			Size:     hdr.Size,
			Typeflag: tar.TypeReg,
			Format:   tar.FormatPAX,
			ModTime:  time.Unix(0, 0).UTC(),
		}
		if err := tw.WriteHeader(out); err != nil {
			return err
		}
		if _, err := io.CopyN(tw, tr, hdr.Size); err != nil {
			return err
		}
	}
}

var errTooBig = errors.New("too big")
var errBadEntry = errors.New("bad archive entry")

type byteCap struct {
	r      io.Reader
	limit  int64
	n      int64
	tooBig bool
}

func (c *byteCap) Read(p []byte) (int, error) {
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
