package replace

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

func TestReplacePostSwitchStepFailureKeepsLiveMachine(t *testing.T) {
	r := newRig(t, "running")
	ctx := context.Background()
	_, err := r.st.ProbeDB().Exec(`CREATE TRIGGER probe_fail BEFORE UPDATE OF step ON vm_replacements
WHEN NEW.step = 'delete' BEGIN SELECT RAISE(ABORT, 'database is locked'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, r.st, r.fake, r.opt(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	live, err := r.st.LiveVM(ctx, r.user.ID, r.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.fake.Machines[live.MicroVMID]; !ok {
		t.Fatalf("live machine %s deleted after the switch", live.MicroVMID)
	}
	if _, err := r.st.ProbeDB().Exec(`DROP TRIGGER probe_fail`); err != nil {
		t.Fatal(err)
	}
	if err := Recover(ctx, r.st, r.fake); err != nil {
		t.Fatal(err)
	}
	live, err = r.st.LiveVM(ctx, r.user.ID, r.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.fake.Machines[live.MicroVMID]; !ok {
		t.Fatalf("recovery deleted the live machine %s", live.MicroVMID)
	}
}

func TestReplaceRollbackDeleteFailureStaysRetryable(t *testing.T) {
	r := newRig(t, "running")
	ctx := context.Background()
	r.fake.FailClass = "copy"
	sticky := &stickyDelete{Fake: r.fake, fail: true}
	if _, err := Run(ctx, r.st, sticky, r.opt(t.TempDir())); err == nil {
		t.Fatal("copy succeeded")
	}
	if _, ok := r.fake.Machines["vm-test"]; !ok {
		t.Fatal("new machine already gone")
	}
	row, err := r.st.LatestReplacement(ctx, r.user.ID, r.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State == "failed" {
		t.Fatal("marked failed while the new machine may still exist")
	}
	var pending int
	if err := r.st.ProbeDB().QueryRow(`SELECT cleanup_pending FROM vm_replacements WHERE id = ?`, row.ID).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("cleanup_pending %d %v", pending, err)
	}
	sticky.fail = false
	if err := Recover(ctx, r.st, sticky); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.fake.Machines["vm-test"]; ok {
		t.Fatal("recovery left the new machine")
	}
	if _, ok := r.fake.Machines["vm-old"]; !ok {
		t.Fatal("old machine deleted")
	}
	row, err = r.st.LatestReplacement(ctx, r.user.ID, r.agent.ID)
	if err != nil || row.State != "failed" {
		t.Fatalf("row %+v %v", row, err)
	}
}

func TestReplaceRecoverRetriesAfterTransientStatus(t *testing.T) {
	r := newRig(t, "running")
	ctx := context.Background()
	opt := r.opt(t.TempDir())
	opt.HaltAt = "copy"
	if _, err := Run(ctx, r.st, r.fake, opt); !errors.Is(err, ErrHalted) {
		t.Fatal(err)
	}
	down := &downHost{Fake: r.fake, down: true}
	if err := Recover(ctx, r.st, down); err != nil {
		t.Fatal(err)
	}
	row, err := r.st.LatestReplacement(ctx, r.user.ID, r.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State == "failed" {
		t.Fatal("marked failed while status was unavailable")
	}
	if _, ok := r.fake.Machines["vm-test"]; !ok {
		t.Fatal("new machine dropped")
	}
	down.down = false
	if err := Recover(ctx, r.st, down); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.fake.Machines["vm-test"]; ok {
		t.Fatal("second recovery left the new machine")
	}
	if _, ok := r.fake.Machines["vm-old"]; !ok {
		t.Fatal("old machine deleted")
	}
}

func TestReplaceDeadlineDuringCopyStillRollsBack(t *testing.T) {
	r := newRig(t, "running")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := Run(ctx, r.st, slowHost{Fake: r.fake}, r.opt(t.TempDir()))
	var kept *Kept
	if !errors.As(err, &kept) {
		t.Fatal(err)
	}
	if _, ok := r.fake.Machines["vm-test"]; ok {
		t.Fatal("new machine remains")
	}
	if _, ok := r.fake.Machines["vm-old"]; !ok {
		t.Fatal("old machine deleted")
	}
	if _, err := r.st.ActiveReplacement(context.Background(), r.user.ID, r.agent.ID); err == nil {
		t.Fatal("agent still locked")
	}
}

func TestReplaceFailedReplaceRevokesNewToken(t *testing.T) {
	r := newRig(t, "running")
	ctx := context.Background()
	var newTok string
	r.fake.OnCreate = func(spec host.CreateSpec) {
		next, err := r.st.ExchangeBootstrap(context.Background(), spec.AgentID, spec.Bootstrap)
		if err != nil {
			t.Errorf("exchange %v", err)
			return
		}
		newTok = next
		if _, err := r.st.PlaceVMToken(context.Background(), spec.AgentID, next); err != nil {
			t.Errorf("place %v", err)
		}
	}
	r.fake.FailClass = "copy"
	if _, err := Run(ctx, r.st, r.fake, r.opt(t.TempDir())); err == nil {
		t.Fatal("copy succeeded")
	}
	if newTok == "" {
		t.Fatal("no new token")
	}
	if _, err := r.st.UserForAgentToken(ctx, r.agent.ID, newTok); err == nil {
		t.Fatal("new token still valid")
	}
	if _, err := r.st.UserForAgentToken(ctx, r.agent.ID, r.token); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceRepackedTarMatchesAfterCleanup(t *testing.T) {
	r := newRig(t, "running")
	if _, err := Run(context.Background(), r.st, repackHost{Fake: r.fake}, r.opt(t.TempDir())); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceLockPrecheckBlocksStreamingRun(t *testing.T) {
	r := newRig(t, "running")
	ctx := context.Background()
	threads, err := r.st.ListThreadsByAgent(ctx, r.user.ID, r.agent.ID)
	if err != nil || len(threads) == 0 {
		t.Fatal(err)
	}
	run, err := r.st.CreateRun(ctx, r.user.ID, threads[0].ID, r.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.ProbeDB().Exec(`UPDATE runs SET status = 'streaming' WHERE id = ?`, run.ID); err != nil {
		t.Fatal(err)
	}
	_, err = Run(ctx, r.st, r.fake, r.opt(t.TempDir()))
	var blocked *Blocked
	if !errors.As(err, &blocked) || !strings.Contains(blocked.Error(), "Finish or stop the reply in progress first.") {
		t.Fatal(err)
	}
	if _, ok := r.fake.Machines["vm-test"]; ok {
		t.Fatal("replace started")
	}
}

func TestReplaceVerifyComparesWorkspaceBytes(t *testing.T) {
	r := newRig(t, "running")
	_, err := Run(context.Background(), r.st, flipHost{Fake: r.fake, old: "https://old.example"}, r.opt(t.TempDir()))
	var kept *Kept
	if !errors.As(err, &kept) || kept.Line != Reason("verify") {
		t.Fatal(err)
	}
	live, err := r.st.LiveVM(context.Background(), r.user.ID, r.agent.ID)
	if err != nil || live.MicroVMID != "vm-old" {
		t.Fatalf("live %+v %v", live, err)
	}
}

func TestReplaceSwitchWritesEventInTheTransaction(t *testing.T) {
	r := newRig(t, "running")
	ctx := context.Background()
	opt := r.opt(t.TempDir())
	opt.HaltAt = "switch"
	if _, err := Run(ctx, r.st, r.fake, opt); !errors.Is(err, ErrHalted) {
		t.Fatal(err)
	}
	if n := countKind(t, r, store.EventReplaceSwitch); n != 1 {
		t.Fatalf("replace_switched %d", n)
	}
	if err := Recover(ctx, r.st, r.fake); err != nil {
		t.Fatal(err)
	}
	if n := countKind(t, r, store.EventReplaceSwitch); n != 1 {
		t.Fatalf("duplicate replace_switched %d", n)
	}
}

func TestReplaceStaleRecoveryLeavesALiveRun(t *testing.T) {
	r := newRig(t, "running")
	ctx := context.Background()
	opt := r.opt(t.TempDir())
	opt.HaltAt = "copy"
	if _, err := Run(ctx, r.st, r.fake, opt); !errors.Is(err, ErrHalted) {
		t.Fatal(err)
	}
	if err := RecoverStale(ctx, r.st, r.fake, StaleAfter); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.ActiveReplacement(ctx, r.user.ID, r.agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.fake.Machines["vm-test"]; !ok {
		t.Fatal("live replace was recovered")
	}
	old := time.Now().Add(-StaleAfter - time.Minute).UTC().Format(time.RFC3339)
	if _, err := r.st.ProbeDB().Exec(`UPDATE vm_replacements SET updated_at = ?`, old); err != nil {
		t.Fatal(err)
	}
	if err := RecoverStale(ctx, r.st, r.fake, StaleAfter); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.ActiveReplacement(ctx, r.user.ID, r.agent.ID); err == nil {
		t.Fatal("stale replace still running")
	}
	if _, ok := r.fake.Machines["vm-old"]; !ok {
		t.Fatal("old machine deleted")
	}
	if _, ok := r.fake.Machines["vm-test"]; ok {
		t.Fatal("new machine remains")
	}
}

func TestReplaceStaleRecoveryRetriesFreshCleanup(t *testing.T) {
	r := newRig(t, "running")
	ctx := context.Background()
	r.fake.FailClass = "copy"
	sticky := &stickyDelete{Fake: r.fake, fail: true}
	if _, err := Run(ctx, r.st, sticky, r.opt(t.TempDir())); err == nil {
		t.Fatal("copy succeeded")
	}
	if err := RecoverStale(ctx, r.st, sticky, StaleAfter); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.fake.Machines["vm-test"]; !ok {
		t.Fatal("cleanup dropped the new machine")
	}
	row, err := r.st.LatestReplacement(ctx, r.user.ID, r.agent.ID)
	if err != nil || row.State != "running" || !row.CleanupPending {
		t.Fatalf("row %+v %v", row, err)
	}
	sticky.fail = false
	if err := RecoverStale(ctx, r.st, sticky, StaleAfter); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.fake.Machines["vm-test"]; ok {
		t.Fatal("fresh cleanup was not retried")
	}
	if _, ok := r.fake.Machines["vm-old"]; !ok {
		t.Fatal("old machine deleted")
	}
}

func countKind(t *testing.T, r *rig, kind string) int {
	t.Helper()
	events, err := r.st.ListVMEvents(context.Background(), r.user.ID, r.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range events {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

type stickyDelete struct {
	*host.Fake
	fail bool
}

func (s *stickyDelete) Delete(ctx context.Context, id string) error {
	if s.fail {
		return errors.New("api 500")
	}
	return s.Fake.Delete(ctx, id)
}

type downHost struct {
	*host.Fake
	down bool
}

func (d *downHost) Status(ctx context.Context, id string) (host.Machine, error) {
	if d.down {
		return host.Machine{}, errors.New("api 503")
	}
	return d.Fake.Status(ctx, id)
}

func (d *downHost) List(ctx context.Context) ([]host.Machine, error) {
	if d.down {
		return nil, errors.New("api 503")
	}
	return d.Fake.List(ctx)
}

type slowHost struct {
	*host.Fake
}

func (s slowHost) ImportMemory(ctx context.Context, endpoint, token string, body io.Reader) error {
	select {
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.Fake.ImportMemory(ctx, endpoint, token, body)
}

func (s slowHost) Status(ctx context.Context, id string) (host.Machine, error) {
	if err := ctx.Err(); err != nil {
		return host.Machine{}, err
	}
	return s.Fake.Status(ctx, id)
}

func (s slowHost) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Fake.Delete(ctx, id)
}

type flipHost struct {
	*host.Fake
	old string
}

func (f flipHost) ReadWorkspace(ctx context.Context, endpoint, token, name string) (io.ReadCloser, error) {
	if endpoint != f.old {
		return io.NopCloser(strings.NewReader("tampered-bytes")), nil
	}
	return f.Fake.ReadWorkspace(ctx, endpoint, token, name)
}

type repackHost struct {
	*host.Fake
}

func (h repackHost) ExportMemory(ctx context.Context, endpoint, token string) (io.ReadCloser, error) {
	mod := time.Unix(0, 0)
	if endpoint != "https://old.example" {
		mod = time.Unix(12345, 0)
	}
	return io.NopCloser(bytes.NewReader(tarBytes(tName(), []byte("hello"), mod))), nil
}

func tName() string { return "memory.db" }

func tarBytes(name string, body []byte, mod time.Time) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), ModTime: mod, Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil
	}
	if _, err := tw.Write(body); err != nil {
		return nil
	}
	if err := tw.Close(); err != nil {
		return nil
	}
	return buf.Bytes()
}
