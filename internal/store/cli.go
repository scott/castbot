package store

import (
	"context"
	"database/sql"
	"strings"
)

// CLIToken is one Cast CLI credential. The raw value is never stored.
type CLIToken struct {
	ID         string
	AgentID    string
	Kind       string
	Prefix     string
	CreatedAt  string
	LastUsedAt string
	RevokedAt  string
}

// CLIHash is the hash castlet stores. It is not the raw token.
type CLIHash struct {
	ID      string `json:"id"`
	Hash    string `json:"hash"`
	Revoked bool   `json:"revoked"`
}

// JournalEntry is one turn record pushed or pulled from castlet.
type JournalEntry struct {
	Seq        int    `json:"seq"`
	At         string `json:"at"`
	SessionID  string `json:"session_id"`
	Caller     string `json:"caller"`
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	Tool       string `json:"tool"`
	RequestID  string `json:"request_id"`
	Decision   string `json:"decision"`
	Incomplete bool   `json:"incomplete"`
}

// IssueCLIToken mints a CLI token for one of the caller's agents.
// The plain value is returned once. Only its hash is stored.
func (s *Store) IssueCLIToken(ctx context.Context, userID, agentID string) (id, plain string, err error) {
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return "", "", err
	}
	plain, err = newToken()
	if err != nil {
		return "", "", err
	}
	id, err = newID()
	if err != nil {
		return "", "", err
	}
	prefix := plain
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO cli_tokens(id, workspace_id, user_id, agent_id, kind, token_hash, prefix, scopes, created_at) VALUES (?, ?, ?, ?, 'agent_direct', ?, ?, '', ?)`,
		id, agent.WorkspaceID, userID, agentID, tokenHash(plain), prefix, s.stamp(s.Now()))
	if err != nil {
		return "", "", err
	}
	return id, plain, nil
}

// ListCLITokens returns CLI token metadata for one of the caller's agents.
func (s *Store) ListCLITokens(ctx context.Context, userID, agentID string) ([]CLIToken, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, agent_id, kind, prefix, created_at, COALESCE(last_used_at, ''), COALESCE(revoked_at, '') FROM cli_tokens WHERE user_id = ? AND agent_id = ? ORDER BY created_at, id`, userID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CLIToken
	for rows.Next() {
		var t CLIToken
		if err := rows.Scan(&t.ID, &t.AgentID, &t.Kind, &t.Prefix, &t.CreatedAt, &t.LastUsedAt, &t.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListCLITokenHashes returns hashes for the guest. The raw token is not included.
func (s *Store) ListCLITokenHashes(ctx context.Context, userID, agentID string) ([]CLIHash, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, token_hash, revoked_at IS NOT NULL FROM cli_tokens WHERE user_id = ? AND agent_id = ? ORDER BY created_at, id`, userID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CLIHash
	for rows.Next() {
		var t CLIHash
		if err := rows.Scan(&t.ID, &t.Hash, &t.Revoked); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeCLIToken revokes one of the caller's CLI tokens.
func (s *Store) RevokeCLIToken(ctx context.Context, userID, agentID, tokenID string) error {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE cli_tokens SET revoked_at = ? WHERE id = ? AND user_id = ? AND agent_id = ? AND revoked_at IS NULL`,
		s.stamp(s.Now()), tokenID, userID, agentID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	var exists int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cli_tokens WHERE id = ? AND user_id = ? AND agent_id = ?`, tokenID, userID, agentID).Scan(&exists)
	if err != nil {
		return err
	}
	if exists == 0 {
		return ErrNotFound
	}
	return nil
}

// JournalCursor is the last journal sequence stored for the caller's machine.
func (s *Store) JournalCursor(ctx context.Context, userID, agentID string) (int, error) {
	key, err := s.agentVMKey(ctx, userID, agentID)
	if err != nil {
		return 0, err
	}
	var last int
	err = s.db.QueryRowContext(ctx, `SELECT last_seq FROM turn_journal_cursor WHERE agent_vm_id = ? AND user_id = ?`, key, userID).Scan(&last)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return last, nil
}

// IngestJournal stores journal rows and the messages they represent.
// A second copy of the same (agent_vm_id, seq) does not add a message.
// last is the highest sequence stored for this machine.
func (s *Store) IngestJournal(ctx context.Context, userID, agentID string, entries []JournalEntry) (int, error) {
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		if err := validEntry(e); err != nil {
			return 0, err
		}
	}
	key, err := s.agentVMKey(ctx, userID, agentID)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	last := 0
	for _, e := range entries {
		if e.Seq > last {
			last = e.Seq
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO journal_entries(agent_vm_id, seq, workspace_id, user_id, agent_id, session_id, caller, kind, body, tool, request_id, decision, incomplete, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(agent_vm_id, seq) DO NOTHING`,
			key, e.Seq, agent.WorkspaceID, userID, agentID, e.SessionID, e.Caller, e.Kind, e.Text, e.Tool, e.RequestID, e.Decision, boolInt(e.Incomplete), entryStamp(s, e.At))
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		if n == 0 {
			continue
		}
		if err := s.journalMessage(ctx, tx, agent, key, e); err != nil {
			return 0, err
		}
		if id, ok := cliCaller(e.Caller); ok {
			_, _ = tx.ExecContext(ctx, `UPDATE cli_tokens SET last_used_at = ? WHERE id = ? AND user_id = ? AND agent_id = ?`,
				s.stamp(s.Now()), id, userID, agentID)
		}
	}
	if last > 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO turn_journal_cursor(agent_vm_id, workspace_id, user_id, last_seq, synced_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(agent_vm_id) DO UPDATE SET last_seq = MAX(turn_journal_cursor.last_seq, excluded.last_seq), synced_at = excluded.synced_at
WHERE turn_journal_cursor.user_id = excluded.user_id`,
			key, agent.WorkspaceID, userID, last, s.stamp(s.Now()))
		if err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return s.JournalCursor(ctx, userID, agentID)
}

func (s *Store) journalMessage(ctx context.Context, tx *sql.Tx, agent Agent, vmKey string, e JournalEntry) error {
	body := journalBody(e)
	if strings.TrimSpace(body) == "" {
		return nil
	}
	if len(body) > replyLimit {
		body = body[:replyLimit]
	}
	session := e.SessionID
	if session == "" {
		session = "cli"
	}
	th, err := s.cliThreadTx(ctx, tx, agent, session)
	if err != nil {
		return err
	}
	author := "system"
	switch e.Kind {
	case "request":
		author = "user"
	case "reply":
		author = "agent"
	}
	if e.Caller == "control_plane" && (author == "user" || author == "agent") {
		var id string
		err := tx.QueryRowContext(ctx, `SELECT id FROM messages WHERE user_id = ? AND thread_id = ? AND author_kind = ? AND body_md = ? AND journal_seq IS NULL ORDER BY created_at DESC, rowid DESC LIMIT 1`,
			agent.UserID, th.ID, author, body).Scan(&id)
		if err == nil && id != "" {
			res, err := tx.ExecContext(ctx, `UPDATE messages SET journal_seq = ? WHERE id = ? AND user_id = ? AND journal_seq IS NULL`, e.Seq, id, agent.UserID)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n == 1 {
				return nil
			}
		} else if err != nil && err != sql.ErrNoRows {
			return err
		}
	}
	origin := "cli"
	if e.Caller == "control_plane" {
		origin = "web"
	}
	id, err := newID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO messages(id, workspace_id, user_id, thread_id, author_kind, author_id, body_md, origin, journal_seq, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, agent.WorkspaceID, agent.UserID, th.ID, author, agent.ID, body, origin, e.Seq, entryStamp(s, e.At))
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return nil
	}
	return err
}

func (s *Store) cliThreadTx(ctx context.Context, tx *sql.Tx, agent Agent, session string) (Thread, error) {
	var t Thread
	err := tx.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, kind, COALESCE(agent_id, ''), title, cast_session_id, created_at FROM threads WHERE user_id = ? AND agent_id = ? AND kind = 'cli' AND cast_session_id = ?`,
		agent.UserID, agent.ID, session).Scan(&t.ID, &t.WorkspaceID, &t.UserID, &t.Kind, &t.AgentID, &t.Title, &t.CastSessionID, &t.CreatedAt)
	if err == nil {
		return t, nil
	}
	if err != sql.ErrNoRows {
		return Thread{}, err
	}
	id, err := newID()
	if err != nil {
		return Thread{}, err
	}
	stamp := s.stamp(s.Now())
	_, err = tx.ExecContext(ctx, `INSERT INTO threads(id, workspace_id, user_id, kind, agent_id, title, cast_session_id, created_at) VALUES (?, ?, ?, 'cli', ?, 'Cast CLI', ?, ?)`,
		id, agent.WorkspaceID, agent.UserID, agent.ID, session, stamp)
	if err != nil {
		if !strings.Contains(err.Error(), "UNIQUE") {
			return Thread{}, err
		}
		err = tx.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, kind, COALESCE(agent_id, ''), title, cast_session_id, created_at FROM threads WHERE user_id = ? AND agent_id = ? AND kind = 'cli' AND cast_session_id = ?`,
			agent.UserID, agent.ID, session).Scan(&t.ID, &t.WorkspaceID, &t.UserID, &t.Kind, &t.AgentID, &t.Title, &t.CastSessionID, &t.CreatedAt)
		return t, err
	}
	t = Thread{ID: id, WorkspaceID: agent.WorkspaceID, UserID: agent.UserID, Kind: "cli", AgentID: agent.ID, Title: "Cast CLI", CastSessionID: session, CreatedAt: stamp}
	return t, nil
}

func (s *Store) agentVMKey(ctx context.Context, userID, agentID string) (string, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return "", err
	}
	vm, err := s.LiveVM(ctx, userID, agentID)
	if err == nil && vm.ID != "" {
		return vm.ID, nil
	}
	if err != nil && err != ErrNotFound {
		return "", err
	}
	return "agent:" + agentID, nil
}

// SweepExpiredDrafts deletes import drafts whose hour has passed.
func (s *Store) SweepExpiredDrafts(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM import_drafts WHERE expires_at <= ?`, s.stamp(s.Now()))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func validEntry(e JournalEntry) error {
	if e.Seq < 1 || len(e.Text) > replyLimit || len(e.SessionID) > 128 || len(e.Caller) > 80 {
		return ErrInvalid
	}
	switch e.Kind {
	case "request", "reply", "permission", "decision":
	default:
		return ErrInvalid
	}
	if e.Caller != "control_plane" && !strings.HasPrefix(e.Caller, "cli:") {
		return ErrInvalid
	}
	return nil
}

func cliCaller(caller string) (string, bool) {
	id, ok := strings.CutPrefix(caller, "cli:")
	if !ok || id == "" || len(id) > 64 {
		return "", false
	}
	return id, true
}

func journalBody(e JournalEntry) string {
	switch e.Kind {
	case "request", "reply":
		text := e.Text
		if e.Incomplete && strings.TrimSpace(text) != "" && !strings.Contains(text, "This turn did not finish.") {
			text += "\n\nThis turn did not finish."
		}
		if e.Incomplete && strings.TrimSpace(text) == "" {
			return "This turn did not finish."
		}
		return text
	case "permission":
		tool := strings.TrimSpace(e.Tool)
		if tool == "" {
			return "An approval was asked. Answer it in the Cast CLI."
		}
		return "An approval was asked for " + tool + ". Answer it in the Cast CLI."
	case "decision":
		tool := strings.TrimSpace(e.Tool)
		word := "recorded"
		switch e.Decision {
		case "approved":
			word = "approved"
		case "denied":
			word = "denied"
		}
		if tool == "" {
			return "An approval was " + word + "."
		}
		return "An approval for " + tool + " was " + word + "."
	default:
		return ""
	}
}

func entryStamp(s *Store, at string) string {
	if t, err := parseTime(at); err == nil && !t.IsZero() {
		return s.stamp(t)
	}
	return s.stamp(s.Now())
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
