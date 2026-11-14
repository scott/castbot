package store

import (
	"context"
	"testing"

	"github.com/scott/castbot/internal/connect"
)

func TestConnectionPolicyIsScopedToTheUser(t *testing.T) {
	s := connectStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "policy-ada@example.com", "Ada")
	other := makeUser(t, s, "policy-bea@example.com", "Bea")

	on, err := s.ConnectionSwitchedOn(ctx, owner.ID, connect.Shell)
	if err != nil || !on {
		t.Fatal("shell default")
	}
	on, err = s.ConnectionSwitchedOn(ctx, owner.ID, connect.GitHub)
	if err != nil || on {
		t.Fatal("github default")
	}
	if err := s.SetConnectionSwitch(ctx, owner.ID, connect.GitHub, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveConnectionLevels(ctx, owner.ID, connect.GitHub, map[string]string{"create_issue": "deny", "get_me": "ask"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveConnectionLevels(ctx, owner.ID, connect.Shell, map[string]string{"bash": "deny"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveConnectionLevels(ctx, owner.ID, connect.GitHub, map[string]string{"not_a_tool": "ask"}); err == nil {
		t.Fatal("unknown tool")
	}
	if err := s.SetConnectionSwitch(ctx, other.ID, connect.GitHub, false); err != nil {
		t.Fatal(err)
	}
	on, err = s.ConnectionSwitchedOn(ctx, owner.ID, connect.GitHub)
	if err != nil || !on {
		t.Fatal("owner switch changed")
	}
	on, err = s.ConnectionSwitchedOn(ctx, other.ID, connect.GitHub)
	if err != nil || on {
		t.Fatal("other switch")
	}
	levels, err := s.ConnectionLevels(ctx, other.ID, connect.GitHub)
	if err != nil || len(levels) != 0 {
		t.Fatalf("other levels %#v %v", levels, err)
	}
	levels, err = s.ConnectionLevels(ctx, owner.ID, connect.GitHub)
	if err != nil || levels["create_issue"] != "deny" || levels["get_me"] != "ask" {
		t.Fatalf("%#v %v", levels, err)
	}
	shell, err := s.ConnectionLevels(ctx, owner.ID, connect.Shell)
	if err != nil || shell["bash"] != "deny" {
		t.Fatalf("%#v %v", shell, err)
	}
	if _, err := s.ConnectionLevels(ctx, other.ID, "nope"); err != ErrInvalid {
		t.Fatal(err)
	}
}
