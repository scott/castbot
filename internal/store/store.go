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
	"sync"
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
	// ErrQuota is the per-user machine cap. It is not a billing product.
	ErrQuota = errors.New("agent limit reached")
	// ErrCLICap is the active Cast CLI token cap for one agent.
	ErrCLICap = errors.New("cli token cap")
	// ErrCLIRate is the short window on minting Cast CLI tokens.
	ErrCLIRate = errors.New("cli token rate")
)

const (
	cliActiveCap  = 10
	cliMintMax    = 5
	cliMintWindow = time.Minute
)

// Store scopes every owned-row query by the caller's user id.
type Store struct {
	db                *sql.DB
	clockMu           sync.RWMutex
	now               func() time.Time
	stepFail          string
	cleanupWriteFault bool
	argon             auth.Params
	dummy             string

	// restartWanted is the first moment a bundle or secret reload had to wait,
	// keyed by user and agent. It is process memory, so a restart of this
	// process starts the wait again. Cleared once the reload is no longer wanted.
	restartMu     sync.Mutex
	restartWanted map[string]time.Time
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
	s.clockMu.RLock()
	fn := s.now
	s.clockMu.RUnlock()
	if fn != nil {
		return fn().UTC()
	}
	return time.Now().UTC()
}

// SetNow replaces the clock. Tests use it.
// The expiry loop reads the clock from another goroutine.
func (s *Store) SetNow(fn func() time.Time) {
	s.clockMu.Lock()
	s.now = fn
	s.clockMu.Unlock()
}

// FailStepUpdate makes SetReplacementStep fail for one step name. Tests use it.
func (s *Store) FailStepUpdate(step string) {
	s.clockMu.Lock()
	s.stepFail = strings.TrimSpace(step)
	s.clockMu.Unlock()
}

func (s *Store) stepUpdateBlocked(step string) bool {
	s.clockMu.RLock()
	defer s.clockMu.RUnlock()
	return s.stepFail != "" && s.stepFail == step
}

// FailCleanupWrite makes RecordCleanupStopped fail before its commit. Tests use it.
func (s *Store) FailCleanupWrite(on bool) {
	s.clockMu.Lock()
	s.cleanupWriteFault = on
	s.clockMu.Unlock()
}

func (s *Store) cleanupWriteBlocked() bool {
	s.clockMu.RLock()
	defer s.clockMu.RUnlock()
	return s.cleanupWriteFault
}

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
