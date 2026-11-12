package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// File is one upload kept on the control plane.
// Source is upload or agent. Status for an upload is stored, held, copied, or failed.
type File struct {
	ID          string
	WorkspaceID string
	UserID      string
	AgentID     string
	ThreadID    string
	Name        string
	Mime        string
	Size        int64
	SHA256      string
	Source      string
	Path        string
	Status      string
	SeenAt      time.Time
	CreatedAt   time.Time
	Announced   bool
}

// CachedFile is one workspace path remembered while the machine is paused.
// SeenAt is the first time this name and size were observed.
type CachedFile struct {
	ID          string
	WorkspaceID string
	UserID      string
	AgentID     string
	Name        string
	Size        int64
	SeenAt      time.Time
}

// ListedFile is a name and size from a live workspace listing.
type ListedFile struct {
	Name string
	Size int64
}

// FileHit is one sidebar search match.
type FileHit struct {
	ID        string
	AgentID   string
	AgentName string
	Name      string
	Path      string
	Size      int64
	Source    string
	Paused    bool
}

// migrateFiles creates the files table and the workspace listing cache.
// It is safe to run more than once.
func (s *Store) migrateFiles() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS files (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  thread_id TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  mime TEXT NOT NULL DEFAULT '',
  size INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  source TEXT NOT NULL CHECK (source IN ('upload', 'agent')),
  path TEXT NOT NULL,
  seen_at TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('stored', 'held', 'copied', 'failed')),
  announced INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_files_user ON files(user_id, agent_id, source)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_files_workspace ON files(workspace_id, source, status)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS file_cache (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  name TEXT NOT NULL,
  size INTEGER NOT NULL,
  seen_at TEXT NOT NULL,
  UNIQUE (user_id, agent_id, name)
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_file_cache_user ON file_cache(user_id, agent_id)`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (18) ON CONFLICT(version) DO NOTHING`)
	return err
}

// InsertUpload records one upload row. The caller has already stored the blob.
func (s *Store) InsertUpload(ctx context.Context, userID, agentID, threadID, name, mime, sum, path, status string, size int64) (File, error) {
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return File{}, err
	}
	name = strings.TrimSpace(name)
	status = strings.TrimSpace(status)
	if name == "" || sum == "" || path == "" || size < 0 {
		return File{}, ErrInvalid
	}
	switch status {
	case "stored", "held", "copied", "failed":
	default:
		return File{}, ErrInvalid
	}
	id, err := newID()
	if err != nil {
		return File{}, err
	}
	now := s.Now()
	stamp := s.stamp(now)
	_, err = s.db.ExecContext(ctx, `INSERT INTO files(
  id, workspace_id, user_id, agent_id, thread_id, name, mime, size, sha256, source, path, seen_at, status, announced, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'upload', ?, ?, ?, 0, ?)`,
		id, agent.WorkspaceID, userID, agentID, strings.TrimSpace(threadID), name, strings.TrimSpace(mime), size, sum, path, stamp, status, stamp)
	if err != nil {
		return File{}, err
	}
	return s.GetFile(ctx, userID, id)
}

// GetFile returns one of the caller's files.
func (s *Store) GetFile(ctx context.Context, userID, id string) (File, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, agent_id, thread_id, name, mime, size, sha256, source, path, seen_at, status, announced, created_at
FROM files WHERE id = ? AND user_id = ?`, id, userID)
	return scanFile(row)
}

// ListUploads returns the caller's uploads for one agent, newest first.
func (s *Store) ListUploads(ctx context.Context, userID, agentID string) ([]File, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, user_id, agent_id, thread_id, name, mime, size, sha256, source, path, seen_at, status, announced, created_at
FROM files WHERE user_id = ? AND agent_id = ? AND source = 'upload' AND status != 'failed'
ORDER BY created_at DESC, name`, userID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ListHeldUploads returns uploads waiting to be copied into a machine.
func (s *Store) ListHeldUploads(ctx context.Context, userID, agentID string) ([]File, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, user_id, agent_id, thread_id, name, mime, size, sha256, source, path, seen_at, status, announced, created_at
FROM files WHERE user_id = ? AND agent_id = ? AND source = 'upload' AND status = 'held'
ORDER BY created_at`, userID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// UploadNames is the set of upload names already kept for one agent.
// Failed rows are omitted so a retry can reuse the name.
func (s *Store) UploadNames(ctx context.Context, userID, agentID string) (map[string]struct{}, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM files WHERE user_id = ? AND agent_id = ? AND source = 'upload' AND status IN ('stored', 'held', 'copied')`, userID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = struct{}{}
	}
	return out, rows.Err()
}

// UploadUsage is the sum of upload bytes stored for one workspace.
func (s *Store) UploadUsage(ctx context.Context, userID, workspaceID string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(size), 0) FROM files
WHERE user_id = ? AND workspace_id = ? AND source = 'upload' AND status IN ('stored', 'held', 'copied')`, userID, workspaceID).Scan(&n)
	return n, err
}

// MarkUploadStatus moves one of the caller's uploads to a new status.
func (s *Store) MarkUploadStatus(ctx context.Context, userID, id, status string) error {
	switch status {
	case "stored", "held", "copied", "failed":
	default:
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `UPDATE files SET status = ? WHERE id = ? AND user_id = ?`, status, id, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// PendingAnnouncements returns uploads whose path has not been named in a message yet.
func (s *Store) PendingAnnouncements(ctx context.Context, userID, agentID string) ([]File, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, user_id, agent_id, thread_id, name, mime, size, sha256, source, path, seen_at, status, announced, created_at
FROM files WHERE user_id = ? AND agent_id = ? AND source = 'upload' AND announced = 0 AND status IN ('stored', 'held', 'copied')
ORDER BY created_at`, userID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// MarkAnnounced records that the path line was included in a user message.
func (s *Store) MarkAnnounced(ctx context.Context, userID string, ids []string) error {
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE files SET announced = 1 WHERE id = ? AND user_id = ?`, id, userID); err != nil {
			return err
		}
	}
	return nil
}

// ObserveWorkspace updates the listing cache from a live listing.
// A row keeps its seen time when the name and size are unchanged.
func (s *Store) ObserveWorkspace(ctx context.Context, userID, agentID string, listed []ListedFile) error {
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seen := map[string]struct{}{}
	now := s.stamp(s.Now())
	for _, item := range listed {
		name := strings.TrimSpace(item.Name)
		if !cacheNameOK(name) {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		var id, seenAt string
		var size int64
		err := tx.QueryRowContext(ctx, `SELECT id, size, seen_at FROM file_cache WHERE user_id = ? AND agent_id = ? AND name = ?`, userID, agentID, name).Scan(&id, &size, &seenAt)
		if err == nil {
			if size == item.Size {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE file_cache SET size = ?, seen_at = ? WHERE id = ? AND user_id = ?`, item.Size, now, id, userID); err != nil {
				return err
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		id, err = newID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO file_cache(id, workspace_id, user_id, agent_id, name, size, seen_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, agent.WorkspaceID, userID, agentID, name, item.Size, now); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT name FROM file_cache WHERE user_id = ? AND agent_id = ?`, userID, agentID)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		if _, ok := seen[name]; !ok {
			stale = append(stale, name)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, name := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM file_cache WHERE user_id = ? AND agent_id = ? AND name = ?`, userID, agentID, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func cacheNameOK(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, `\`) || strings.Contains(name, "..") || strings.ContainsRune(name, 0) {
		return false
	}
	low := strings.ToLower(name)
	if strings.Contains(low, "%2f") || strings.Contains(low, "%5c") || strings.Contains(low, "%00") || strings.Contains(low, "%2e%2e") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// PlantCached inserts a cache row without the name check.
// Download and preview must still refuse a name that leaves the workspace.
func (s *Store) PlantCached(ctx context.Context, userID, agentID, name string, size int64) (string, error) {
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return "", err
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	now := s.stamp(s.Now())
	if _, err := s.db.ExecContext(ctx, `INSERT INTO file_cache(id, workspace_id, user_id, agent_id, name, size, seen_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, agent.WorkspaceID, userID, agentID, name, size, now); err != nil {
		return "", err
	}
	return id, nil
}

// ListCached returns the cached workspace listing for one agent.
func (s *Store) ListCached(ctx context.Context, userID, agentID string) ([]CachedFile, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, user_id, agent_id, name, size, seen_at FROM file_cache WHERE user_id = ? AND agent_id = ? ORDER BY name`, userID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CachedFile
	for rows.Next() {
		var f CachedFile
		var seen string
		if err := rows.Scan(&f.ID, &f.WorkspaceID, &f.UserID, &f.AgentID, &f.Name, &f.Size, &seen); err != nil {
			return nil, err
		}
		f.SeenAt, _ = parseTime(seen)
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetCached returns one cached workspace file for the caller.
func (s *Store) GetCached(ctx context.Context, userID, id string) (CachedFile, error) {
	var f CachedFile
	var seen string
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, agent_id, name, size, seen_at FROM file_cache WHERE id = ? AND user_id = ?`, id, userID).
		Scan(&f.ID, &f.WorkspaceID, &f.UserID, &f.AgentID, &f.Name, &f.Size, &seen)
	if err != nil {
		return CachedFile{}, scanErr(err)
	}
	f.SeenAt, _ = parseTime(seen)
	return f, nil
}

// SearchFiles matches file names across the caller's agents.
// Paused agents contribute their cached names.
func (s *Store) SearchFiles(ctx context.Context, userID, query string) ([]FileHit, error) {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" || len(query) > 80 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.agent_id, a.name, c.name, c.size, a.status
FROM file_cache c
JOIN agents a ON a.id = c.agent_id AND a.user_id = c.user_id
WHERE c.user_id = ? AND instr(lower(c.name), ?) > 0
ORDER BY c.name LIMIT 20`, userID, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileHit
	for rows.Next() {
		var h FileHit
		var status string
		if err := rows.Scan(&h.ID, &h.AgentID, &h.AgentName, &h.Name, &h.Size, &status); err != nil {
			return nil, err
		}
		h.Source = "agent"
		h.Path = "workspace/" + h.Name
		h.Paused = status == "paused"
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	urows, err := s.db.QueryContext(ctx, `SELECT f.id, f.agent_id, a.name, f.name, f.path, f.size, a.status
FROM files f
JOIN agents a ON a.id = f.agent_id AND a.user_id = f.user_id
WHERE f.user_id = ? AND f.source = 'upload' AND f.status IN ('stored', 'held', 'copied') AND instr(lower(f.name), ?) > 0
ORDER BY f.created_at DESC LIMIT 20`, userID, query)
	if err != nil {
		return nil, err
	}
	defer urows.Close()
	for urows.Next() {
		var h FileHit
		var status string
		if err := urows.Scan(&h.ID, &h.AgentID, &h.AgentName, &h.Name, &h.Path, &h.Size, &status); err != nil {
			return nil, err
		}
		h.Source = "upload"
		h.Paused = status == "paused"
		out = append(out, h)
	}
	return out, urows.Err()
}

type fileScanner interface {
	Scan(dest ...any) error
}

func scanFile(row fileScanner) (File, error) {
	var f File
	var seen, created string
	var announced int
	err := row.Scan(&f.ID, &f.WorkspaceID, &f.UserID, &f.AgentID, &f.ThreadID, &f.Name, &f.Mime, &f.Size, &f.SHA256, &f.Source, &f.Path, &seen, &f.Status, &announced, &created)
	if err != nil {
		return File{}, scanErr(err)
	}
	f.SeenAt, _ = parseTime(seen)
	f.CreatedAt, _ = parseTime(created)
	f.Announced = announced != 0
	return f, nil
}
