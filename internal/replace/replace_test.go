package replace

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

const (
	oldImage = "example.invalid/castbot-runtime:7b5e414"
	newImage = "example.invalid/castbot-runtime:ff6d7ea"
)

type rig struct {
	st    *store.Store
	fake  *host.Fake
	user  store.User
	agent store.Agent
	token string
	mem   []byte
	files map[string][]byte
}

func newRig(t *testing.T, state string) *rig {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), store.Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	user := makeUser(t, st, "ada@example.com", "Ada")
	sec, err := st.CreateSection(ctx, user.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := st.CreateAgent(ctx, user.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Docs helper", Status: "ready", Model: "model-large"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginVM(ctx, user.ID, agent.ID, "castbot-old", oldImage); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishVM(ctx, user.ID, agent.ID, "vm-old", "https://old.example", state, ""); err != nil {
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
	mem := []byte("memory-v1")
	files := map[string][]byte{"notes.txt": []byte("hello atlas"), "plan.txt": []byte("keep")}
	fake := host.NewFake()
	fake.Machines["vm-old"] = host.Machine{
		ID: "vm-old", Name: "castbot-old", State: state, Endpoint: "https://old.example", Image: oldImage,
		Tags: []string{"castbot", "agent:" + agent.ID},
	}
	fake.SeedDisk("https://old.example", mem, files)
	fake.OnCreate = func(spec host.CreateSpec) {
		next, err := st.ExchangeBootstrap(context.Background(), spec.AgentID, spec.Bootstrap)
		if err != nil {
			t.Errorf("exchange %v", err)
			return
		}
		if _, err := st.PlaceVMToken(context.Background(), spec.AgentID, next); err != nil {
			t.Errorf("place %v", err)
		}
	}
	return &rig{st: st, fake: fake, user: user, agent: agent, token: token, mem: mem, files: files}
}

func (r *rig) opt(dir string) Options {
	return Options{
		UserID: r.user.ID, AgentID: r.agent.ID,
		ActorKind: "user", ActorID: r.user.ID, Actor: "web:ada@example.com",
		Image: newImage, PublicURL: "https://castbot.example",
		DataDir: dir, BootWait: time.Second,
	}
}

func makeUser(t *testing.T, s *store.Store, email, name string) store.User {
	t.Helper()
	hash, err := auth.Hash("right-password-1", s.Argon())
	if err != nil {
		t.Fatal(err)
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser(context.Background(), store.NewUser{Email: email, DisplayName: name, Workspace: "Personal", PasswordHash: hash, TOTPSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func countsMatch(before, after int) bool {
	return after == before
}

func TestReplaceCopiesMemoryAndDeletesOldAfterHealth(t *testing.T) {
	r := newRig(t, "running")
	ctx := context.Background()
	if _, err := r.st.IngestJournal(ctx, r.user.ID, r.agent.ID, []store.JournalEntry{{
		Seq: 1, SessionID: "kept", Caller: "control_plane", Kind: "request", Text: "keep this turn",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.st.IssueCLIToken(ctx, r.user.ID, r.agent.ID); err != nil {
		t.Fatal(err)
	}
	thread, err := r.st.CreateThread(ctx, r.user.ID, "dm", r.agent.ID, "Kept")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.CreateMessage(ctx, r.user.ID, thread.ID, "user", r.user.ID, "keep this"); err != nil {
		t.Fatal(err)
	}
	before, err := r.st.CountAgentRows(ctx, r.user.ID, r.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	res, err := Run(ctx, r.st, r.fake, r.opt(dir))
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 2 || res.DeletePending || res.NoCopy {
		t.Fatalf("%+v", res)
	}
	live, err := r.st.LiveVM(ctx, r.user.ID, r.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if live.MicroVMID == "vm-old" || live.Image != newImage {
		t.Fatalf("live %+v", live)
	}
	var n int
	if err := countLive(r, &n); err != nil || n != 1 {
		t.Fatalf("live rows %d %v", n, err)
	}
	if len(r.fake.Deletes) != 1 || r.fake.Deletes[0] != "vm-old" {
		t.Fatalf("deletes %#v", r.fake.Deletes)
	}
	if !healthBeforeDelete(r.fake.Calls) {
		t.Fatalf("calls %v", r.fake.Calls)
	}
	disk := r.fake.Disks[live.Endpoint]
	if disk == nil || !disk.Reloaded || string(disk.Memory) != string(r.mem) {
		t.Fatal("memory was not copied")
	}
	for name, body := range r.files {
		if string(disk.Files[name]) != string(body) {
			t.Fatalf("file %s", name)
		}
		sum := sha256.Sum256(body)
		if sha256.Sum256(disk.Files[name]) != sum {
			t.Fatal(name)
		}
	}
	after, err := r.st.CountAgentRows(ctx, r.user.ID, r.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if countsMatch(1, 2) {
		t.Fatal("a higher count was accepted")
	}
	if before["messages"] < 1 || before["journal_entries"] < 1 || before["cli_tokens"] < 1 {
		t.Fatalf("fixture counts %#v", before)
	}
	for k, v := range before {
		if k == "audit_log" {
			if after[k] <= v {
				t.Fatalf("audit_log %d -> %d", v, after[k])
			}
			continue
		}
		if !countsMatch(v, after[k]) {
			t.Fatalf("%s %d -> %d", k, v, after[k])
		}
	}
	agent, err := r.st.GetAgent(ctx, r.user.ID, r.agent.ID)
	if err != nil || agent.Name != "Atlas" || agent.Model != "model-large" || agent.SectionID != r.agent.SectionID || agent.Status != "ready" {
		t.Fatalf("agent %+v %v", agent, err)
	}
	if _, err := r.st.UserForAgentToken(ctx, r.agent.ID, r.token); err != store.ErrNotFound {
		t.Fatalf("old token %v", err)
	}
	actions, err := r.st.AuditActions(ctx, r.user.ID, r.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(actions, "machine.replace_requested") || !contains(actions, "machine.replace_succeeded") {
		t.Fatalf("audit %v", actions)
	}
	assertNoSecrets(t, r, r.token)
}

func TestReplaceWakesPausedMachineOnce(t *testing.T) {
	r := newRig(t, "paused")
	if _, err := Run(context.Background(), r.st, r.fake, r.opt(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	wakes := 0
	for _, call := range r.fake.Calls {
		if call == "wake" {
			wakes++
		}
	}
	if wakes != 1 {
		t.Fatalf("wakes %d", wakes)
	}
	pauses := 0
	for _, call := range r.fake.Calls {
		if call == "pause" {
			pauses++
		}
	}
	if pauses != 1 {
		t.Fatalf("pauses %d", pauses)
	}
	pushAt, pauseAt := -1, -1
	for i, call := range r.fake.Calls {
		if call == "push-cli" && pushAt < 0 {
			pushAt = i
		}
		if call == "pause" && pauseAt < 0 {
			pauseAt = i
		}
	}
	if pushAt < 0 || pauseAt < 0 || pushAt >= pauseAt {
		t.Fatalf("push-cli at %d pause at %d calls %v", pushAt, pauseAt, r.fake.Calls)
	}
	if len(r.fake.Deletes) != 1 || r.fake.Deletes[0] != "vm-old" {
		t.Fatalf("deletes %#v", r.fake.Deletes)
	}
	got, err := r.st.GetAgent(context.Background(), r.user.ID, r.agent.ID)
	if err != nil || got.Status != "paused" {
		t.Fatalf("status %s %v", got.Status, err)
	}
	live, err := r.st.LiveVM(context.Background(), r.user.ID, r.agent.ID)
	if err != nil || live.State != "paused" {
		t.Fatalf("live %+v %v", live, err)
	}
	m := r.fake.Machines[live.MicroVMID]
	if m.State != "paused" {
		t.Fatalf("machine state %s", m.State)
	}
}

func TestReplaceFailureKeepsOldMachine(t *testing.T) {
	for _, class := range []string{"create", "boot", "read_old", "copy", "verify"} {
		t.Run(class, func(t *testing.T) {
			r := newRig(t, "running")
			ctx := context.Background()
			agent, _ := r.st.GetAgent(ctx, r.user.ID, r.agent.ID)
			r.fake.FailClass = class
			opt := r.opt(t.TempDir())
			_, err := Run(ctx, r.st, r.fake, opt)
			var kept *Kept
			if !errors.As(err, &kept) {
				t.Fatal(err)
			}
			if kept.Line != Reason(class) {
				t.Fatalf("line %q", kept.Line)
			}
			live, err := r.st.LiveVM(ctx, r.user.ID, r.agent.ID)
			if err != nil || live.MicroVMID != "vm-old" || live.Image != oldImage {
				t.Fatalf("live %+v %v", live, err)
			}
			for _, id := range r.fake.Deletes {
				if id == "vm-old" {
					t.Fatal("old deleted")
				}
			}
			if class != "create" && len(r.fake.Deletes) != 1 {
				t.Fatalf("new machine deletes %#v", r.fake.Deletes)
			}
			if _, ok := r.fake.Machines["vm-old"]; !ok {
				t.Fatal("old machine missing")
			}
			got, err := r.st.GetAgent(ctx, r.user.ID, r.agent.ID)
			if err != nil || got.Status != agent.Status {
				t.Fatalf("status %s -> %s", agent.Status, got.Status)
			}
			row, err := r.st.LatestReplacement(ctx, r.user.ID, r.agent.ID)
			if err != nil || row.State != "failed" || row.Class != class {
				t.Fatalf("row %+v %v", row, err)
			}
			events, err := r.st.ListVMEvents(ctx, r.user.ID, r.agent.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !hasKind(events, store.EventReplaceFailed) {
				t.Fatal("missing replace_failed")
			}
		})
	}
}

func TestReplaceCrashRecovery(t *testing.T) {
	steps := []string{"lock", "create", "boot", "read_old", "copy", "verify", "switch", "delete"}
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			r := newRig(t, "running")
			ctx := context.Background()
			opt := r.opt(t.TempDir())
			opt.HaltAt = step
			_, err := Run(ctx, r.st, r.fake, opt)
			if !errors.Is(err, ErrHalted) {
				t.Fatal(err)
			}
			if err := Recover(ctx, r.st, r.fake); err != nil {
				t.Fatal(err)
			}
			live, err := r.st.LiveVM(ctx, r.user.ID, r.agent.ID)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for id := range r.fake.Machines {
				ids = append(ids, id)
			}
			if len(ids) != 1 {
				t.Fatalf("machines %v", ids)
			}
			afterSwitch := step == "switch" || step == "delete"
			if afterSwitch {
				if live.MicroVMID == "vm-old" || ids[0] == "vm-old" {
					t.Fatalf("live %s machines %v", live.MicroVMID, ids)
				}
			} else if live.MicroVMID != "vm-old" || ids[0] != "vm-old" {
				t.Fatalf("live %s machines %v", live.MicroVMID, ids)
			}
			var n int
			if err := countLive(r, &n); err != nil || n != 1 {
				t.Fatalf("rows %d %v", n, err)
			}
		})
	}
}

func TestReplaceNoCopySkipsFiles(t *testing.T) {
	r := newRig(t, "running")
	opt := r.opt(t.TempDir())
	opt.NoCopy = true
	res, err := Run(context.Background(), r.st, r.fake, opt)
	if err != nil {
		t.Fatal(err)
	}
	if !res.NoCopy || res.Files != 0 {
		t.Fatalf("%+v", res)
	}
	for _, call := range r.fake.Calls {
		if call == "export-memory" || call == "read-file" || call == "import-memory" {
			t.Fatalf("copied via %s", call)
		}
	}
	events, err := r.st.ListVMEvents(context.Background(), r.user.ID, r.agent.ID)
	if err != nil || !hasKind(events, store.EventReplaceNoCopy) {
		t.Fatal("missing no-copy event")
	}
}

func TestReplaceCaps(t *testing.T) {
	r := newRig(t, "running")
	files := make([]host.WorkspaceFile, MaxFiles+1)
	for i := range files {
		files[i] = host.WorkspaceFile{Name: "f" + strings.Repeat("a", 0) + itoa(i) + ".txt", Size: 1}
	}
	_, err := Run(context.Background(), r.st, capHost{Fake: r.fake, files: files}, r.opt(t.TempDir()))
	var kept *Kept
	if !errors.As(err, &kept) || kept.Line != Reason("copy") {
		t.Fatal(err)
	}
	live, err := r.st.LiveVM(context.Background(), r.user.ID, r.agent.ID)
	if err != nil || live.MicroVMID != "vm-old" {
		t.Fatal(err)
	}
}

func TestReplaceDryRunMakesNoHostCall(t *testing.T) {
	r := newRig(t, "running")
	before := len(r.fake.Calls)
	plan, err := DryRun(context.Background(), r.st, r.user.ID, r.agent.ID, newImage)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Block != "" || plan.AgentName != "Atlas" || plan.State != "running" || plan.CurrentImage != oldImage || plan.TargetImage != newImage {
		t.Fatalf("%+v", plan)
	}
	if len(r.fake.Calls) != before {
		t.Fatal("dry-run called the host")
	}
}

func TestReplaceBlockedWhileRunning(t *testing.T) {
	r := newRig(t, "running")
	started := make(chan struct{}, 1)
	hold := make(chan struct{})
	r.fake.CreateStarted = started
	r.fake.WaitCreate = hold
	errc := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), r.st, r.fake, r.opt(t.TempDir()))
		errc <- err
	}()
	<-started
	_, err := Run(context.Background(), r.st, r.fake, r.opt(t.TempDir()))
	var blocked *Blocked
	if !errors.As(err, &blocked) || blocked.Line != "A replacement is already running." {
		t.Fatal(err)
	}
	close(hold)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

type capHost struct {
	*host.Fake
	files []host.WorkspaceFile
}

func (c capHost) ListWorkspace(context.Context, string, string) ([]host.WorkspaceFile, error) {
	return c.files, nil
}

func countLive(r *rig, n *int) error {
	rows, err := r.st.LiveMachines(context.Background())
	if err != nil {
		return err
	}
	*n = 0
	for _, vm := range rows {
		if vm.AgentID == r.agent.ID {
			*n++
		}
	}
	return nil
}

func healthBeforeDelete(calls []string) bool {
	health, del := -1, -1
	for i, call := range calls {
		if call == "health" {
			health = i
		}
		if call == "delete" && del < 0 {
			del = i
		}
	}
	return health >= 0 && del > health
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func hasKind(events []store.VMEvent, kind string) bool {
	for _, ev := range events {
		if ev.Kind == kind {
			return true
		}
	}
	return false
}

func assertNoSecrets(t *testing.T, r *rig, token string) {
	t.Helper()
	row, err := r.st.LatestReplacement(context.Background(), r.user.ID, r.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	blob := row.NewEndpoint + row.NewTokenHash + row.Actor + row.Class
	if strings.Contains(blob, token) || strings.Contains(blob, "memory-v1") || strings.Contains(blob, "hello atlas") {
		t.Fatal("secret in replacement")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
