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
  created_at TEXT NOT NULL,
  deleted_at TEXT
);

CREATE TABLE IF NOT EXISTS threads (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('dm', 'group', 'routine')),
  agent_id TEXT,
  title TEXT NOT NULL DEFAULT '',
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
	if _, err := s.db.Exec(`INSERT INTO user_quotas(user_id, workspace_id, max_agents)
SELECT id, workspace_id, ? FROM users WHERE id NOT IN (SELECT user_id FROM user_quotas)`, DefaultMaxAgents); err != nil {
		return err
	}
	if err := s.ensureInstance(); err != nil {
		return err
	}
	for _, version := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10} {
		if _, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (?) ON CONFLICT(version) DO NOTHING`, version); err != nil {
			return err
		}
	}
	return nil
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
