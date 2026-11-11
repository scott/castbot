package store

import "database/sql"

// ProbeDB is the store connection for tests that install a trigger or age a row.
func (s *Store) ProbeDB() *sql.DB {
	return s.db
}
