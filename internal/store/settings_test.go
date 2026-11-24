package store

import (
	"context"
	"testing"
)

func TestInstanceSettings(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 38`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("schema 38 %d %v", n, err)
	}
	rows, err := s.db.Query(`PRAGMA table_info(instance_settings)`)
	if err != nil {
		t.Fatal(err)
	}
	cols := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if cols["user_id"] || cols["workspace_id"] || !cols["key"] || !cols["value"] {
		t.Fatalf("columns %#v", cols)
	}
	got, err := s.InstanceSetting(ctx, "update_mode")
	if err != nil || got != "" {
		t.Fatalf("missing %q %v", got, err)
	}
	if err := s.SetInstanceSetting(ctx, "update_mode", "auto"); err != nil {
		t.Fatal(err)
	}
	got, err = s.InstanceSetting(ctx, "update_mode")
	if err != nil || got != "auto" {
		t.Fatalf("stored %q %v", got, err)
	}
	if err := s.SetInstanceSetting(ctx, "update_mode", "prompt"); err != nil {
		t.Fatal(err)
	}
	got, err = s.InstanceSetting(ctx, "update_mode")
	if err != nil || got != "prompt" {
		t.Fatalf("replaced %q %v", got, err)
	}
	if err := s.SetInstanceSetting(ctx, "Update", "prompt"); err != ErrInvalid {
		t.Fatalf("key %v", err)
	}
	if err := s.SetInstanceSetting(ctx, "update_mode", ""); err != ErrInvalid {
		t.Fatalf("empty %v", err)
	}
}
