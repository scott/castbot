package castlet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/replace"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

func TestReplaceCopiesRealCastletBytes(t *testing.T) {
	oldSrv, oldDir := guestServer(t, "guest-token")
	newSrv, newDir := guestServer(t, "guest-token")
	if err := os.WriteFile(filepath.Join(oldDir, "workspace", "tool"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("tool", filepath.Join(oldDir, "workspace", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(oldDir, ".local"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, ".local", "pkg"), []byte("pkg"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, agent := castletAgent(t, oldSrv.URL)
	h := &castHost{Fake: host.NewFake(), oldURL: oldSrv.URL, newURL: newSrv.URL, token: "guest-token"}
	h.Machines["vm-old"] = host.Machine{ID: "vm-old", State: "running", Endpoint: oldSrv.URL, Tags: []string{"agent:" + agent.ID}}
	h.OnCreate = func(spec host.CreateSpec) {
		next, err := st.ExchangeBootstrap(context.Background(), spec.AgentID, spec.Bootstrap)
		if err != nil {
			t.Errorf("exchange %v", err)
			return
		}
		if _, err := st.PlaceVMToken(context.Background(), spec.AgentID, next); err != nil {
			t.Errorf("place %v", err)
		}
	}
	res, err := replace.Run(context.Background(), st, h, replace.Options{
		UserID: agent.UserID, AgentID: agent.ID, ActorKind: "cli", ActorID: "cli", Actor: "cli",
		Image: "example.invalid/castbot-runtime:ff6d7ea", PublicURL: "https://castbot.example",
		DataDir: t.TempDir(), BootWait: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 2 || h.reloads != 1 || h.exports != 2 || h.imports != 1 {
		t.Fatalf("files %d reloads %d exports %d imports %d", res.Files, h.reloads, h.exports, h.imports)
	}
	got, err := os.ReadFile(filepath.Join(newDir, "workspace", "uploads", "hello.txt"))
	if err != nil || string(got) != "hello file" {
		t.Fatalf("file %q %v", got, err)
	}
	info, err := os.Stat(filepath.Join(newDir, "workspace", "tool"))
	if err != nil || info.Mode().Perm() != 0o770 {
		t.Fatalf("mode %v %v", info, err)
	}
	target, err := os.Readlink(filepath.Join(newDir, "workspace", "link"))
	if err != nil || target != "tool" {
		t.Fatalf("link %q %v", target, err)
	}
	pkg, err := os.ReadFile(filepath.Join(newDir, ".local", "pkg"))
	if err != nil || string(pkg) != "pkg" {
		t.Fatalf("local %q %v", pkg, err)
	}
	mem, err := os.ReadFile(filepath.Join(newDir, "memory", "note.txt"))
	if err != nil || string(mem) != "remember" {
		t.Fatalf("memory %q %v", mem, err)
	}
	live, err := st.LiveVM(context.Background(), agent.UserID, agent.ID)
	if err != nil || live.MicroVMID == "vm-old" {
		t.Fatal(err)
	}
	_ = oldDir
}

func TestArchiveLargeCheckoutAndEmptyControlPlaneDir(t *testing.T) {
	oldSrv, oldDir := guestServer(t, "guest-token")
	newSrv, newDir := guestServer(t, "guest-token")
	big := bytes.Repeat([]byte("x"), (32<<20)+1)
	for i, body := range [][]byte{big, big} {
		if err := os.WriteFile(filepath.Join(oldDir, "workspace", "big-"+strconv.Itoa(i)+".bin"), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 499; i++ {
		if err := os.WriteFile(filepath.Join(oldDir, "workspace", "n"+strconv.Itoa(i)+".txt"), []byte("z"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, agent := castletAgent(t, oldSrv.URL)
	h := &castHost{Fake: host.NewFake(), oldURL: oldSrv.URL, newURL: newSrv.URL, token: "guest-token"}
	h.Machines["vm-old"] = host.Machine{ID: "vm-old", State: "running", Endpoint: oldSrv.URL, Tags: []string{"agent:" + agent.ID}}
	h.OnCreate = func(spec host.CreateSpec) {
		next, err := st.ExchangeBootstrap(context.Background(), spec.AgentID, spec.Bootstrap)
		if err != nil {
			t.Errorf("exchange %v", err)
			return
		}
		if _, err := st.PlaceVMToken(context.Background(), spec.AgentID, next); err != nil {
			t.Errorf("place %v", err)
		}
	}
	data := t.TempDir()
	res, err := replace.Run(context.Background(), st, h, replace.Options{
		UserID: agent.UserID, AgentID: agent.ID, ActorKind: "cli", ActorID: "cli", Actor: "cli",
		Image: "example.invalid/castbot-runtime:ff6d7ea", PublicURL: "https://castbot.example",
		DataDir: data, BootWait: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 502 {
		t.Fatalf("files %d", res.Files)
	}
	got, err := os.ReadFile(filepath.Join(newDir, "workspace", "big-0.bin"))
	if err != nil || len(got) != len(big) {
		t.Fatalf("big %d %v", len(got), err)
	}
	entries, err := os.ReadDir(data)
	if err != nil || len(entries) != 0 {
		t.Fatalf("control plane stored %d files", len(entries))
	}
}

func TestArchiveBridgeLeavesLocalBehind(t *testing.T) {
	oldSrv, oldDir := guestServer(t, "guest-token")
	newSrv, newDir := guestServer(t, "guest-token")
	if err := os.MkdirAll(filepath.Join(oldDir, ".local"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, ".local", "pkg"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("q"), (32<<20)+1)
	if err := os.WriteFile(filepath.Join(oldDir, "workspace", "big.bin"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	st, agent := castletAgent(t, oldSrv.URL)
	h := &castHost{Fake: host.NewFake(), oldURL: oldSrv.URL, newURL: newSrv.URL, token: "guest-token", hideArchive: true}
	h.Machines["vm-old"] = host.Machine{ID: "vm-old", State: "running", Endpoint: oldSrv.URL, Tags: []string{"agent:" + agent.ID}}
	h.OnCreate = func(spec host.CreateSpec) {
		next, err := st.ExchangeBootstrap(context.Background(), spec.AgentID, spec.Bootstrap)
		if err != nil {
			t.Errorf("exchange %v", err)
			return
		}
		if _, err := st.PlaceVMToken(context.Background(), spec.AgentID, next); err != nil {
			t.Errorf("place %v", err)
		}
	}
	if _, err := replace.Run(context.Background(), st, h, replace.Options{
		UserID: agent.UserID, AgentID: agent.ID, ActorKind: "cli", ActorID: "cli", Actor: "cli",
		Image: "example.invalid/castbot-runtime:ff6d7ea", PublicURL: "https://castbot.example",
		DataDir: t.TempDir(), BootWait: time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	if h.exports != 1 || h.imports != 1 {
		t.Fatalf("archive calls export %d import %d", h.exports, h.imports)
	}
	got, err := os.ReadFile(filepath.Join(newDir, "workspace", "uploads", "hello.txt"))
	if err != nil || string(got) != "hello file" {
		t.Fatalf("file %q %v", got, err)
	}
	copied, err := os.ReadFile(filepath.Join(newDir, "workspace", "big.bin"))
	if err != nil || len(copied) != len(big) {
		t.Fatalf("big %d %v", len(copied), err)
	}
	mem, err := os.ReadFile(filepath.Join(newDir, "memory", "note.txt"))
	if err != nil || string(mem) != "remember" {
		t.Fatalf("memory %q %v", mem, err)
	}
	if _, err := os.Stat(filepath.Join(newDir, ".local", "pkg")); !os.IsNotExist(err) {
		t.Fatal("bridge copied .local")
	}
	kept, err := os.ReadFile(filepath.Join(oldDir, ".local", "pkg"))
	if err != nil || string(kept) != "keep" {
		t.Fatal("old local changed")
	}
}

func TestArchiveFallsBackWhenNewGuestHasNoArchive(t *testing.T) {
	oldSrv, oldDir := guestServer(t, "guest-token")
	newSrv, newDir := guestServer(t, "guest-token")
	if err := os.MkdirAll(filepath.Join(oldDir, ".local"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, ".local", "pkg"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, agent := castletAgent(t, oldSrv.URL)
	h := &castHost{Fake: host.NewFake(), oldURL: oldSrv.URL, newURL: newSrv.URL, token: "guest-token", noArchive: true}
	h.Machines["vm-old"] = host.Machine{ID: "vm-old", State: "running", Endpoint: oldSrv.URL, Tags: []string{"agent:" + agent.ID}}
	h.OnCreate = func(spec host.CreateSpec) {
		next, err := st.ExchangeBootstrap(context.Background(), spec.AgentID, spec.Bootstrap)
		if err != nil {
			t.Errorf("exchange %v", err)
			return
		}
		if _, err := st.PlaceVMToken(context.Background(), spec.AgentID, next); err != nil {
			t.Errorf("place %v", err)
		}
	}
	res, err := replace.Run(context.Background(), st, h, replace.Options{
		UserID: agent.UserID, AgentID: agent.ID, ActorKind: "cli", ActorID: "cli", Actor: "cli",
		Image: "example.invalid/castbot-runtime:ff6d7ea", PublicURL: "https://castbot.example",
		DataDir: t.TempDir(), BootWait: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 1 || h.exports != 0 || h.imports != 0 {
		t.Fatalf("files %d exports %d imports %d", res.Files, h.exports, h.imports)
	}
	got, err := os.ReadFile(filepath.Join(newDir, "workspace", "uploads", "hello.txt"))
	if err != nil || string(got) != "hello file" {
		t.Fatalf("file %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(newDir, ".local", "pkg")); !os.IsNotExist(err) {
		t.Fatal("per-file copy included .local")
	}
}

func TestArchiveTooBigAndBadHashKeepTheOldMachine(t *testing.T) {
	for _, mode := range []string{"disk", "hash"} {
		t.Run(mode, func(t *testing.T) {
			oldSrv, oldDir := guestServer(t, "guest-token")
			newSrv, _ := guestServer(t, "guest-token")
			st, agent := castletAgent(t, oldSrv.URL)
			h := &castHost{Fake: host.NewFake(), oldURL: oldSrv.URL, newURL: newSrv.URL, token: "guest-token", badHash: mode == "hash"}
			h.Machines["vm-old"] = host.Machine{ID: "vm-old", State: "running", Endpoint: oldSrv.URL, Tags: []string{"agent:" + agent.ID}}
			h.OnCreate = func(spec host.CreateSpec) {
				next, err := st.ExchangeBootstrap(context.Background(), spec.AgentID, spec.Bootstrap)
				if err != nil {
					t.Errorf("exchange %v", err)
					return
				}
				if _, err := st.PlaceVMToken(context.Background(), spec.AgentID, next); err != nil {
					t.Errorf("place %v", err)
				}
			}
			opt := replace.Options{
				UserID: agent.UserID, AgentID: agent.ID, ActorKind: "cli", ActorID: "cli", Actor: "cli",
				Image: "example.invalid/castbot-runtime:ff6d7ea", PublicURL: "https://castbot.example",
				DataDir: t.TempDir(), BootWait: time.Second,
			}
			if mode == "disk" {
				opt.ArchiveLimit = 32
			}
			_, err := replace.Run(context.Background(), st, h, opt)
			want := "could not be copied"
			if mode == "disk" {
				want = "does not fit"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatal(err)
			}
			live, err := st.LiveVM(context.Background(), agent.UserID, agent.ID)
			if err != nil || live.MicroVMID != "vm-old" {
				t.Fatalf("old not kept %+v %v", live, err)
			}
			mem, err := os.ReadFile(filepath.Join(oldDir, "memory", "note.txt"))
			if err != nil || string(mem) != "remember" {
				t.Fatal("old memory changed")
			}
		})
	}
}

func TestReplaceTruncatedTarKeepsOld(t *testing.T) {
	oldSrv, oldDir := guestServer(t, "guest-token")
	newSrv, _ := guestServer(t, "guest-token")
	st, agent := castletAgent(t, oldSrv.URL)
	h := &castHost{Fake: host.NewFake(), oldURL: oldSrv.URL, newURL: newSrv.URL, token: "guest-token", truncate: true}
	h.Machines["vm-old"] = host.Machine{ID: "vm-old", State: "running", Endpoint: oldSrv.URL, Tags: []string{"agent:" + agent.ID}}
	h.OnCreate = func(spec host.CreateSpec) {
		next, err := st.ExchangeBootstrap(context.Background(), spec.AgentID, spec.Bootstrap)
		if err != nil {
			t.Errorf("exchange %v", err)
			return
		}
		if _, err := st.PlaceVMToken(context.Background(), spec.AgentID, next); err != nil {
			t.Errorf("place %v", err)
		}
	}
	_, err := replace.Run(context.Background(), st, h, replace.Options{
		UserID: agent.UserID, AgentID: agent.ID, ActorKind: "cli", ActorID: "cli", Actor: "cli",
		Image: "example.invalid/castbot-runtime:ff6d7ea", PublicURL: "https://castbot.example",
		DataDir: t.TempDir(), BootWait: time.Second,
	})
	var kept *replace.Kept
	if err == nil || !strings.Contains(err.Error(), "could not be copied") {
		t.Fatal(err)
	}
	_ = kept
	live, err := st.LiveVM(context.Background(), agent.UserID, agent.ID)
	if err != nil || live.MicroVMID != "vm-old" {
		t.Fatalf("old not kept %+v %v", live, err)
	}
	mem, err := os.ReadFile(filepath.Join(oldDir, "memory", "note.txt"))
	if err != nil || string(mem) != "remember" {
		t.Fatal("old memory changed")
	}
}

type castHost struct {
	*host.Fake
	oldURL, newURL, token string
	truncate              bool
	hideArchive           bool
	noArchive             bool
	badHash               bool
	reloads               int
	exports               int
	imports               int
}

func (c *castHost) Create(ctx context.Context, spec host.CreateSpec) (host.Machine, error) {
	m, err := c.Fake.Create(ctx, spec)
	if err != nil {
		return m, err
	}
	m.Endpoint = c.newURL
	m.State = "running"
	c.Fake.Machines[m.ID] = m
	return m, nil
}

func (c *castHost) Health(context.Context, string, string) (string, error) {
	return "", nil
}

func (c *castHost) ExportMemory(ctx context.Context, endpoint, _ string) (io.ReadCloser, error) {
	return c.open(ctx, http.MethodGet, endpoint+"/castlet/memory", nil)
}

func (c *castHost) ImportMemory(ctx context.Context, endpoint, _ string, body io.Reader) error {
	raw, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if c.truncate && len(raw) > 4 {
		raw = raw[:4]
	}
	_, err = c.open(ctx, http.MethodPut, endpoint+"/castlet/memory", strings.NewReader(string(raw)))
	return err
}

func (c *castHost) ListWorkspace(ctx context.Context, endpoint, _ string) ([]host.WorkspaceFile, error) {
	res, err := c.do(ctx, http.MethodGet, endpoint+"/castlet/files", nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var listed []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if err := json.NewDecoder(res.Body).Decode(&listed); err != nil {
		return nil, err
	}
	out := make([]host.WorkspaceFile, 0, len(listed))
	for _, item := range listed {
		out = append(out, host.WorkspaceFile{Name: item.Name, Size: item.Size})
	}
	return out, nil
}

func (c *castHost) ReadWorkspace(ctx context.Context, endpoint, _, name string) (io.ReadCloser, error) {
	return c.open(ctx, http.MethodGet, endpoint+"/castlet/files/"+name, nil)
}

func (c *castHost) WriteWorkspace(ctx context.Context, endpoint, _, name string, body io.Reader) error {
	_, err := c.open(ctx, http.MethodPut, endpoint+"/castlet/files/"+name, body)
	return err
}

func (c *castHost) ExportState(ctx context.Context, endpoint, _ string) (io.ReadCloser, error) {
	if c.noArchive || (c.hideArchive && endpoint == c.oldURL) {
		return nil, host.ErrNoArchive
	}
	c.exports++
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/castlet/archive", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusNotFound {
		res.Body.Close()
		return nil, host.ErrNoArchive
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, host.ErrGuest
	}
	return res.Body, nil
}

func (c *castHost) ImportState(ctx context.Context, endpoint, _ string, body io.Reader, limit int64) (host.StateReceipt, error) {
	c.imports++
	if c.badHash {
		_, _ = io.Copy(io.Discard, body)
		return host.StateReceipt{Sum: [32]byte{9}}, nil
	}
	if c.truncate {
		body = io.LimitReader(body, 4)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint+"/castlet/archive", body)
	if err != nil {
		return host.StateReceipt{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if limit > 0 {
		req.Header.Set("X-Castbot-Archive-Limit", strconv.FormatInt(limit, 10))
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return host.StateReceipt{}, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusRequestEntityTooLarge {
		return host.StateReceipt{}, host.ErrArchiveTooBig
	}
	if res.StatusCode != http.StatusOK {
		return host.StateReceipt{}, host.ErrGuest
	}
	var parsed struct {
		SHA256 string `json:"sha256"`
		Files  int    `json:"files"`
		Memory int64  `json:"memory"`
		Bytes  int64  `json:"bytes"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return host.StateReceipt{}, host.ErrGuest
	}
	decoded, err := hex.DecodeString(parsed.SHA256)
	if err != nil || len(decoded) != sha256.Size {
		return host.StateReceipt{}, host.ErrGuest
	}
	var sum [32]byte
	copy(sum[:], decoded)
	return host.StateReceipt{Sum: sum, Files: parsed.Files, Memory: parsed.Memory, Bytes: parsed.Bytes}, nil
}

func (c *castHost) Reload(context.Context, string, string) error {
	c.reloads++
	return nil
}

func (c *castHost) PullJournal(context.Context, string, string, int) ([]byte, error) {
	return []byte(`{"entries":[]}`), nil
}

func (c *castHost) open(ctx context.Context, method, url string, body io.Reader) (io.ReadCloser, error) {
	res, err := c.do(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusNoContent {
		res.Body.Close()
		return nil, host.ErrGuest
	}
	return res.Body, nil
}

func (c *castHost) do(ctx context.Context, method, url string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusNoContent {
		res.Body.Close()
		return nil, host.ErrGuest
	}
	return res, nil
}

func guestServer(t *testing.T, token string) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "workspace", "uploads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace", "uploads", "hello.txt"), []byte("hello file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory", "note.txt"), []byte("remember"), 0o644); err != nil {
		t.Fatal(err)
	}
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "Atlas"})
	t.Cleanup(echo.Close)
	up := httptest.NewServer(echo)
	t.Cleanup(up.Close)
	sup, err := New(Config{Token: token, RuntimeURL: up.URL, AgentDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	sup.mu.Lock()
	sup.ready = true
	sup.mu.Unlock()
	srv := httptest.NewServer(sup)
	t.Cleanup(srv.Close)
	return srv, dir
}

func castletAgent(t *testing.T, endpoint string) (*store.Store, store.Agent) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), store.Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	hash, err := auth.Hash("right-password-1", st.Argon())
	if err != nil {
		t.Fatal(err)
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(ctx, store.NewUser{Email: "ada@example.com", DisplayName: "Ada", Workspace: "Personal", PasswordHash: hash, TOTPSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	sec, err := st.CreateSection(ctx, user.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := st.CreateAgent(ctx, user.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginVM(ctx, user.ID, agent.ID, "castbot-old", "example.invalid/castbot-runtime:7b5e414"); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishVM(ctx, user.ID, agent.ID, "vm-old", endpoint, "running", ""); err != nil {
		t.Fatal(err)
	}
	boot, err := st.IssueBootstrap(ctx, user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := st.ExchangeBootstrap(ctx, agent.ID, boot)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVMToken(ctx, agent.ID, token); err != nil {
		t.Fatal(err)
	}
	return st, agent
}
