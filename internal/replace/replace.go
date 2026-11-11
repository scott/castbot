// Package replace moves one agent onto a new machine.
// The new machine stays out of agent_vms until the switch transaction.
package replace

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

const (
	// MaxMemory and MaxFile match the guest import caps.
	MaxMemory = 32 << 20
	MaxFile   = 32 << 20
	// MaxFiles and MaxBytes are the workspace cap for one replace.
	MaxFiles = 500
	MaxBytes = 64 << 20

	bootDefault = 4 * time.Minute
)

// Blocked is a precondition. The old machine is unchanged.
type Blocked struct{ Line string }

func (e *Blocked) Error() string {
	if e == nil || e.Line == "" {
		return "replace blocked"
	}
	return e.Line
}

// Kept means the replace failed and the old machine is still the live one.
type Kept struct{ Line string }

func (e *Kept) Error() string {
	if e == nil || e.Line == "" {
		return "The machine was not replaced."
	}
	return e.Line
}

// NeedsHuman means the switch stands and the old machine is not deleted yet.
type NeedsHuman struct{ Line string }

func (e *NeedsHuman) Error() string {
	if e == nil || e.Line == "" {
		return "The old machine is not deleted yet. castbot will keep trying."
	}
	return e.Line
}

// ErrHalted is a test crash. The row is left as it was so recovery can finish it.
var ErrHalted = errors.New("replace halted")

// Options is one replace. NoCopy is the admin override. It is never a web control.
type Options struct {
	UserID    string
	AgentID   string
	ActorKind string
	ActorID   string
	Actor     string
	Image     string
	PublicURL string
	NoCopy    bool
	DataDir   string
	BootWait  time.Duration
	// HaltAt stops after that step and leaves the row for Recover.
	// Steps: lock, create, boot, read_old, copy, verify, switch, delete.
	HaltAt   string
	Progress func(title string)
	Logger   *slog.Logger
}

// Result is the outcome the UI and the admin command print.
type Result struct {
	ID            string
	AgentName     string
	OldImage      string
	NewImage      string
	Files         int
	MemoryBytes   int64
	DeletePending bool
	NoCopy        bool
	Woke          bool
}

// Plan is the dry-run report. Building it does not call a machine API.
type Plan struct {
	AgentName    string
	AgentID      string
	MachineID    string
	State        string
	CurrentImage string
	TargetImage  string
	Block        string
}

// Reason is the one bold line for a failure class.
func Reason(class string) string {
	switch class {
	case "create", "boot":
		return "The new machine did not start."
	case "read_old":
		return "The old machine could not be read."
	case "copy":
		return "Memory or files could not be copied."
	default:
		return "The new machine did not pass its health check."
	}
}

// ImageTag is the short tag after the last colon.
func ImageTag(ref string) string {
	ref = strings.TrimSpace(ref)
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		tag := ref[i+1:]
		if tag != "" && !strings.Contains(tag, "/") {
			return tag
		}
	}
	return ref
}

// UIStep is the 1-based step the machine page shows.
func UIStep(step string) (n int, title, detail string) {
	switch step {
	case "read_old", "copy", "verify":
		return 2, "Copying memory and files", ""
	case "switch", "switched":
		return 3, "Switching over", ""
	case "delete", "done":
		return 4, "Removing the old machine", ""
	default:
		return 1, "Starting a new machine", "Waiting for the new machine to be ready · up to 4 min"
	}
}

// DryRun reads the database and reports the plan. It takes no host.
func DryRun(ctx context.Context, st *store.Store, userID, agentID, image string) (Plan, error) {
	agent, err := st.GetAgent(ctx, userID, agentID)
	if err != nil {
		return Plan{}, err
	}
	vm, line, err := st.ReplacePrecheck(ctx, userID, agentID)
	if err != nil {
		return Plan{}, err
	}
	return Plan{
		AgentName:    agent.Name,
		AgentID:      agent.ID,
		MachineID:    prefix(vm.MicroVMID),
		State:        vm.State,
		CurrentImage: vm.Image,
		TargetImage:  image,
		Block:        line,
	}, nil
}

func prefix(id string) string {
	id = strings.TrimSpace(id)
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// Run performs one replace. A failure before the switch deletes the new
// machine and leaves the old one in place.
func Run(ctx context.Context, st *store.Store, h host.AgentHost, opt Options) (Result, error) {
	if st == nil || h == nil || strings.TrimSpace(opt.Image) == "" {
		return Result{}, &Blocked{Line: "No machine host is configured."}
	}
	if opt.BootWait <= 0 {
		opt.BootWait = bootDefault
	}
	if opt.Logger == nil {
		opt.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	agent, err := st.GetAgent(ctx, opt.UserID, opt.AgentID)
	if err != nil {
		return Result{}, err
	}
	row, vm, err := st.BeginReplacement(ctx, opt.UserID, opt.AgentID, opt.Actor, opt.Image, opt.NoCopy)
	if err != nil {
		var block *store.ReplaceBlock
		if errors.As(err, &block) {
			return Result{}, &Blocked{Line: block.Line}
		}
		return Result{}, err
	}
	res := Result{ID: row.ID, AgentName: agent.Name, OldImage: vm.Image, NewImage: opt.Image, NoCopy: opt.NoCopy}
	_ = st.AddVMEventFor(ctx, opt.UserID, opt.AgentID, row.OldVMID, store.EventReplaceRequest, 0)
	_ = st.WriteMachineAudit(ctx, opt.UserID, opt.ActorKind, opt.ActorID, "machine.replace_requested", opt.AgentID, ImageTag(vm.Image), ImageTag(opt.Image), opt.Actor)
	if opt.HaltAt == "lock" {
		return res, ErrHalted
	}
	r := &runner{ctx: ctx, st: st, h: h, opt: opt, row: row, vm: vm, agent: agent, res: res}
	out, err := r.run()
	if errors.Is(err, ErrHalted) {
		return out, err
	}
	var ce *classError
	if errors.As(err, &ce) {
		r.rollback(ce.step, ce.class)
		out.Files = 0
		return out, &Kept{Line: Reason(ce.class)}
	}
	return out, err
}

type classError struct{ step, class string }

func (e *classError) Error() string { return e.class }

type runner struct {
	ctx   context.Context
	st    *store.Store
	h     host.AgentHost
	opt   Options
	row   store.Replacement
	vm    store.VM
	agent store.Agent
	res   Result
	token string
	ui    string
}

func (r *runner) run() (Result, error) {
	r.uiStep("create")
	if err := r.create(); err != nil {
		return r.res, err
	}
	if err := r.halt("create"); err != nil {
		return r.res, err
	}
	if err := r.boot(); err != nil {
		return r.res, err
	}
	if err := r.halt("boot"); err != nil {
		return r.res, err
	}
	if r.opt.NoCopy {
		_ = r.st.AddVMEventFor(r.ctx, r.opt.UserID, r.opt.AgentID, r.row.OldVMID, store.EventReplaceNoCopy, 0)
		if err := r.step("verify"); err != nil {
			return r.res, err
		}
		if _, err := r.healthy(r.row.NewEndpoint, r.token); err != nil {
			return r.res, &classError{step: "verify", class: "verify"}
		}
		if err := r.halt("verify"); err != nil {
			return r.res, err
		}
	} else {
		r.uiStep("read_old")
		mem, files, err := r.readOld()
		if err != nil {
			return r.res, err
		}
		if err := r.halt("read_old"); err != nil {
			r.cleanup(mem, files)
			return r.res, err
		}
		if err := r.writeNew(mem, files); err != nil {
			r.cleanup(mem, files)
			return r.res, err
		}
		r.cleanup(mem, files)
		if err := r.halt("copy"); err != nil {
			return r.res, err
		}
		if err := r.verify(mem, files); err != nil {
			return r.res, err
		}
		if err := r.halt("verify"); err != nil {
			return r.res, err
		}
	}
	version, _ := r.healthy(r.row.NewEndpoint, r.token)
	if err := r.switchTo(version); err != nil {
		return r.res, err
	}
	if err := r.halt("switch"); err != nil {
		return r.res, err
	}
	r.uiStep("delete")
	if err := r.step("delete"); err != nil {
		return r.res, err
	}
	if err := r.halt("delete"); err != nil {
		return r.res, err
	}
	pending := r.deleteOld()
	r.res.DeletePending = pending
	r.pushCLI()
	if err := r.st.FinishReplacement(r.ctx, r.opt.UserID, r.row.ID, pending); err != nil {
		return r.res, err
	}
	_ = r.st.AddVMEventFor(r.ctx, r.opt.UserID, r.opt.AgentID, r.row.NewVMID, store.EventReplaceSwitch, 0)
	_ = r.st.WriteMachineAudit(r.ctx, r.opt.UserID, r.opt.ActorKind, r.opt.ActorID, "machine.replace_succeeded", r.opt.AgentID, ImageTag(r.vm.Image), ImageTag(r.opt.Image), r.opt.Actor)
	if pending {
		_ = r.st.AddVMEventFor(r.ctx, r.opt.UserID, r.opt.AgentID, r.row.OldVMID, store.EventReplaceDelete, 0)
		return r.res, &NeedsHuman{Line: "The old machine is not deleted yet. castbot will keep trying."}
	}
	return r.res, nil
}

func (r *runner) create() error {
	if err := r.step("create"); err != nil {
		return err
	}
	boot, err := r.st.IssueBootstrapFor(r.ctx, r.opt.UserID, r.opt.AgentID, r.row.NewVMID)
	if err != nil {
		return &classError{step: "create", class: "create"}
	}
	inst, err := r.st.InstanceID(r.ctx)
	if err != nil {
		return &classError{step: "create", class: "create"}
	}
	idle := time.Duration(r.agent.IdleTimeout) * time.Second
	if idle <= 0 {
		idle = host.DefaultIdle
	}
	name := machineName(r.opt.AgentID)
	created, err := r.h.Create(r.ctx, host.CreateSpec{
		Name: name, AgentID: r.opt.AgentID, Image: r.opt.Image,
		Region: host.DefaultRegion, CPU: host.DefaultCPU, MemoryMiB: host.DefaultMemoryMiB,
		Idle: idle, HTTPPort: host.DefaultHTTPPort,
		ControlURL: r.opt.PublicURL, Bootstrap: boot,
		Tags: []string{host.InstanceTag(inst), host.ReplaceTag(r.row.ID)},
	})
	if err != nil || created.ID == "" || created.Endpoint == "" || created.State != "running" {
		if created.ID != "" {
			r.row.NewMicro = created.ID
			_ = r.st.NoteReplacementMachine(r.ctx, r.opt.UserID, r.row.ID, created.ID, created.Endpoint, name)
		}
		return &classError{step: "create", class: "create"}
	}
	r.row.NewMicro = created.ID
	r.row.NewEndpoint = created.Endpoint
	r.row.NewName = name
	if err := r.st.NoteReplacementMachine(r.ctx, r.opt.UserID, r.row.ID, created.ID, created.Endpoint, name); err != nil {
		return &classError{step: "create", class: "create"}
	}
	return nil
}

func (r *runner) boot() error {
	if err := r.step("boot"); err != nil {
		return err
	}
	want, _, err := r.st.BundleVersions(r.ctx, r.opt.UserID, r.opt.AgentID)
	if err != nil {
		return &classError{step: "boot", class: "boot"}
	}
	deadline := time.Now().Add(r.opt.BootWait)
	for {
		if err := r.ctx.Err(); err != nil {
			return &classError{step: "boot", class: "boot"}
		}
		token, terr := r.st.ReplacementToken(r.ctx, r.opt.UserID, r.row.ID)
		ver, herr := r.healthy(r.row.NewEndpoint, token)
		if terr == nil && token != "" && herr == nil && (want == "" || ver == want) {
			r.token = token
			return nil
		}
		if errors.Is(herr, host.ErrGuest) {
			return &classError{step: "boot", class: "boot"}
		}
		if time.Now().After(deadline) {
			return &classError{step: "boot", class: "boot"}
		}
		select {
		case <-r.ctx.Done():
			return &classError{step: "boot", class: "boot"}
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (r *runner) healthy(endpoint, token string) (string, error) {
	if endpoint == "" || token == "" {
		return "", host.ErrGuestWait
	}
	return r.h.Health(r.ctx, endpoint, token)
}

func (r *runner) readOld() (*saved, []savedFile, error) {
	if err := r.step("read_old"); err != nil {
		return nil, nil, err
	}
	if r.vm.State == "paused" {
		started := time.Now()
		_ = r.st.AddVMEvent(r.ctx, r.opt.UserID, r.opt.AgentID, store.EventWakeRequested, 0)
		if _, err := r.h.Wake(r.ctx, r.vm.MicroVMID); err != nil {
			return nil, nil, &classError{step: "read_old", class: "read_old"}
		}
		_ = r.st.AddVMEvent(r.ctx, r.opt.UserID, r.opt.AgentID, store.EventWakeRunning, time.Since(started))
		r.res.Woke = true
		r.row.Woke = true
	}
	endpoint, token, err := r.st.VMCredentials(r.ctx, r.opt.UserID, r.opt.AgentID)
	if err != nil || endpoint == "" || token == "" {
		return nil, nil, &classError{step: "read_old", class: "read_old"}
	}
	r.pullJournal(endpoint, token)
	mem, err := r.saveMemory(endpoint, token)
	if err != nil {
		return nil, nil, readErr(err)
	}
	files, err := r.saveFiles(endpoint, token)
	if err != nil {
		r.cleanup(mem, files)
		return nil, nil, readErr(err)
	}
	r.res.Files = len(files)
	r.res.MemoryBytes = mem.size
	var total int64
	for _, f := range files {
		total += f.size
	}
	_ = r.st.NoteReplacementCopy(r.ctx, r.opt.UserID, r.row.ID, r.res.Woke, mem.size, len(files), len(files), total)
	return mem, files, nil
}

func (r *runner) writeNew(mem *saved, files []savedFile) error {
	if err := r.step("copy"); err != nil {
		return err
	}
	mf, err := os.Open(mem.path)
	if err != nil {
		return &classError{step: "copy", class: "copy"}
	}
	err = r.h.ImportMemory(r.ctx, r.row.NewEndpoint, r.token, mf)
	mf.Close()
	if err != nil {
		return &classError{step: "copy", class: "copy"}
	}
	for _, f := range files {
		body, err := os.Open(f.path)
		if err != nil {
			return &classError{step: "copy", class: "copy"}
		}
		err = r.h.WriteWorkspace(r.ctx, r.row.NewEndpoint, r.token, f.name, body)
		body.Close()
		if err != nil {
			return &classError{step: "copy", class: "copy"}
		}
	}
	if err := r.h.Reload(r.ctx, r.row.NewEndpoint, r.token); err != nil {
		return &classError{step: "copy", class: "copy"}
	}
	return nil
}

func (r *runner) verify(mem *saved, files []savedFile) error {
	if err := r.step("verify"); err != nil {
		return err
	}
	if _, err := r.healthy(r.row.NewEndpoint, r.token); err != nil {
		return &classError{step: "verify", class: "verify"}
	}
	if !r.sameMemory(mem) {
		return &classError{step: "verify", class: "verify"}
	}
	listed, err := r.h.ListWorkspace(r.ctx, r.row.NewEndpoint, r.token)
	if err != nil || len(listed) != len(files) {
		return &classError{step: "verify", class: "verify"}
	}
	var total int64
	seen := map[string]int64{}
	for _, item := range listed {
		seen[item.Name] = item.Size
		total += item.Size
	}
	var want int64
	for _, f := range files {
		want += f.size
		if seen[f.name] != f.size {
			return &classError{step: "verify", class: "verify"}
		}
		sum, err := r.hashFile(r.row.NewEndpoint, r.token, f.name)
		if err != nil || sum != f.sum {
			return &classError{step: "verify", class: "verify"}
		}
	}
	if total != want {
		return &classError{step: "verify", class: "verify"}
	}
	return nil
}

func (r *runner) switchTo(version string) error {
	if err := r.step("switch"); err != nil {
		return err
	}
	if err := r.st.SwitchReplacement(r.ctx, r.opt.UserID, r.row.ID, r.token, version); err != nil {
		return &classError{step: "switch", class: "verify"}
	}
	r.row.Step = "switched"
	return nil
}

func (r *runner) deleteOld() bool {
	ok, gone := deleteMachine(r.ctx, r.h, r.vm.MicroVMID, r.opt.AgentID, "")
	if gone || ok {
		return false
	}
	return true
}

func (r *runner) pushCLI() {
	endpoint, token, err := r.st.VMCredentials(r.ctx, r.opt.UserID, r.opt.AgentID)
	if err != nil || endpoint == "" || token == "" {
		return
	}
	hashes, err := r.st.ListCLITokenHashes(r.ctx, r.opt.UserID, r.opt.AgentID)
	if err != nil {
		return
	}
	if hashes == nil {
		hashes = []store.CLIHash{}
	}
	body, err := json.Marshal(map[string]any{"tokens": hashes})
	if err != nil {
		return
	}
	_ = r.h.PushCLI(r.ctx, endpoint, token, body)
}

func (r *runner) pullJournal(endpoint, token string) {
	after, err := r.st.JournalCursor(r.ctx, r.opt.UserID, r.opt.AgentID)
	if err != nil {
		return
	}
	raw, err := r.h.PullJournal(r.ctx, endpoint, token, after)
	if err != nil || len(raw) == 0 {
		return
	}
	var body struct {
		Entries []store.JournalEntry `json:"entries"`
	}
	if json.Unmarshal(raw, &body) != nil || len(body.Entries) == 0 {
		return
	}
	_, _ = r.st.IngestJournal(r.ctx, r.opt.UserID, r.opt.AgentID, body.Entries)
}

func (r *runner) rollback(step, class string) {
	if r.row.NewMicro != "" {
		_, _ = deleteMachine(r.ctx, r.h, r.row.NewMicro, r.opt.AgentID, host.ReplaceTag(r.row.ID))
	} else {
		_ = deleteByTag(r.ctx, r.h, host.ReplaceTag(r.row.ID))
	}
	if err := r.st.FailReplacement(r.ctx, r.opt.UserID, r.row.ID, step, class); err != nil {
		r.opt.Logger.Info("replace fail row", "agent", r.opt.AgentID, "class", class)
		return
	}
	_ = r.st.AddVMEventFor(r.ctx, r.opt.UserID, r.opt.AgentID, r.row.OldVMID, store.EventReplaceFailed, 0)
	_ = r.st.WriteMachineAudit(r.ctx, r.opt.UserID, r.opt.ActorKind, r.opt.ActorID, "machine.replace_failed", r.opt.AgentID, ImageTag(r.vm.Image), ImageTag(r.opt.Image), r.opt.Actor)
}

func (r *runner) step(name string) error {
	r.row.Step = name
	if err := r.st.SetReplacementStep(r.ctx, r.opt.UserID, r.row.ID, name); err != nil {
		return &classError{step: name, class: classForStep(name)}
	}
	return nil
}

func (r *runner) halt(step string) error {
	if r.opt.HaltAt == step {
		return ErrHalted
	}
	return nil
}

func (r *runner) uiStep(name string) {
	_, title, _ := UIStep(name)
	if title == r.ui || r.opt.Progress == nil {
		r.ui = title
		return
	}
	r.ui = title
	r.opt.Progress(title)
}

type saved struct {
	path string
	sum  [32]byte
	size int64
}

type savedFile struct {
	name string
	path string
	sum  [32]byte
	size int64
}

func (r *runner) saveMemory(endpoint, token string) (*saved, error) {
	body, err := r.h.ExportMemory(r.ctx, endpoint, token)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	path, sum, n, err := writeLimit(r.opt.DataDir, "memory-", body, MaxMemory)
	if err != nil {
		return &saved{path: path}, err
	}
	return &saved{path: path, sum: sum, size: n}, nil
}

func (r *runner) saveFiles(endpoint, token string) ([]savedFile, error) {
	listed, err := r.h.ListWorkspace(r.ctx, endpoint, token)
	if err != nil {
		return nil, err
	}
	if len(listed) > MaxFiles {
		return nil, errCap
	}
	var total int64
	for _, item := range listed {
		if item.Size < 0 || item.Size > MaxFile || !oneName(item.Name) {
			return nil, errCap
		}
		total += item.Size
	}
	if total > MaxBytes {
		return nil, errCap
	}
	out := make([]savedFile, 0, len(listed))
	for i, item := range listed {
		body, err := r.h.ReadWorkspace(r.ctx, endpoint, token, item.Name)
		if err != nil {
			r.cleanup(nil, out)
			return nil, err
		}
		path, sum, n, err := writeLimit(r.opt.DataDir, "file-", body, MaxFile)
		body.Close()
		if err != nil || n != item.Size {
			if path != "" {
				os.Remove(path)
			}
			r.cleanup(nil, out)
			if err == nil {
				err = errCap
			}
			return nil, err
		}
		out = append(out, savedFile{name: item.Name, path: path, sum: sum, size: n})
		_ = r.st.NoteReplacementCopy(r.ctx, r.opt.UserID, r.row.ID, r.res.Woke, r.res.MemoryBytes, i+1, len(listed), total)
		_ = i
	}
	return out, nil
}

func (r *runner) sameMemory(mem *saved) bool {
	body, err := r.h.ExportMemory(r.ctx, r.row.NewEndpoint, r.token)
	if err != nil {
		return false
	}
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, MaxMemory+1))
	if err != nil || int64(len(raw)) > MaxMemory {
		return false
	}
	if sha256.Sum256(raw) == mem.sum {
		return true
	}
	// A guest re-packs the tar, so headers can differ while the files match.
	got, err := tarDigest(raw)
	if err != nil {
		return false
	}
	saved, err := os.ReadFile(mem.path)
	if err != nil {
		return false
	}
	want, err := tarDigest(saved)
	return err == nil && got == want
}

func tarDigest(raw []byte) ([32]byte, error) {
	tr := tar.NewReader(bytes.NewReader(raw))
	type part struct {
		name string
		sum  [32]byte
	}
	var parts []part
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return [32]byte{}, err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		sum, err := hashLimit(tr, MaxMemory)
		if err != nil {
			return [32]byte{}, err
		}
		parts = append(parts, part{name: hdr.Name, sum: sum})
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].name < parts[j].name })
	h := sha256.New()
	for _, p := range parts {
		_, _ = io.WriteString(h, p.name)
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(p.sum[:])
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

func (r *runner) hashFile(endpoint, token, name string) ([32]byte, error) {
	body, err := r.h.ReadWorkspace(r.ctx, endpoint, token, name)
	if err != nil {
		return [32]byte{}, err
	}
	defer body.Close()
	return hashLimit(body, MaxFile)
}

func (r *runner) cleanup(mem *saved, files []savedFile) {
	if mem != nil && mem.path != "" {
		os.Remove(mem.path)
	}
	for _, f := range files {
		if f.path != "" {
			os.Remove(f.path)
		}
	}
}

var errCap = errors.New("cap")

func readErr(err error) error {
	if errors.Is(err, errCap) {
		return &classError{step: "copy", class: "copy"}
	}
	return &classError{step: "read_old", class: "read_old"}
}

func classForStep(step string) string {
	switch step {
	case "boot":
		return "boot"
	case "read_old":
		return "read_old"
	case "copy":
		return "copy"
	case "verify", "switch":
		return "verify"
	default:
		return "create"
	}
}

func writeLimit(dir, prefix string, body io.Reader, limit int64) (string, [32]byte, int64, error) {
	if dir == "" {
		dir = os.TempDir()
	}
	f, err := os.CreateTemp(dir, prefix)
	if err != nil {
		return "", [32]byte{}, 0, err
	}
	_ = f.Chmod(0600)
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(body, limit+1))
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		os.Remove(f.Name())
		return "", [32]byte{}, n, err
	}
	if n > limit {
		os.Remove(f.Name())
		return "", [32]byte{}, n, errCap
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return f.Name(), sum, n, nil
}

func hashLimit(body io.Reader, limit int64) ([32]byte, error) {
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(body, limit+1))
	if err != nil {
		return [32]byte{}, err
	}
	if n > limit {
		return [32]byte{}, errCap
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

func oneName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, `\`) || strings.Contains(name, "..") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func machineName(agentID string) string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return "castbot-" + strings.ToLower(agentID) + "-r" + hex.EncodeToString(b[:])
}

// Recover rolls back a replace that stopped before the switch, and finishes
// one that stopped after it. A missing host leaves the rows for the next start.
func Recover(ctx context.Context, st *store.Store, h host.AgentHost) error {
	if st == nil || h == nil {
		return nil
	}
	rows, err := st.RunningReplacements(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Step == "switched" || row.Step == "delete" {
			if err := finishDelete(ctx, st, h, row); err != nil {
				return err
			}
			continue
		}
		if row.NewMicro != "" {
			_, _ = deleteMachine(ctx, h, row.NewMicro, row.AgentID, host.ReplaceTag(row.ID))
		} else {
			_ = deleteByTag(ctx, h, host.ReplaceTag(row.ID))
		}
		class := classForStep(row.Step)
		if err := st.FailReplacement(ctx, row.UserID, row.ID, row.Step, class); err != nil {
			return err
		}
		_ = st.AddVMEventFor(ctx, row.UserID, row.AgentID, row.OldVMID, store.EventReplaceFailed, 0)
		kind, id := auditActor(row)
		_ = st.WriteMachineAudit(ctx, row.UserID, kind, id, "machine.replace_failed", row.AgentID, ImageTag(row.OldImage), ImageTag(row.NewImage), row.Actor)
	}
	return RetryDeletes(ctx, st, h)
}

// RetryDeletes tries once more to remove old machines whose switch already stands.
func RetryDeletes(ctx context.Context, st *store.Store, h host.AgentHost) error {
	if st == nil || h == nil {
		return nil
	}
	rows, err := st.PendingDeletes(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		ok, gone := deleteMachine(ctx, h, row.OldMicro, row.AgentID, "")
		if ok || gone {
			_ = st.ClearDeletePending(ctx, row.UserID, row.ID)
		}
	}
	return nil
}

func finishDelete(ctx context.Context, st *store.Store, h host.AgentHost, row store.Replacement) error {
	ok, gone := deleteMachine(ctx, h, row.OldMicro, row.AgentID, "")
	pending := !ok && !gone
	if err := st.FinishReplacement(ctx, row.UserID, row.ID, pending); err != nil {
		return err
	}
	_ = st.AddVMEventFor(ctx, row.UserID, row.AgentID, row.NewVMID, store.EventReplaceSwitch, 0)
	kind, id := auditActor(row)
	_ = st.WriteMachineAudit(ctx, row.UserID, kind, id, "machine.replace_succeeded", row.AgentID, ImageTag(row.OldImage), ImageTag(row.NewImage), row.Actor)
	if pending {
		_ = st.AddVMEventFor(ctx, row.UserID, row.AgentID, row.OldVMID, store.EventReplaceDelete, 0)
	}
	return nil
}

// deleteMachine removes one machine after the id and the agent tag match.
// gone is true when the API no longer lists it. ok is true when this call deleted it.
func deleteMachine(ctx context.Context, h host.AgentHost, id, agentID, extraTag string) (ok, gone bool) {
	id = strings.TrimSpace(id)
	if h == nil || id == "" || id == "pending" {
		return false, false
	}
	m, err := h.Status(ctx, id)
	if err != nil {
		if errors.Is(err, host.ErrNotSupported) || isMissing(err) {
			return false, true
		}
		return false, false
	}
	if m.ID != id {
		return false, false
	}
	if !hasTag(m, "agent:"+agentID) {
		return false, false
	}
	if extraTag != "" && !hasTag(m, extraTag) {
		return false, false
	}
	if err := host.GuardDelete(m); err != nil {
		return false, false
	}
	if err := h.Delete(ctx, id); err != nil {
		return false, false
	}
	return true, false
}

func deleteByTag(ctx context.Context, h host.AgentHost, tag string) error {
	lister, ok := h.(interface {
		List(context.Context) ([]host.Machine, error)
	})
	if !ok {
		return nil
	}
	machines, err := lister.List(ctx)
	if err != nil {
		return err
	}
	for _, m := range machines {
		if !hasTag(m, tag) {
			continue
		}
		if host.GuardDelete(m) != nil {
			continue
		}
		_ = h.Delete(ctx, m.ID)
	}
	return nil
}

func hasTag(m host.Machine, tag string) bool {
	for _, item := range m.Tags {
		if item == tag {
			return true
		}
	}
	return false
}

func auditActor(row store.Replacement) (kind, id string) {
	if strings.HasPrefix(row.Actor, "web:") {
		return "user", row.UserID
	}
	return "cli", "cli"
}

func isMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "404")
}
