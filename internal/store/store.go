// Package store is the only layer that reads or writes SQLite.
// Every method that touches owned rows takes the caller's user id and adds it to the query.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/scott/castbot/internal/auth"
	_ "modernc.org/sqlite"
)

const (
	timeLayout   = time.RFC3339
	lockFailures = 5
	lockWindow   = 15 * time.Minute
)

var (
	ErrNotFound  = errors.New("not found")
	ErrConflict  = errors.New("conflict")
	ErrProtected = errors.New("protected")
	ErrInvalid   = errors.New("invalid")
)

// Store scopes every owned-row query by the caller's user id.
type Store struct {
	db    *sql.DB
	now   func() time.Time
	argon auth.Params
	dummy string
}

// Options tunes a store. Zero values use the production defaults.
type Options struct {
	Now   func() time.Time
	Argon auth.Params
}

// Open opens a SQLite database in WAL mode and runs migrations.
func Open(path string, opt Options) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		db.Close()
		return nil, err
	}
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		db.Close()
		return nil, err
	}
	if !strings.EqualFold(mode, "wal") {
		db.Close()
		return nil, fmt.Errorf("journal mode %q, want wal", mode)
	}
	argon := opt.Argon
	if argon.Memory == 0 {
		argon = auth.DefaultParams
	}
	dummy, err := auth.Hash("castbot-dummy-password", argon)
	if err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db, now: opt.Now, argon: argon, dummy: dummy}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Now returns the store clock.
func (s *Store) Now() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

// SetNow replaces the clock. Tests use it.
func (s *Store) SetNow(fn func() time.Time) { s.now = fn }

// Argon returns the password hashing parameters this store was opened with.
func (s *Store) Argon() auth.Params { return s.argon }

// JournalMode returns the SQLite journal mode.
func (s *Store) JournalMode(ctx context.Context) (string, error) {
	var mode string
	err := s.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode)
	return mode, err
}

func (s *Store) stamp(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	return time.Parse(timeLayout, v)
}

func newID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func scanErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
