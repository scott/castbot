package store

import (
	"context"
	"database/sql"
	"strings"
)

// SnapshotDB writes a consistent copy of the open database to dest.
// The live file is left as it is. VACUUM INTO does not take a bound parameter.
func (s *Store) SnapshotDB(ctx context.Context, dest string) error {
	if dest == "" || strings.ContainsAny(dest, "\x00\r\n'") {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `VACUUM INTO '`+dest+`'`)
	return err
}

// InspectDB opens a snapshot read-only and returns its schema version.
// It does not migrate the file. integrity_check must report ok.
func InspectDB(ctx context.Context, path string) (int, error) {
	if path == "" || strings.ContainsAny(path, "\x00\r\n") {
		return 0, ErrInvalid
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var check string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&check); err != nil {
		return 0, err
	}
	if check != "ok" {
		return 0, ErrInvalid
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}
