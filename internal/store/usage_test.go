package store

import (
	"context"
	"testing"
	"time"
)

func TestVMUsageIntervals(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 24`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("version 24 count %d %v", n, err)
	}
	user := testUser(t, st)
	agent := testAgent(t, st, user.ID)
	start := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	vm := bootUsageVM(t, st, user, agent, "vm-usage", "https://usage.example", start)

	st.SetNow(func() time.Time { return start.Add(time.Hour) })
	if err := st.ApplyVMState(ctx, user.ID, agent.ID, "running", "", ""); err != nil {
		t.Fatal(err)
	}
	var started string
	var open int
	if err := st.db.QueryRow(`SELECT started_at, COUNT(*) FROM vm_usage WHERE user_id = ? AND ended_at = ''`, user.ID).Scan(&started, &open); err != nil {
		t.Fatal(err)
	}
	if open != 1 || started != start.UTC().Format(time.RFC3339) {
		t.Fatalf("open %d started %s", open, started)
	}

	if _, err := st.db.Exec(`UPDATE agents SET size = '2:4096' WHERE id = ? AND user_id = ?`, agent.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	st.SetNow(func() time.Time { return start.Add(2 * time.Hour) })
	if err := st.ApplyVMState(ctx, user.ID, agent.ID, "running", "", ""); err != nil {
		t.Fatal(err)
	}
	var sizes int
	if err := st.db.QueryRow(`SELECT COUNT(DISTINCT size) FROM vm_usage WHERE user_id = ? AND agent_vm_id = ?`, user.ID, vm.ID).Scan(&sizes); err != nil || sizes != 2 {
		t.Fatalf("sizes %d %v", sizes, err)
	}

	fresh, err := st.LiveVM(ctx, user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	st.SetNow(func() time.Time { return start.Add(3 * time.Hour) })
	ok, err := st.MarkPausedIfUnchanged(ctx, user.ID, agent.ID, fresh.MicroVMID, fresh.StateGen, "")
	if err != nil || !ok {
		t.Fatalf("pause %v %v", ok, err)
	}
	st.SetNow(func() time.Time { return start.Add(3*time.Hour + 24*time.Hour) })
	spans, err := st.UsageIn(ctx, user.ID, agent.ID, start, st.Now())
	if err != nil {
		t.Fatal(err)
	}
	var compute, disk int64
	for _, span := range spans {
		switch span.Kind {
		case "compute":
			compute += span.Seconds
		case "disk":
			disk += span.Seconds
		}
	}
	if compute != 3*3600 || disk != 24*3600 {
		t.Fatalf("compute %d disk %d", compute, disk)
	}

	other := testUser(t, st)
	if _, err := st.UsageIn(ctx, other.ID, agent.ID, start, st.Now()); err != ErrNotFound {
		t.Fatalf("other agent %v", err)
	}
	if got, err := st.UsageIn(ctx, other.ID, "", start, st.Now()); err != nil || len(got) != 0 {
		t.Fatalf("other account %+v %v", got, err)
	}
	if _, err := st.db.Exec(`INSERT INTO vm_usage(workspace_id, user_id, agent_id, agent_vm_id, size, kind, started_at, ended_at)
VALUES (?, ?, 'other-agent', 'other-vm', '1:2048', 'compute', ?, '')`,
		other.WorkspaceID, other.ID, start.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if got, err := st.UsageIn(ctx, user.ID, "", start, st.Now()); err != nil {
		t.Fatal(err)
	} else {
		for _, span := range got {
			if span.AgentID == "other-agent" {
				t.Fatal("other user's interval was visible")
			}
		}
	}
}

func TestVMUsageMonthBoundaryAndClose(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	user := testUser(t, st)
	agent := testAgent(t, st, user.ID)
	start := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	vm := bootUsageVM(t, st, user, agent, "vm-month", "https://month.example", start)
	st.SetNow(func() time.Time { return time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC) })
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	spans, err := st.UsageIn(ctx, user.ID, agent.ID, from, to)
	if err != nil || len(spans) != 1 || spans[0].Seconds != 12*3600 || spans[0].Kind != "compute" {
		t.Fatalf("%v %+v", err, spans)
	}

	st.SetNow(func() time.Time { return start.Add(time.Hour) })
	if err := st.MarkVMDeleted(ctx, user.ID, agent.ID); err != nil {
		t.Fatal(err)
	}
	var open int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM vm_usage WHERE user_id = ? AND agent_vm_id = ? AND ended_at = ''`, user.ID, vm.ID).Scan(&open); err != nil || open != 0 {
		t.Fatalf("open after delete %d %v", open, err)
	}
	st.SetNow(func() time.Time { return start.Add(3 * time.Hour) })
	spans, err = st.UsageIn(ctx, user.ID, agent.ID, start, st.Now())
	if err != nil || len(spans) != 1 || spans[0].Seconds != 3600 {
		t.Fatalf("after delete %v %+v", err, spans)
	}

	second := namedUsageAgent(t, st, user.ID, "Berry")
	bootUsageVM(t, st, user, second, "vm-del-agent", "https://del.example", start.Add(4*time.Hour))
	st.SetNow(func() time.Time { return start.Add(5 * time.Hour) })
	if err := st.DeleteAgent(ctx, user.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM vm_usage WHERE user_id = ? AND agent_id = ? AND ended_at = ''`, user.ID, second.ID).Scan(&open); err != nil || open != 0 {
		t.Fatalf("open after agent delete %d %v", open, err)
	}
	if _, err := st.UsageIn(ctx, user.ID, second.ID, start, st.Now()); err != ErrNotFound {
		t.Fatalf("deleted agent %v", err)
	}
}

func TestVMUsageReplaceSplitsAndRelease(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	user := testUser(t, st)
	agent := testAgent(t, st, user.ID)
	start := time.Date(2026, 11, 12, 15, 0, 0, 0, time.UTC)
	old := bootUsageVM(t, st, user, agent, "vm-old", "https://old.example", start)
	row, _, err := st.BeginReplacement(ctx, user.ID, agent.ID, "web:owner@example.com", "example.invalid/castbot-runtime:next", false)
	if err != nil {
		t.Fatal(err)
	}
	nextBoot, err := st.IssueBootstrapFor(ctx, user.ID, agent.ID, row.NewVMID)
	if err != nil {
		t.Fatal(err)
	}
	next, err := st.ExchangeBootstrap(ctx, agent.ID, nextBoot)
	if err != nil {
		t.Fatal(err)
	}
	if wrote, err := st.PlaceVMToken(ctx, agent.ID, next); err != nil || wrote {
		t.Fatalf("stage %v %v", wrote, err)
	}
	switched := start.Add(2 * time.Hour)
	st.SetNow(func() time.Time { return switched })
	if err := st.SwitchReplacement(ctx, user.ID, row.ID, next, ""); err != nil {
		t.Fatal(err)
	}
	st.SetNow(func() time.Time { return switched.Add(time.Hour) })
	spans, err := st.UsageIn(ctx, user.ID, agent.ID, start, st.Now())
	if err != nil {
		t.Fatal(err)
	}
	var seconds int64
	for _, span := range spans {
		if span.Kind != "compute" {
			t.Fatalf("kind %s", span.Kind)
		}
		seconds += span.Seconds
	}
	if seconds != 3*3600 {
		t.Fatalf("seconds %d", seconds)
	}
	var oldEnded, newEnded string
	var newVM string
	if err := st.db.QueryRow(`SELECT ended_at FROM vm_usage WHERE user_id = ? AND agent_vm_id = ? AND started_at = ?`, user.ID, old.ID, start.UTC().Format(time.RFC3339)).Scan(&oldEnded); err != nil || oldEnded == "" {
		t.Fatalf("old interval %q %v", oldEnded, err)
	}
	if err := st.db.QueryRow(`SELECT agent_vm_id, ended_at FROM vm_usage WHERE user_id = ? AND ended_at = ''`, user.ID).Scan(&newVM, &newEnded); err != nil {
		t.Fatal(err)
	}
	if newVM != row.NewVMID || newVM == old.ID || newEnded != "" {
		t.Fatalf("new interval %s ended %q old %s", newVM, newEnded, old.ID)
	}

	third := namedUsageAgent(t, st, user.ID, "Cleo")
	live := bootUsageVM(t, st, user, third, "vm-release", "https://release.example", start)
	st.SetNow(func() time.Time { return start.Add(30 * time.Minute) })
	if err := st.ReleaseVM(ctx, user.ID, third.ID); err != nil {
		t.Fatal(err)
	}
	var open int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM vm_usage WHERE agent_vm_id = ? AND ended_at = ''`, live.ID).Scan(&open); err != nil || open != 0 {
		t.Fatalf("release left %d open %v", open, err)
	}
	spans, err = st.UsageIn(ctx, user.ID, third.ID, start, start.Add(2*time.Hour))
	if err != nil || len(spans) != 1 || spans[0].Seconds != 30*60 {
		t.Fatalf("release span %v %+v", err, spans)
	}
}

func TestEnsureUsageBackfillsFromNow(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	user := testUser(t, st)
	agent := testAgent(t, st, user.ID)
	start := time.Date(2026, 11, 8, 8, 0, 0, 0, time.UTC)
	vm := bootUsageVM(t, st, user, agent, "vm-backfill", "https://backfill.example", start)
	if _, err := st.db.Exec(`DELETE FROM vm_usage WHERE user_id = ? AND agent_vm_id = ?`, user.ID, vm.ID); err != nil {
		t.Fatal(err)
	}
	later := start.Add(5 * time.Hour)
	st.SetNow(func() time.Time { return later })
	if err := st.EnsureUsage(ctx, user.ID, agent.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureUsage(ctx, user.ID, agent.ID); err != nil {
		t.Fatal(err)
	}
	var started string
	var n int
	if err := st.db.QueryRow(`SELECT started_at, COUNT(*) FROM vm_usage WHERE user_id = ? AND agent_vm_id = ? AND ended_at = ''`, user.ID, vm.ID).Scan(&started, &n); err != nil {
		t.Fatal(err)
	}
	if n != 1 || started != later.UTC().Format(time.RFC3339) {
		t.Fatalf("backfill %d %s", n, started)
	}
	st.SetNow(func() time.Time { return later.Add(time.Hour) })
	spans, err := st.UsageIn(ctx, user.ID, agent.ID, start, st.Now())
	if err != nil || len(spans) != 1 || spans[0].Seconds != 3600 {
		t.Fatalf("backfill span %v %+v", err, spans)
	}
}

func namedUsageAgent(t *testing.T, st *Store, userID, name string) Agent {
	t.Helper()
	sec, err := st.SystemSection(context.Background(), userID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := st.CreateAgent(context.Background(), userID, AgentInput{SectionID: sec.ID, Name: name, Title: "Helper"})
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func bootUsageVM(t *testing.T, st *Store, user User, agent Agent, micro, endpoint string, when time.Time) VM {
	t.Helper()
	st.SetNow(func() time.Time { return when })
	if _, err := st.BeginVM(context.Background(), user.ID, agent.ID, "castbot-"+micro, "example.invalid/runtime"); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishVM(context.Background(), user.ID, agent.ID, micro, endpoint, "running", ""); err != nil {
		t.Fatal(err)
	}
	vm, err := st.LiveVM(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	return vm
}
