package store

import (
	"database/sql"
	"strings"
)

const schema = `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS workspaces (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  email TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL,
  avatar_path TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL CHECK (role IN ('owner', 'member')),
  theme TEXT NOT NULL DEFAULT 'system' CHECK (theme IN ('system', 'dark', 'light')),
  timezone TEXT NOT NULL DEFAULT 'America/Denver',
  password_changed_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  disabled_at TEXT
);

CREATE TABLE IF NOT EXISTS credentials (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('passkey', 'password', 'totp')),
  data TEXT NOT NULL,
  last_step INTEGER,
  created_at TEXT NOT NULL,
  UNIQUE (user_id, kind)
);

CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  csrf TEXT NOT NULL,
  user_agent TEXT NOT NULL DEFAULT '',
  remember INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS login_challenges (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  email TEXT NOT NULL,
  remember INTEGER NOT NULL DEFAULT 0,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS trusted_devices (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  secret_hash TEXT NOT NULL,
  user_agent TEXT NOT NULL DEFAULT '',
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);

-- login_limits counts sign-in failures. Unknown emails have an empty user_id.
CREATE TABLE IF NOT EXISTS login_limits (
  scope TEXT NOT NULL,
  subject TEXT NOT NULL,
  workspace_id TEXT NOT NULL DEFAULT '',
  user_id TEXT NOT NULL DEFAULT '',
  failures INTEGER NOT NULL,
  window_start TEXT NOT NULL,
  locked_until TEXT,
  PRIMARY KEY (scope, subject)
);

CREATE TABLE IF NOT EXISTS sections (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('pinned', 'agents', 'custom')),
  position INTEGER NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  section_id TEXT NOT NULL,
  name TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  avatar_color TEXT NOT NULL DEFAULT 'c0',
  avatar_path TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  bundle_version TEXT NOT NULL DEFAULT '',
  pending_bundle TEXT NOT NULL DEFAULT '',
  kept_yaml TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK (status IN ('draft', 'provisioning', 'ready', 'paused', 'failed', 'deleted')),
  activity TEXT NOT NULL DEFAULT '',
  unread INTEGER NOT NULL DEFAULT 0,
  approvals INTEGER NOT NULL DEFAULT 0,
  idle_timeout INTEGER NOT NULL DEFAULT 300,
  size TEXT NOT NULL DEFAULT '1:2048',
  position INTEGER NOT NULL DEFAULT 0,
  cli_slug TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  deleted_at TEXT
);

CREATE TABLE IF NOT EXISTS threads (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('dm', 'group', 'routine', 'cli')),
  agent_id TEXT,
  title TEXT NOT NULL DEFAULT '',
  cast_session_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS messages (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  thread_id TEXT NOT NULL,
  author_kind TEXT NOT NULL CHECK (author_kind IN ('user', 'agent', 'system')),
  author_id TEXT NOT NULL,
  body_md TEXT NOT NULL,
  run_id TEXT,
  origin TEXT NOT NULL DEFAULT 'web',
  journal_seq INTEGER,
  agent_vm_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sections_user ON sections(user_id, position);
CREATE INDEX IF NOT EXISTS idx_agents_user ON agents(user_id, section_id);
CREATE INDEX IF NOT EXISTS idx_threads_user ON threads(user_id, agent_id);
CREATE INDEX IF NOT EXISTS idx_messages_thread ON messages(user_id, thread_id, created_at);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);

-- runs are owned rows. run_events inherit ownership through the run and are
-- reached only after a query scoped by the caller's user id.
CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  thread_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  routine_run_id TEXT,
  status TEXT NOT NULL CHECK (status IN ('queued', 'streaming', 'waiting_approval', 'done', 'failed', 'stopped', 'retried')),
  stream_offset INTEGER NOT NULL DEFAULT 0,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  error TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS run_events (
  run_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('text', 'permission', 'tool', 'error', 'done')),
  payload_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (run_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_runs_user ON runs(user_id, thread_id, started_at);

-- S03 test stub. Plaintext on purpose. S08 replaces this with the vault.
CREATE TABLE IF NOT EXISTS agent_bundles (
  agent_id TEXT NOT NULL,
  version TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  agent_yaml TEXT NOT NULL,
  instructions_md TEXT NOT NULL,
  created_by TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (agent_id, version)
);

CREATE TABLE IF NOT EXISTS agent_tokens (
  token_hash TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('bootstrap', 'agent')),
  created_at TEXT NOT NULL,
  used_at TEXT
);

CREATE TABLE IF NOT EXISTS secret_fixtures (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  name TEXT NOT NULL,
  value TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_secret_grants (
  agent_id TEXT NOT NULL,
  secret_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  PRIMARY KEY (agent_id, secret_id)
);

CREATE INDEX IF NOT EXISTS idx_bundles_user ON agent_bundles(user_id, agent_id, created_at);
CREATE INDEX IF NOT EXISTS idx_tokens_agent ON agent_tokens(agent_id, kind);
CREATE INDEX IF NOT EXISTS idx_secrets_user ON secret_fixtures(user_id, name);
CREATE INDEX IF NOT EXISTS idx_grants_user ON agent_secret_grants(user_id, agent_id);

-- One machine row per agent. The agent token is plaintext until the vault
-- slice. Do not log it. deleted_at hides a machine that was removed.
CREATE TABLE IF NOT EXISTS agent_vms (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  microvm_id TEXT NOT NULL,
  name TEXT NOT NULL,
  endpoint TEXT NOT NULL DEFAULT '',
  image_ref TEXT NOT NULL,
  state TEXT NOT NULL,
  failure TEXT NOT NULL DEFAULT '',
  agent_token TEXT NOT NULL DEFAULT '',
  agent_token_hash TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  last_wake_at TEXT,
  created_running_at TEXT,
  observe_boot INTEGER NOT NULL DEFAULT 1,
  state_gen INTEGER NOT NULL DEFAULT 0,
  deleted_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_agent_vms_user ON agent_vms(user_id, agent_id);

-- Avatar bytes stay with the owning user. avatar_path on agents is only
-- the content type, so a list query can tell that an image exists.
CREATE TABLE IF NOT EXISTS agent_avatars (
  agent_id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  mime TEXT NOT NULL,
  bytes BLOB NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_avatars_user ON agent_avatars(user_id, agent_id);

-- CLI tokens are hashes only. The raw value is shown once and never stored.
CREATE TABLE IF NOT EXISTS cli_tokens (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('agent_direct', 'relay')),
  token_hash TEXT NOT NULL UNIQUE,
  prefix TEXT NOT NULL,
  scopes TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  expires_at TEXT,
  last_used_at TEXT,
  revoked_at TEXT,
  viewed_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_cli_tokens_user ON cli_tokens(user_id, agent_id);

-- Journal rows are idempotent on (agent_vm_id, seq).
CREATE TABLE IF NOT EXISTS journal_entries (
  agent_vm_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  caller TEXT NOT NULL,
  kind TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  tool TEXT NOT NULL DEFAULT '',
  request_id TEXT NOT NULL DEFAULT '',
  decision TEXT NOT NULL DEFAULT '',
  incomplete INTEGER NOT NULL DEFAULT 0,
  at TEXT NOT NULL,
  message_id TEXT NOT NULL DEFAULT '',
  stream_id TEXT NOT NULL DEFAULT '',
  placed INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (agent_vm_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_journal_user ON journal_entries(user_id, agent_id, seq);

CREATE TABLE IF NOT EXISTS turn_journal_cursor (
  agent_vm_id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  last_seq INTEGER NOT NULL,
  synced_at TEXT NOT NULL
);

-- Import drafts hold the keys the form does not own between review and save.
-- The form posts the id, never the document.
CREATE TABLE IF NOT EXISTS import_drafts (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL DEFAULT '',
  kept_yaml TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_import_drafts_user ON import_drafts(user_id, agent_id);

-- max_agents is the only cap. Running hours are not metered here.
CREATE TABLE IF NOT EXISTS user_quotas (
  user_id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  max_agents INTEGER NOT NULL
);

-- One row per database. Machines this process creates are tagged with it,
-- and the orphan sweep deletes only that tag.
CREATE TABLE IF NOT EXISTS instance (
  singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
  id TEXT NOT NULL
);
`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	if err := s.ensureColumn("users", "timezone", `ALTER TABLE users ADD COLUMN timezone TEXT NOT NULL DEFAULT 'America/Denver'`); err != nil {
		return err
	}
	if err := s.ensureColumn("credentials", "last_step", `ALTER TABLE credentials ADD COLUMN last_step INTEGER`); err != nil {
		return err
	}
	if err := s.ensureColumn("agents", "avatar_path", `ALTER TABLE agents ADD COLUMN avatar_path TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn("agents", "pending_bundle", `ALTER TABLE agents ADD COLUMN pending_bundle TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn("agents", "kept_yaml", `ALTER TABLE agents ADD COLUMN kept_yaml TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.allowRetried(); err != nil {
		return err
	}
	if err := s.allowCLIThread(); err != nil {
		return err
	}
	if err := s.ensureColumn("messages", "origin", `ALTER TABLE messages ADD COLUMN origin TEXT NOT NULL DEFAULT 'web'`); err != nil {
		return err
	}
	if err := s.ensureColumn("messages", "journal_seq", `ALTER TABLE messages ADD COLUMN journal_seq INTEGER`); err != nil {
		return err
	}
	if err := s.ensureColumn("journal_entries", "message_id", `ALTER TABLE journal_entries ADD COLUMN message_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn("journal_entries", "stream_id", `ALTER TABLE journal_entries ADD COLUMN stream_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn("agents", "cli_slug", `ALTER TABLE agents ADD COLUMN cli_slug TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_cli_slug ON agents(user_id, cli_slug) WHERE cli_slug != '' AND deleted_at IS NULL`); err != nil {
		return err
	}
	if err := s.ensureColumn("agent_vms", "created_running_at", `ALTER TABLE agent_vms ADD COLUMN created_running_at TEXT`); err != nil {
		return err
	}
	if err := s.ensureColumn("agent_vms", "observe_boot", `ALTER TABLE agent_vms ADD COLUMN observe_boot INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn("agents", "secret_fp", `ALTER TABLE agents ADD COLUMN secret_fp TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	missingViewed, err := s.columnMissing("cli_tokens", "viewed_at")
	if err != nil {
		return err
	}
	if err := s.ensureColumn("cli_tokens", "viewed_at", `ALTER TABLE cli_tokens ADD COLUMN viewed_at TEXT`); err != nil {
		return err
	}
	if missingViewed {
		if _, err := s.db.Exec(`UPDATE cli_tokens SET viewed_at = created_at WHERE viewed_at IS NULL OR viewed_at = ''`); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS vm_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  agent_vm_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  at TEXT NOT NULL,
  duration_ms INTEGER NOT NULL
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_vm_events_user ON vm_events(user_id, agent_id, id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_journal ON messages(thread_id, journal_seq) WHERE journal_seq IS NOT NULL`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_threads_cli_session ON threads(user_id, agent_id, cast_session_id) WHERE kind = 'cli' AND cast_session_id != ''`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO user_quotas(user_id, workspace_id, max_agents)
SELECT id, workspace_id, ? FROM users WHERE id NOT IN (SELECT user_id FROM user_quotas)`, DefaultMaxAgents); err != nil {
		return err
	}
	if err := s.ensureInstance(); err != nil {
		return err
	}
	for _, version := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12} {
		if _, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (?) ON CONFLICT(version) DO NOTHING`, version); err != nil {
			return err
		}
	}
	// Version 12 is the real runtime. Approvals are version 13.
	// Version 14 is the S07c carry-forward columns.
	if err := s.migrateApprovals(); err != nil {
		return err
	}
	if err := s.migrateV14(); err != nil {
		return err
	}
	return nil
}

// migrateV14 adds the machine state generation, the approval journal
// snapshot, and the journal placement mark. A message also records which
// machine journal it came from, so a new machine can reuse sequence numbers.
// vm_replacements and agent token revoke stay out of this version. Those
// belong to Replace machine.
func (s *Store) migrateV14() error {
	if err := s.ensureColumn("agent_vms", "state_gen", `ALTER TABLE agent_vms ADD COLUMN state_gen INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn("approvals", "recover_seq", `ALTER TABLE approvals ADD COLUMN recover_seq INTEGER`); err != nil {
		return err
	}
	missingPlaced, err := s.columnMissing("journal_entries", "placed")
	if err != nil {
		return err
	}
	if err := s.ensureColumn("journal_entries", "placed", `ALTER TABLE journal_entries ADD COLUMN placed INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn("messages", "agent_vm_id", `ALTER TABLE messages ADD COLUMN agent_vm_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if missingPlaced {
		// Decisions already filed on this agent's threads are not filed again.
		// The match is the agent, not the user: two agents share sequence numbers.
		if _, err := s.db.Exec(`UPDATE journal_entries SET placed = 1
WHERE EXISTS (
  SELECT 1 FROM messages m
  JOIN threads t ON t.id = m.thread_id AND t.user_id = m.user_id AND t.agent_id = journal_entries.agent_id
  WHERE m.user_id = journal_entries.user_id AND m.journal_seq = journal_entries.seq
)`); err != nil {
			return err
		}
	}
	cols, err := s.indexColumns("idx_messages_journal")
	if err != nil {
		return err
	}
	if len(cols) != 3 || cols[0] != "thread_id" || cols[1] != "journal_seq" || cols[2] != "agent_vm_id" {
		if _, err := s.db.Exec(`DROP INDEX IF EXISTS idx_messages_journal`); err != nil {
			return err
		}
		if _, err := s.db.Exec(`CREATE UNIQUE INDEX idx_messages_journal ON messages(thread_id, journal_seq, agent_vm_id) WHERE journal_seq IS NOT NULL`); err != nil {
			return err
		}
	}
	_, err = s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (14) ON CONFLICT(version) DO NOTHING`)
	return err
}

// allowRetried rebuilds runs when an older file still has the status check
// from before retry claimed a failed row. SQLite cannot alter a check.
func (s *Store) allowRetried() error {
	var ddl string
	err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'runs'`).Scan(&ddl)
	if err != nil {
		return err
	}
	if strings.Contains(ddl, "'retried'") {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TABLE runs_v4 (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  thread_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  routine_run_id TEXT,
  status TEXT NOT NULL CHECK (status IN ('queued', 'streaming', 'waiting_approval', 'done', 'failed', 'stopped', 'retried')),
  stream_offset INTEGER NOT NULL DEFAULT 0,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  error TEXT NOT NULL DEFAULT ''
)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO runs_v4(id, workspace_id, thread_id, agent_id, user_id, routine_run_id, status, stream_offset, started_at, finished_at, error)
SELECT id, workspace_id, thread_id, agent_id, user_id, routine_run_id, status, stream_offset, started_at, finished_at, error FROM runs`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE runs`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE runs_v4 RENAME TO runs`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_runs_user ON runs(user_id, thread_id, started_at)`); err != nil {
		return err
	}
	return tx.Commit()
}

// allowCLIThread rebuilds threads when an older file still rejects kind cli.
// SQLite cannot alter a check. New databases already include the kind.
// The rebuild drops threads and renames the replacement in one transaction.
// Take a backup before opening a database that still rejects kind cli.
func (s *Store) allowCLIThread() error {
	var ddl string
	err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'threads'`).Scan(&ddl)
	if err != nil {
		return err
	}
	if strings.Contains(ddl, "'cli'") {
		if err := s.ensureColumn("threads", "cast_session_id", `ALTER TABLE threads ADD COLUMN cast_session_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
		return s.fixThreadsUserIndex()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TABLE threads_v11 (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('dm', 'group', 'routine', 'cli')),
  agent_id TEXT,
  title TEXT NOT NULL DEFAULT '',
  cast_session_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO threads_v11(id, workspace_id, user_id, kind, agent_id, title, cast_session_id, created_at)
SELECT id, workspace_id, user_id, kind, agent_id, title, '', created_at FROM threads`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE threads`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE threads_v11 RENAME TO threads`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE INDEX idx_threads_user ON threads(user_id, agent_id)`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.fixThreadsUserIndex()
}

// fixThreadsUserIndex makes idx_threads_user match a fresh database.
// CREATE INDEX IF NOT EXISTS leaves an older (user_id, created_at) index in place
// because the name already exists.
func (s *Store) fixThreadsUserIndex() error {
	cols, err := s.indexColumns("idx_threads_user")
	if err != nil {
		return err
	}
	if len(cols) == 2 && cols[0] == "user_id" && cols[1] == "agent_id" {
		return nil
	}
	if len(cols) > 0 {
		if _, err := s.db.Exec(`DROP INDEX idx_threads_user`); err != nil {
			return err
		}
	}
	_, err = s.db.Exec(`CREATE INDEX idx_threads_user ON threads(user_id, agent_id)`)
	return err
}

func (s *Store) indexColumns(name string) ([]string, error) {
	rows, err := s.db.Query(`PRAGMA index_info(` + name + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var seqno, cid int
		var col string
		if err := rows.Scan(&seqno, &cid, &col); err != nil {
			return nil, err
		}
		cols = append(cols, col)
	}
	return cols, rows.Err()
}

func (s *Store) columnMissing(table, column string) (bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return false, rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) ensureColumn(table, column, alter string) error {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.Exec(alter)
	return err
}
