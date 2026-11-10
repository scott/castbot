package store

import "database/sql"

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
  model TEXT NOT NULL DEFAULT '',
  bundle_version TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK (status IN ('draft', 'provisioning', 'ready', 'paused', 'failed', 'deleted')),
  activity TEXT NOT NULL DEFAULT '',
  unread INTEGER NOT NULL DEFAULT 0,
  approvals INTEGER NOT NULL DEFAULT 0,
  idle_timeout INTEGER NOT NULL DEFAULT 900,
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
  status TEXT NOT NULL CHECK (status IN ('queued', 'streaming', 'waiting_approval', 'done', 'failed', 'stopped')),
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
	if _, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (1) ON CONFLICT(version) DO NOTHING`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (2) ON CONFLICT(version) DO NOTHING`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (3) ON CONFLICT(version) DO NOTHING`)
	return err
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
