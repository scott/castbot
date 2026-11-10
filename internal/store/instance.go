package store

import (
	"context"
	"strings"
)

// ensureInstance stores one random id for this database. A second open
// keeps the id that is already there.
func (s *Store) ensureInstance() error {
	id, err := newID()
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO instance(singleton, id) VALUES (1, ?) ON CONFLICT(singleton) DO NOTHING`, id)
	return err
}

// InstanceID returns the id created for this database. It is not a user row.
// Callers use it only to tag machines this process creates.
func (s *Store) InstanceID(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM instance WHERE singleton = 1`).Scan(&id)
	if err != nil {
		return "", scanErr(err)
	}
	if !instanceIDOK(id) {
		return "", ErrInvalid
	}
	return id, nil
}

func instanceIDOK(id string) bool {
	if len(id) < 16 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if strings.ContainsRune("0123456789abcdef", c) {
			continue
		}
		return false
	}
	return true
}
