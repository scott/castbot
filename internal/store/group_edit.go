package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// migrateV31 stores per-person group pins, section placement, collapsed
// sidebar sections, and the current delete confirmation for a group.
func (s *Store) migrateV31() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS group_pins (
  thread_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  pinned_at TEXT NOT NULL,
  PRIMARY KEY (thread_id, user_id)
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_group_pins_user ON group_pins(user_id, workspace_id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS group_places (
  thread_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  section_id TEXT NOT NULL,
  PRIMARY KEY (thread_id, user_id)
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_group_places_user ON group_places(user_id, section_id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS section_folds (
  user_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  section_key TEXT NOT NULL,
  PRIMARY KEY (user_id, section_key)
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS group_confirms (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  thread_id TEXT NOT NULL,
  created_at TEXT NOT NULL
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_group_confirms_one ON group_confirms(user_id, thread_id)`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (31) ON CONFLICT(version) DO NOTHING`)
	return err
}

// GroupCard is a group the caller can open, with that person's pin and section.
type GroupCard struct {
	Thread
	Pinned    bool
	PinnedAt  string
	SectionID string
}

// ListGroupCards returns the caller's groups.
// Pinned groups come first, most recently pinned first, then newest created.
func (s *Store) ListGroupCards(ctx context.Context, userID string) ([]GroupCard, error) {
	if _, err := s.GetUser(ctx, userID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t.id, t.workspace_id, t.user_id, t.kind, COALESCE(t.agent_id, ''), t.title, t.cast_session_id, t.created_at,
  CASE WHEN p.thread_id IS NULL THEN 0 ELSE 1 END,
  COALESCE(p.pinned_at, ''),
  CASE WHEN sec.id IS NULL THEN '' ELSE pl.section_id END
FROM threads t
JOIN thread_members m ON m.thread_id = t.id AND m.member_id = ? AND m.workspace_id = t.workspace_id
JOIN users u ON u.id = ? AND u.workspace_id = t.workspace_id AND u.disabled_at IS NULL
LEFT JOIN group_pins p ON p.thread_id = t.id AND p.user_id = ? AND p.workspace_id = t.workspace_id
LEFT JOIN group_places pl ON pl.thread_id = t.id AND pl.user_id = ? AND pl.workspace_id = t.workspace_id
LEFT JOIN sections sec ON sec.id = pl.section_id AND sec.user_id = pl.user_id
WHERE t.kind = 'group'
ORDER BY CASE WHEN p.thread_id IS NULL THEN 1 ELSE 0 END, p.pinned_at DESC, t.created_at DESC`, userID, userID, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GroupCard
	for rows.Next() {
		var card GroupCard
		var pinned int
		if err := rows.Scan(&card.ID, &card.WorkspaceID, &card.UserID, &card.Kind, &card.AgentID, &card.Title, &card.CastSessionID, &card.CreatedAt, &pinned, &card.PinnedAt, &card.SectionID); err != nil {
			return nil, err
		}
		card.Pinned = pinned == 1
		out = append(out, card)
	}
	return out, rows.Err()
}

// SetGroupPin pins or unpins a group for the caller only.
func (s *Store) SetGroupPin(ctx context.Context, userID, threadID string, pin bool) error {
	th, err := s.GroupAccess(ctx, userID, threadID)
	if err != nil {
		return err
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if !pin {
		_, err = s.db.ExecContext(ctx, `DELETE FROM group_pins WHERE thread_id = ? AND user_id = ?`, th.ID, userID)
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO group_pins(thread_id, user_id, workspace_id, pinned_at) VALUES (?, ?, ?, ?)
ON CONFLICT(thread_id, user_id) DO UPDATE SET pinned_at = excluded.pinned_at`, th.ID, userID, user.WorkspaceID, s.stamp(s.Now()))
	return err
}

// PlaceGroup moves a group into one of the caller's sections.
// An empty section id, or "groups", puts it back in the Groups list.
// A section the caller does not own is ErrNotFound.
func (s *Store) PlaceGroup(ctx context.Context, userID, threadID, sectionID string) error {
	th, err := s.GroupAccess(ctx, userID, threadID)
	if err != nil {
		return err
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	sectionID = strings.TrimSpace(sectionID)
	if sectionID == "" || sectionID == "groups" {
		_, err = s.db.ExecContext(ctx, `DELETE FROM group_places WHERE thread_id = ? AND user_id = ?`, th.ID, userID)
		return err
	}
	if _, err := s.GetSection(ctx, userID, sectionID); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO group_places(thread_id, user_id, workspace_id, section_id) VALUES (?, ?, ?, ?)
ON CONFLICT(thread_id, user_id) DO UPDATE SET section_id = excluded.section_id`, th.ID, userID, user.WorkspaceID, sectionID)
	return err
}

// ToggleFold collapses a sidebar section, or expands it when it is already collapsed.
// sectionKey is a section id, or "groups" for the Groups list.
// A section the caller does not own is ErrNotFound.
func (s *Store) ToggleFold(ctx context.Context, userID, sectionKey string) (bool, error) {
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return false, err
	}
	sectionKey = strings.TrimSpace(sectionKey)
	if sectionKey == "" {
		return false, ErrNotFound
	}
	if sectionKey != "groups" {
		if _, err := s.GetSection(ctx, userID, sectionKey); err != nil {
			return false, err
		}
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM section_folds WHERE user_id = ? AND section_key = ?`, userID, sectionKey).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		_, err = s.db.ExecContext(ctx, `DELETE FROM section_folds WHERE user_id = ? AND section_key = ?`, userID, sectionKey)
		return false, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO section_folds(user_id, workspace_id, section_key) VALUES (?, ?, ?)`, userID, user.WorkspaceID, sectionKey)
	return true, err
}

// ExpandSection opens a sidebar section that this person had collapsed.
// A section that is already open stays open. sectionKey is a section id.
// A section the caller does not own is ErrNotFound.
func (s *Store) ExpandSection(ctx context.Context, userID, sectionKey string) error {
	if _, err := s.GetUser(ctx, userID); err != nil {
		return err
	}
	sectionKey = strings.TrimSpace(sectionKey)
	if sectionKey == "" || sectionKey == "groups" {
		return ErrNotFound
	}
	if _, err := s.GetSection(ctx, userID, sectionKey); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM section_folds WHERE user_id = ? AND section_key = ?`, userID, sectionKey)
	return err
}

// FoldedSections returns the section keys this person has collapsed.
func (s *Store) FoldedSections(ctx context.Context, userID string) (map[string]bool, error) {
	if _, err := s.GetUser(ctx, userID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT section_key FROM section_folds WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out[key] = true
	}
	return out, rows.Err()
}

// UpdateGroup changes the title, agents, and people on a group the caller created.
// A run that is still open is ErrConflict and changes nothing.
// The caller stays a member. Once two of the caller's agents are in the group,
// at least two must remain. A group that still has fewer than two can be saved
// with the agents chosen so far.
func (s *Store) UpdateGroup(ctx context.Context, userID, threadID, title string, agentIDs, memberIDs []string) error {
	th, err := s.creatorGroup(ctx, userID, threadID)
	if err != nil {
		return err
	}
	open, err := s.GroupOpen(ctx, userID, th.ID)
	if err != nil {
		return err
	}
	if open {
		return ErrConflict
	}
	title = strings.TrimSpace(title)
	if title == "" || len(title) > 80 {
		return ErrInvalid
	}
	agents, err := s.ownedAgents(ctx, userID, agentIDs)
	if err != nil {
		return err
	}
	current, err := s.Participants(ctx, userID, th.ID)
	if err != nil {
		return err
	}
	if len(agents) < 2 && len(current) >= 2 {
		return ErrInvalid
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	humans, err := s.groupHumans(ctx, user, memberIDs)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE threads SET title = ? WHERE id = ? AND user_id = ? AND kind = 'group'`, title, th.ID, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM thread_participants WHERE thread_id = ? AND user_id = ? AND workspace_id = ?`, th.ID, userID, th.WorkspaceID); err != nil {
		return err
	}
	for _, agentID := range agents {
		if _, err := tx.ExecContext(ctx, `INSERT INTO thread_participants(thread_id, agent_id, user_id, workspace_id) VALUES (?, ?, ?, ?)`, th.ID, agentID, userID, th.WorkspaceID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM thread_members WHERE thread_id = ? AND user_id = ? AND workspace_id = ?`, th.ID, userID, th.WorkspaceID); err != nil {
		return err
	}
	for _, memberID := range humans {
		if _, err := tx.ExecContext(ctx, `INSERT INTO thread_members(thread_id, member_id, user_id, workspace_id) VALUES (?, ?, ?, ?)`, th.ID, memberID, userID, th.WorkspaceID); err != nil {
			return err
		}
	}
	if err := dropLeftPins(ctx, tx, th.ID, th.WorkspaceID, humans); err != nil {
		return err
	}
	return tx.Commit()
}

func dropLeftPins(ctx context.Context, tx *sql.Tx, threadID, workspaceID string, humans []string) error {
	q := `DELETE FROM group_pins WHERE thread_id = ? AND workspace_id = ? AND user_id NOT IN (` + placeholders(len(humans)) + `)`
	args := make([]any, 0, 2+len(humans))
	args = append(args, threadID, workspaceID)
	for _, id := range humans {
		args = append(args, id)
	}
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return err
	}
	q = `DELETE FROM group_places WHERE thread_id = ? AND workspace_id = ? AND user_id NOT IN (` + placeholders(len(humans)) + `)`
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return err
	}
	return nil
}

func placeholders(n int) string {
	if n <= 0 {
		return "''"
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

func (s *Store) ownedAgents(ctx context.Context, userID string, agentIDs []string) ([]string, error) {
	seen := map[string]bool{}
	var agents []string
	for _, id := range agentIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return nil, ErrInvalid
		}
		if _, err := s.GetAgent(ctx, userID, id); err != nil {
			return nil, err
		}
		seen[id] = true
		agents = append(agents, id)
	}
	return agents, nil
}

func (s *Store) groupHumans(ctx context.Context, user User, memberIDs []string) ([]string, error) {
	humans := []string{user.ID}
	seen := map[string]bool{user.ID: true}
	for _, id := range memberIDs {
		id = strings.TrimSpace(id)
		if id == "" || id == user.ID {
			continue
		}
		if seen[id] {
			return nil, ErrInvalid
		}
		other, err := s.GetUser(ctx, id)
		if err != nil {
			return nil, ErrNotFound
		}
		if other.WorkspaceID != user.WorkspaceID || other.Disabled {
			return nil, ErrNotFound
		}
		seen[id] = true
		humans = append(humans, id)
	}
	return humans, nil
}

// SetGroupTitle renames a group the caller created.
// A run that is still open is ErrConflict and the title stays.
func (s *Store) SetGroupTitle(ctx context.Context, userID, threadID, title string) error {
	th, err := s.creatorGroup(ctx, userID, threadID)
	if err != nil {
		return err
	}
	title = strings.TrimSpace(title)
	if title == "" || len(title) > 80 {
		return ErrInvalid
	}
	open, err := s.GroupOpen(ctx, userID, th.ID)
	if err != nil {
		return err
	}
	if open {
		return ErrConflict
	}
	_, err = s.db.ExecContext(ctx, `UPDATE threads SET title = ? WHERE id = ? AND user_id = ? AND kind = 'group'`, title, th.ID, userID)
	return err
}

func (s *Store) creatorGroup(ctx context.Context, userID, threadID string) (Thread, error) {
	th, err := s.GroupAccess(ctx, userID, threadID)
	if err != nil {
		return Thread{}, err
	}
	if th.UserID != userID {
		return Thread{}, ErrNotFound
	}
	return th, nil
}

// DeleteGroup removes a group the caller created, including its chat,
// runs, pins, and placements. The agents stay. An open reply is ErrConflict.
func (s *Store) DeleteGroup(ctx context.Context, userID, threadID string) error {
	th, err := s.creatorGroup(ctx, userID, threadID)
	if err != nil {
		return err
	}
	open, err := s.GroupOpen(ctx, userID, th.ID)
	if err != nil {
		return err
	}
	if open {
		return ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []string{
		`DELETE FROM run_events WHERE run_id IN (SELECT id FROM runs WHERE user_id = ? AND thread_id = ?)`,
		`DELETE FROM approvals WHERE user_id = ? AND run_id IN (SELECT id FROM runs WHERE user_id = ? AND thread_id = ?)`,
		`DELETE FROM runs WHERE user_id = ? AND thread_id = ?`,
		`DELETE FROM messages WHERE user_id = ? AND thread_id = ?`,
		`DELETE FROM thread_participants WHERE user_id = ? AND thread_id = ?`,
		`DELETE FROM thread_members WHERE user_id = ? AND thread_id = ?`,
		`DELETE FROM group_pins WHERE workspace_id = ? AND thread_id = ?`,
		`DELETE FROM group_places WHERE workspace_id = ? AND thread_id = ?`,
		`DELETE FROM group_confirms WHERE workspace_id = ? AND thread_id = ?`,
	}
	for _, stmt := range stmts {
		args := []any{userID, th.ID}
		if strings.Contains(stmt, "workspace_id") && !strings.Contains(stmt, "approvals") {
			args = []any{th.WorkspaceID, th.ID}
		}
		if strings.HasPrefix(stmt, `DELETE FROM approvals`) {
			args = []any{userID, userID, th.ID}
		}
		if _, err := tx.ExecContext(ctx, stmt, args...); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM threads WHERE id = ? AND user_id = ? AND kind = 'group'`, th.ID, userID)
	if err != nil {
		return err
	}
	if err := rowsAffected(res); err != nil {
		return err
	}
	return tx.Commit()
}

// BeginGroupConfirm replaces any unanswered delete confirmation for this group.
// Only the creator can confirm. The returned id is the one a later POST must send.
func (s *Store) BeginGroupConfirm(ctx context.Context, userID, threadID string) (string, error) {
	th, err := s.creatorGroup(ctx, userID, threadID)
	if err != nil {
		return "", err
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM group_confirms WHERE user_id = ? AND thread_id = ?`, userID, th.ID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO group_confirms(id, workspace_id, user_id, thread_id, created_at) VALUES (?, ?, ?, ?, ?)`, id, th.WorkspaceID, userID, th.ID, s.stamp(s.Now())); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

// ClearGroupConfirm drops an unanswered delete confirmation for this person.
// A newer ask uses this so the older confirm can no longer delete the group.
func (s *Store) ClearGroupConfirm(ctx context.Context, userID, threadID string) error {
	if strings.TrimSpace(threadID) == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM group_confirms WHERE user_id = ? AND thread_id = ?`, userID, threadID)
	return err
}

// TakeGroupConfirm removes a confirmation when the id is the current one.
// A stale id returns false and leaves the group alone.
func (s *Store) TakeGroupConfirm(ctx context.Context, userID, threadID, confirmID string) (bool, error) {
	th, err := s.creatorGroup(ctx, userID, threadID)
	if err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM group_confirms WHERE id = ? AND user_id = ? AND thread_id = ?`, strings.TrimSpace(confirmID), userID, th.ID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// GroupPrompt is the hidden copy that started a group run.
func (s *Store) GroupPrompt(ctx context.Context, userID, runID string) (Message, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM messages WHERE user_id = ? AND run_id = ? AND author_kind = 'user' ORDER BY rowid LIMIT 1`, userID, runID).Scan(&id)
	if err != nil {
		return Message{}, scanErr(err)
	}
	return s.GetMessage(ctx, userID, id)
}

// GroupTurnBefore is the visible person post that a hidden prompt copied.
func (s *Store) GroupTurnBefore(ctx context.Context, userID, threadID string, prompt Message) (Message, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM messages
WHERE user_id = ? AND thread_id = ? AND author_kind = 'user' AND origin = 'web' AND body_md = ?
AND rowid < (SELECT rowid FROM messages WHERE id = ? AND user_id = ?)
ORDER BY rowid DESC LIMIT 1`, userID, threadID, prompt.Body, prompt.ID, userID).Scan(&id)
	if err != nil {
		return Message{}, scanErr(err)
	}
	return s.GetMessage(ctx, userID, id)
}

// hideLookPass marks a look reply that is exactly PASS so it is never shown.
// It runs inside FinishRun's transaction. A normal reply, including one that
// says PASS for another reason, stays visible.
func hideLookPass(ctx context.Context, tx *sql.Tx, userID, runID, msgID, body string) error {
	if !strings.EqualFold(strings.TrimSpace(body), "PASS") {
		return nil
	}
	var origin string
	err := tx.QueryRowContext(ctx, `SELECT origin FROM messages WHERE user_id = ? AND run_id = ? AND author_kind = 'user' ORDER BY rowid LIMIT 1`, userID, runID).Scan(&origin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if origin != "look" {
		return nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE messages SET origin = 'pass' WHERE id = ? AND user_id = ?`, msgID, userID)
	return err
}

// ConcealGroupPass hides a look that replied with exactly PASS.
// The bool is true when the reply is now hidden. promptID is the hidden copy.
func (s *Store) ConcealGroupPass(ctx context.Context, userID, runID string) (bool, string, error) {
	prompt, err := s.GroupPrompt(ctx, userID, runID)
	if err != nil {
		if err == ErrNotFound {
			return false, "", nil
		}
		return false, "", err
	}
	if prompt.Origin != "look" {
		return false, prompt.ID, nil
	}
	var msgID, body string
	err = s.db.QueryRowContext(ctx, `SELECT id, body_md FROM messages WHERE user_id = ? AND run_id = ? AND author_kind = 'agent' ORDER BY rowid DESC LIMIT 1`, userID, runID).Scan(&msgID, &body)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, prompt.ID, nil
		}
		return false, "", err
	}
	if !strings.EqualFold(strings.TrimSpace(body), "PASS") {
		return false, prompt.ID, nil
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE messages SET origin = 'pass' WHERE id = ? AND user_id = ?`, msgID, userID); err != nil {
		return false, "", err
	}
	return true, prompt.ID, nil
}

// GroupTurnAgents reports agents that already posted after humanID,
// and agents that still have an open run on the thread.
func (s *Store) GroupTurnAgents(ctx context.Context, userID, threadID, humanID string) (posted, open map[string]bool, err error) {
	posted = map[string]bool{}
	open = map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT author_id FROM messages
WHERE user_id = ? AND thread_id = ? AND author_kind = 'agent'
AND origin NOT IN ('mention', 'look', 'pass', 'setup', 'relay', 'notice')
AND rowid > (SELECT rowid FROM messages WHERE id = ? AND user_id = ?)`, userID, threadID, humanID, userID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, nil, err
		}
		posted[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	openRows, err := s.db.QueryContext(ctx, `SELECT agent_id FROM runs WHERE user_id = ? AND thread_id = ? AND status IN ('queued', 'streaming', 'waiting_approval')`, userID, threadID)
	if err != nil {
		return nil, nil, err
	}
	defer openRows.Close()
	for openRows.Next() {
		var id string
		if err := openRows.Scan(&id); err != nil {
			return nil, nil, err
		}
		open[id] = true
	}
	return posted, open, openRows.Err()
}

// GroupHasVisibleAfter reports a visible agent post inserted after messageID.
func (s *Store) GroupHasVisibleAfter(ctx context.Context, userID, threadID, messageID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages
WHERE user_id = ? AND thread_id = ? AND author_kind = 'agent'
AND origin NOT IN ('mention', 'look', 'pass', 'setup', 'relay', 'notice')
AND rowid > (SELECT rowid FROM messages WHERE id = ? AND user_id = ?)`, userID, threadID, messageID, userID).Scan(&n)
	return n > 0, err
}

// GroupAsk is one change requested by a post in the group.
type GroupAsk struct {
	Kind    string
	Title   string
	Name    string
	Section string
}

// ParseGroupAsk recognizes one clear request that fills the whole post.
// A post that asks for two changes, or that is not one of the known sentences, is not an ask.
func ParseGroupAsk(body string) (GroupAsk, bool) {
	text := strings.TrimSpace(body)
	if text == "" || strings.ContainsAny(text, "\r\n") {
		return GroupAsk{}, false
	}
	// Two command shapes in one post are an ask that changes nothing.
	if askShapeCount(text) > 1 {
		return GroupAsk{Kind: "many"}, true
	}
	if strings.EqualFold(text, "delete this group") {
		return GroupAsk{Kind: "delete"}, true
	}
	if rest, ok := cutFold(text, "rename this group to "); ok && strings.TrimSpace(rest) != "" {
		return GroupAsk{Kind: "rename", Title: strings.TrimSpace(rest)}, true
	}
	if rest, ok := cutFold(text, "add "); ok {
		if name, ok := cutSuffixFold(rest, " to this group"); ok && strings.TrimSpace(name) != "" {
			return GroupAsk{Kind: "add", Name: strings.TrimSpace(name)}, true
		}
	}
	if rest, ok := cutFold(text, "remove "); ok {
		if name, ok := cutSuffixFold(rest, " from this group"); ok && strings.TrimSpace(name) != "" {
			return GroupAsk{Kind: "remove", Name: strings.TrimSpace(name)}, true
		}
	}
	if rest, ok := cutFold(text, "put this group in "); ok && strings.TrimSpace(rest) != "" {
		return GroupAsk{Kind: "place", Section: strings.TrimSpace(rest)}, true
	}
	return GroupAsk{}, false
}

func askShapeCount(text string) int {
	lower := strings.ToLower(text)
	return strings.Count(lower, "rename this group to ") +
		strings.Count(lower, "put this group in ") +
		strings.Count(lower, "delete this group") +
		strings.Count(lower, " to this group") +
		strings.Count(lower, " from this group")
}

func cutFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}

func cutSuffixFold(s, suffix string) (string, bool) {
	if len(s) < len(suffix) || !strings.EqualFold(s[len(s)-len(suffix):], suffix) {
		return "", false
	}
	return s[:len(s)-len(suffix)], true
}
