package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
)

func TestTOTPSetupIsScoped(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 34`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("schema 34 %d %v", n, err)
	}
	now := time.Date(2026, 11, 22, 16, 0, 0, 0, time.UTC)
	s.SetNow(func() time.Time { return now })

	hash, err := auth.Hash("correct-horse-1", s.argon)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := s.CreateUser(ctx, NewUser{Email: "plain@example.com", DisplayName: "Plain", Workspace: "Personal", PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	on, err := s.HasTOTP(ctx, plain.ID)
	if err != nil || on {
		t.Fatalf("plain totp %v %v", on, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM credentials WHERE user_id = ? AND kind = 'totp'`, plain.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("plain credential %d %v", n, err)
	}

	a, err := s.CreateUser(ctx, NewUser{Email: "a-totp@example.com", DisplayName: "User A", Workspace: "Personal", PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateUser(ctx, NewUser{Email: "b-totp@example.com", DisplayName: "User B", Workspace: "Personal", PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	already := makeUser(t, s, "on-totp@example.com", "Already On")
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BeginTOTP(ctx, already.ID, secret); err != ErrConflict {
		t.Fatalf("begin on %v", err)
	}
	if _, err := s.PendingTOTP(ctx, already.ID); err != ErrNotFound {
		t.Fatalf("pending for enabled user %v", err)
	}

	if err := s.BeginTOTP(ctx, a.ID, secret); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PendingTOTP(ctx, b.ID); err != ErrNotFound {
		t.Fatalf("b saw a pending %v", err)
	}
	code, err := auth.TOTPCode(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmTOTP(ctx, b.ID, code); err != ErrNotFound {
		t.Fatalf("b confirmed a %v", err)
	}
	bad := "000000"
	if bad == code {
		bad = "111111"
	}
	if err := s.ConfirmTOTP(ctx, a.ID, bad); err != ErrInvalid {
		t.Fatalf("bad code %v", err)
	}
	pending, err := s.PendingTOTP(ctx, a.ID)
	if err != nil || pending != secret {
		t.Fatalf("pending after bad code %q %v", pending, err)
	}
	if err := s.ConfirmTOTP(ctx, a.ID, code); err != nil {
		t.Fatal(err)
	}
	on, err = s.HasTOTP(ctx, a.ID)
	if err != nil || !on {
		t.Fatalf("a after confirm %v %v", on, err)
	}
	on, err = s.HasTOTP(ctx, b.ID)
	if err != nil || on {
		t.Fatalf("b after a confirm %v %v", on, err)
	}
	if _, err := s.PendingTOTP(ctx, a.ID); err != ErrNotFound {
		t.Fatalf("pending left %v", err)
	}
	var last sql.NullInt64
	if err := s.db.QueryRow(`SELECT last_step FROM credentials WHERE user_id = ? AND workspace_id = ? AND kind = 'totp'`, a.ID, a.WorkspaceID).Scan(&last); err != nil || !last.Valid {
		t.Fatalf("last step %v %v", last, err)
	}
	if err := s.BeginTOTP(ctx, a.ID, secret); err != ErrConflict {
		t.Fatalf("begin again %v", err)
	}
	if err := s.ConfirmTOTP(ctx, a.ID, code); err != ErrConflict {
		t.Fatalf("confirm again %v", err)
	}

	if _, err := s.db.Exec(`INSERT INTO totp_pending(user_id, workspace_id, secret, created_at) VALUES (?, ?, 'LEAKEDSECRET', ?)`, b.ID, a.WorkspaceID, s.stamp(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PendingTOTP(ctx, b.ID); err != ErrNotFound {
		t.Fatalf("wrong workspace pending %v", err)
	}
	if err := s.ConfirmTOTP(ctx, b.ID, code); err != ErrNotFound {
		t.Fatalf("wrong workspace confirm %v", err)
	}
	on, err = s.HasTOTP(ctx, b.ID)
	if err != nil || on {
		t.Fatalf("b stayed off %v %v", on, err)
	}
}
