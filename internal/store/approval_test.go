package store

import "testing"

func TestApprovalsMigration(t *testing.T) {
	st := testStore(t)
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 13`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("version 13 count %d", n)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('approvals', 'audit_log')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("tables %d", n)
	}
}
