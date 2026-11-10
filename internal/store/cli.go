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
	MessageID  string `json:"message_id"`
	StreamID   string `json:"stream_id"`
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	if err := s.bumpCLIMint(ctx, tx, agent.WorkspaceID, userID, agentID); err != nil {
		return "", "", err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cli_tokens WHERE user_id = ? AND agent_id = ? AND revoked_at IS NULL`, userID, agentID).Scan(&active); err != nil {
		return "", "", err
	}
	if active >= cliActiveCap {
		return "", "", ErrCLICap
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO cli_tokens(id, workspace_id, user_id, agent_id, kind, token_hash, prefix, scopes, created_at) VALUES (?, ?, ?, ?, 'agent_direct', ?, ?, '', ?)`,
		id, agent.WorkspaceID, userID, agentID, tokenHash(plain), prefix, s.stamp(s.Now()))
	if err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return id, plain, nil
}

// TouchCLIToken records that castlet accepted this token.
// A revoked token is left unchanged.
func (s *Store) TouchCLIToken(ctx context.Context, userID, agentID, tokenID string) error {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE cli_tokens SET last_used_at = ? WHERE id = ? AND user_id = ? AND agent_id = ? AND revoked_at IS NULL`,
		s.stamp(s.Now()), tokenID, userID, agentID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) bumpCLIMint(ctx context.Context, tx *sql.Tx, workspaceID, userID, agentID string) error {
	subject := userID + ":" + agentID
	row, err := readLimitQ(ctx, tx, "cli_mint", subject)
	if err != nil {
		return err
	}
	now := s.Now()
	failures := 1
	window := now
	if row.present && now.Sub(row.window) <= cliMintWindow {
		if row.failures >= cliMintMax {
			return ErrCLIRate
		}
		failures = row.failures + 1
		window = row.window
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO login_limits(scope, subject, workspace_id, user_id, failures, window_start, locked_until)
		VALUES (?, ?, ?, ?, ?, ?, NULL)
		ON CONFLICT(scope, subject) DO UPDATE SET
		  workspace_id = excluded.workspace_id,
		  user_id = excluded.user_id,
		  failures = excluded.failures,
		  window_start = excluded.window_start,
		  locked_until = NULL`,
		"cli_mint", subject, workspaceID, userID, failures, s.stamp(window))
	return err
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
		res, err := tx.ExecContext(ctx, `INSERT INTO journal_entries(agent_vm_id, seq, workspace_id, user_id, agent_id, session_id, caller, kind, body, tool, request_id, decision, incomplete, at, message_id, stream_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(agent_vm_id, seq) DO NOTHING`,
			key, e.Seq, agent.WorkspaceID, userID, agentID, e.SessionID, e.Caller, e.Kind, e.Text, e.Tool, e.RequestID, e.Decision, boolInt(e.Incomplete), entryStamp(s, e.At), e.MessageID, e.StreamID)
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
	// Only a CLI token may open a Cast CLI thread. A control-plane turn
	// whose session is the web thread id is already stored there.
	if _, ok := cliCaller(e.Caller); ok {
		session := e.SessionID
		if session == "" {
			session = "cli"
		}
		th, err := s.cliThreadTx(ctx, tx, agent, session)
		if err != nil {
			return err
		}
		return s.insertJournalMessage(ctx, tx, agent, th, e, body, "cli")
	}
	if e.Caller != "control_plane" {
		return nil
	}
	th, ok, err := s.resolveSessionTx(ctx, tx, agent, e.SessionID)
	if err != nil || !ok {
		return err
	}
	if e.Kind == "request" || e.Kind == "reply" {
		attached, err := s.attachJournalMessage(ctx, tx, agent, th.ID, e)
		if err != nil || attached {
			return err
		}
		if th.Kind != "cli" {
			return nil
		}
	}
	return s.insertJournalMessage(ctx, tx, agent, th, e, body, "web")
}

func (s *Store) attachJournalMessage(ctx context.Context, tx *sql.Tx, agent Agent, threadID string, e JournalEntry) (bool, error) {
	if e.Kind == "request" && e.MessageID != "" {
		res, err := tx.ExecContext(ctx, `UPDATE messages SET journal_seq = ? WHERE id = ? AND user_id = ? AND thread_id = ? AND author_kind = 'user' AND journal_seq IS NULL`,
			e.Seq, e.MessageID, agent.UserID, threadID)
		if err != nil {
			return false, err
		}
		n, err := res.RowsAffected()
		return n == 1, err
	}
	if e.Kind == "reply" && e.StreamID != "" {
		res, err := tx.ExecContext(ctx, `UPDATE messages SET journal_seq = ? WHERE user_id = ? AND thread_id = ? AND author_kind = 'agent' AND run_id = ? AND journal_seq IS NULL`,
			e.Seq, agent.UserID, threadID, e.StreamID)
		if err != nil {
			return false, err
		}
		n, err := res.RowsAffected()
		return n >= 1, err
	}
	return false, nil
}

func (s *Store) insertJournalMessage(ctx context.Context, tx *sql.Tx, agent Agent, th Thread, e JournalEntry, body, origin string) error {
	author := "system"
	switch e.Kind {
	case "request":
		author = "user"
	case "reply":
		author = "agent"
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

func (s *Store) resolveSessionTx(ctx context.Context, tx *sql.Tx, agent Agent, session string) (Thread, bool, error) {
	if session == "" {
		return Thread{}, false, nil
	}
	t, err := scanThread(tx.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, kind, COALESCE(agent_id, ''), title, cast_session_id, created_at FROM threads WHERE user_id = ? AND agent_id = ? AND kind = 'cli' AND cast_session_id = ?`,
		agent.UserID, agent.ID, session))
	if err == nil {
		return t, true, nil
	}
	if err != sql.ErrNoRows {
		return Thread{}, false, err
	}
	t, err = scanThread(tx.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, kind, COALESCE(agent_id, ''), title, cast_session_id, created_at FROM threads WHERE id = ? AND user_id = ? AND agent_id = ?`,
		session, agent.UserID, agent.ID))
	if err == sql.ErrNoRows {
		return Thread{}, false, nil
	}
	if err != nil {
		return Thread{}, false, err
	}
	return t, true, nil
}

func scanThread(row *sql.Row) (Thread, error) {
	var t Thread
	err := row.Scan(&t.ID, &t.WorkspaceID, &t.UserID, &t.Kind, &t.AgentID, &t.Title, &t.CastSessionID, &t.CreatedAt)
	return t, err
}

func (s *Store) cliThreadTx(ctx context.Context, tx *sql.Tx, agent Agent, session string) (Thread, error) {
	t, err := scanThread(tx.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, kind, COALESCE(agent_id, ''), title, cast_session_id, created_at FROM threads WHERE user_id = ? AND agent_id = ? AND kind = 'cli' AND cast_session_id = ?`,
		agent.UserID, agent.ID, session))
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
		return scanThread(tx.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, kind, COALESCE(agent_id, ''), title, cast_session_id, created_at FROM threads WHERE user_id = ? AND agent_id = ? AND kind = 'cli' AND cast_session_id = ?`,
			agent.UserID, agent.ID, session))
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
	if e.Seq < 1 || len(e.Text) > replyLimit || len(e.SessionID) > 128 || len(e.Caller) > 80 || len(e.MessageID) > 80 || len(e.StreamID) > 80 {
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

// CLISlug is the name cast import accepts for this agent.
// A slug saved at the first export stays put. Before that, the result is
// the name that export would save, and it is not stored yet.
func (s *Store) CLISlug(ctx context.Context, userID, agentID string) (string, error) {
	agent, stored, err := s.cliSlugRow(ctx, userID, agentID)
	if err != nil {
		return "", err
	}
	if stored != "" {
		return stored, nil
	}
	return s.candidateSlug(ctx, userID, agent)
}

// AssignCLISlug saves the CLI name on the first export and returns it.
// A later rename or another agent's delete does not change it.
func (s *Store) AssignCLISlug(ctx context.Context, userID, agentID string) (string, error) {
	agent, stored, err := s.cliSlugRow(ctx, userID, agentID)
	if err != nil {
		return "", err
	}
	if stored != "" {
		return stored, nil
	}
	slug, err := s.candidateSlug(ctx, userID, agent)
	if err != nil {
		return "", err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE agents SET cli_slug = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL AND cli_slug = ''`,
		slug, agentID, userID)
	if err != nil {
		if !strings.Contains(err.Error(), "UNIQUE") {
			return "", err
		}
		slug, err = s.candidateSlug(ctx, userID, agent)
		if err != nil {
			return "", err
		}
		res, err = s.db.ExecContext(ctx, `UPDATE agents SET cli_slug = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL AND cli_slug = ''`,
			slug, agentID, userID)
		if err != nil {
			return "", err
		}
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", err
	}
	if n == 1 {
		return slug, nil
	}
	_, stored, err = s.cliSlugRow(ctx, userID, agentID)
	if err != nil {
		return "", err
	}
	if stored == "" {
		return "", ErrConflict
	}
	return stored, nil
}

func (s *Store) cliSlugRow(ctx context.Context, userID, agentID string) (Agent, string, error) {
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return Agent{}, "", err
	}
	var stored string
	err = s.db.QueryRowContext(ctx, `SELECT cli_slug FROM agents WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, agentID, userID).Scan(&stored)
	if err != nil {
		return Agent{}, "", err
	}
	return agent, stored, nil
}

func (s *Store) candidateSlug(ctx context.Context, userID string, agent Agent) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT cli_slug FROM agents WHERE user_id = ? AND deleted_at IS NULL AND id <> ? AND cli_slug <> ''`, userID, agent.ID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	used := map[string]struct{}{}
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return "", err
		}
		used[slug] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return claimSlug(slugBase(agent.Name), agent.ID, used), nil
}

func claimSlug(base, id string, used map[string]struct{}) string {
	if base == "" {
		base = "agent"
	}
	if _, ok := used[base]; !ok {
		return base
	}
	suffix := id
	if len(suffix) > 6 {
		suffix = suffix[:6]
	}
	candidate := base + "-" + suffix
	if _, ok := used[candidate]; !ok {
		return candidate
	}
	return base + "-" + id
}

func slugBase(name string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		case r == '\'' || r == '\u2019':
		default:
			if b.Len() > 0 && !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 48 {
		out = strings.Trim(out[:48], "-")
	}
	return out
}
