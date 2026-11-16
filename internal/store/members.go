package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// InviteWindow is how long an invite link stays valid.
const InviteWindow = 7 * 24 * time.Hour

// Invite is a pending invitation into a workspace.
// The plain link is shown once at creation and is not stored.
type Invite struct {
	ID          string
	WorkspaceID string
	UserID      string
	Email       string
	DisplayName string
	ExpiresAt   string
	CreatedAt   string
}

// migrateV29 adds workspace invites and the humans on a group thread.
// Agent participants stay on thread_participants. A group thread is still
// owned by the person who created it. thread_members grants the other people
// in that workspace a way to read and post.
func (s *Store) migrateV29() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS invites (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  email TEXT NOT NULL,
  display_name TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  expires_at TEXT NOT NULL,
  accepted_user_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_invites_user ON invites(user_id, created_at)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_invites_pending ON invites(workspace_id, email) WHERE accepted_user_id = ''`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS thread_members (
  thread_id TEXT NOT NULL,
  member_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  PRIMARY KEY (thread_id, member_id)
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_thread_members_member ON thread_members(member_id, workspace_id)`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (29) ON CONFLICT(version) DO NOTHING`)
	return err
}

func newInviteToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "cbi_" + hex.EncodeToString(buf), nil
}

// CreateInvite starts or refreshes a pending invite.
// The caller must be the workspace owner. A member is ErrNotFound.
// An email that already has an account is ErrConflict.
// The returned token is the only copy of the link secret.
func (s *Store) CreateInvite(ctx context.Context, ownerID, email, name string) (Invite, string, error) {
	email = normalizeEmail(email)
	name = strings.TrimSpace(name)
	if email == "" || !strings.Contains(email, "@") || len(email) > 254 || name == "" || len(name) > 80 {
		return Invite{}, "", ErrInvalid
	}
	owner, err := s.GetUser(ctx, ownerID)
	if err != nil {
		return Invite{}, "", err
	}
	if owner.Role != "owner" || owner.Disabled {
		return Invite{}, "", ErrNotFound
	}
	var existing string
	err = s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, email).Scan(&existing)
	if err == nil {
		return Invite{}, "", ErrConflict
	}
	if err != sql.ErrNoRows {
		return Invite{}, "", err
	}
	plain, err := newInviteToken()
	if err != nil {
		return Invite{}, "", err
	}
	hash := tokenHash(plain)
	now := s.Now()
	stamp := s.stamp(now)
	exp := s.stamp(now.Add(InviteWindow))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Invite{}, "", err
	}
	defer tx.Rollback()
	var pending string
	err = tx.QueryRowContext(ctx, `SELECT id FROM invites WHERE user_id = ? AND workspace_id = ? AND email = ? AND accepted_user_id = ''`, owner.ID, owner.WorkspaceID, email).Scan(&pending)
	if err == sql.ErrNoRows {
		id, err := newID()
		if err != nil {
			return Invite{}, "", err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO invites(id, workspace_id, user_id, email, display_name, token_hash, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, owner.WorkspaceID, owner.ID, email, name, hash, exp, stamp); err != nil {
			return Invite{}, "", err
		}
	} else if err != nil {
		return Invite{}, "", err
	} else {
		res, err := tx.ExecContext(ctx, `UPDATE invites SET display_name = ?, token_hash = ?, expires_at = ?, created_at = ? WHERE id = ? AND user_id = ? AND accepted_user_id = ''`,
			name, hash, exp, stamp, pending, owner.ID)
		if err != nil {
			return Invite{}, "", err
		}
		if err := rowsAffected(res); err != nil {
			return Invite{}, "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return Invite{}, "", err
	}
	row, err := s.inviteByEmail(ctx, owner.ID, email)
	if err != nil {
		return Invite{}, "", err
	}
	return row, plain, nil
}

func (s *Store) inviteByEmail(ctx context.Context, ownerID, email string) (Invite, error) {
	var row Invite
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, email, display_name, expires_at, created_at FROM invites WHERE user_id = ? AND email = ? AND accepted_user_id = ''`, ownerID, email).
		Scan(&row.ID, &row.WorkspaceID, &row.UserID, &row.Email, &row.DisplayName, &row.ExpiresAt, &row.CreatedAt)
	if err != nil {
		return Invite{}, scanErr(err)
	}
	return row, nil
}

// ListInvites returns the caller's pending invites. Accepted rows stay hidden.
func (s *Store) ListInvites(ctx context.Context, userID string) ([]Invite, error) {
	if _, err := s.GetUser(ctx, userID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, user_id, email, display_name, expires_at, created_at FROM invites WHERE user_id = ? AND accepted_user_id = '' ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		var row Invite
		if err := rows.Scan(&row.ID, &row.WorkspaceID, &row.UserID, &row.Email, &row.DisplayName, &row.ExpiresAt, &row.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// RevokeInvite deletes one of the caller's pending invites.
func (s *Store) RevokeInvite(ctx context.Context, userID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM invites WHERE id = ? AND user_id = ? AND accepted_user_id = ''`, id, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// PeekInvite loads a pending, unexpired invite by its plain token.
// The invitee has no account yet, so this lookup is not scoped by a user id.
func (s *Store) PeekInvite(ctx context.Context, token string) (Invite, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, "cbi_") || len(token) < 20 {
		return Invite{}, ErrNotFound
	}
	var row Invite
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, email, display_name, expires_at, created_at FROM invites WHERE token_hash = ? AND accepted_user_id = '' AND expires_at > ?`,
		tokenHash(token), s.stamp(s.Now())).Scan(&row.ID, &row.WorkspaceID, &row.UserID, &row.Email, &row.DisplayName, &row.ExpiresAt, &row.CreatedAt)
	if err != nil {
		return Invite{}, scanErr(err)
	}
	return row, nil
}

// AcceptInvite creates a member in the inviter's workspace and consumes the token.
// passwordHash and totp are stored as given. A used or expired token is ErrNotFound.
func (s *Store) AcceptInvite(ctx context.Context, token, passwordHash, totp string) (User, error) {
	if passwordHash == "" || totp == "" {
		return User{}, ErrInvalid
	}
	row, err := s.PeekInvite(ctx, token)
	if err != nil {
		return User{}, err
	}
	owner, err := s.GetUser(ctx, row.UserID)
	if err != nil {
		return User{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var taken string
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, row.Email).Scan(&taken)
	if err == nil {
		return User{}, ErrConflict
	}
	if err != sql.ErrNoRows {
		return User{}, err
	}
	uid, err := newID()
	if err != nil {
		return User{}, err
	}
	stamp := s.stamp(s.Now())
	res, err := tx.ExecContext(ctx, `UPDATE invites SET accepted_user_id = ? WHERE id = ? AND user_id = ? AND accepted_user_id = '' AND expires_at > ?`,
		uid, row.ID, row.UserID, stamp)
	if err != nil {
		return User{}, err
	}
	if err := rowsAffected(res); err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO users(id, workspace_id, email, display_name, role, theme, timezone, password_changed_at, created_at) VALUES (?, ?, ?, ?, 'member', 'system', ?, ?, ?)`,
		uid, owner.WorkspaceID, row.Email, row.DisplayName, owner.Timezone, stamp, stamp); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return User{}, ErrConflict
		}
		return User{}, err
	}
	for _, kind := range []struct{ k, data string }{{"password", passwordHash}, {"totp", totp}} {
		cid, err := newID()
		if err != nil {
			return User{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO credentials(id, workspace_id, user_id, kind, data, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			cid, owner.WorkspaceID, uid, kind.k, kind.data, stamp); err != nil {
			return User{}, err
		}
	}
	if _, err := insertSection(ctx, tx, owner.WorkspaceID, uid, "Pinned", "pinned", 0, stamp); err != nil {
		return User{}, err
	}
	if _, err := insertSection(ctx, tx, owner.WorkspaceID, uid, "Agents", "agents", 1, stamp); err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_quotas(user_id, workspace_id, max_agents) VALUES (?, ?, ?)`, uid, owner.WorkspaceID, DefaultMaxAgents); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return s.GetUser(ctx, uid)
}

// ListMembers returns the people in the caller's workspace.
func (s *Store) ListMembers(ctx context.Context, userID string) ([]User, error) {
	caller, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT u.id, u.workspace_id, w.name, u.email, u.display_name, u.role, u.theme, u.timezone, u.password_changed_at, u.created_at, u.disabled_at
		FROM users u JOIN workspaces w ON w.id = u.workspace_id
		WHERE u.workspace_id = ? AND u.disabled_at IS NULL
		ORDER BY u.created_at`, caller.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var disabled sql.NullString
		if err := rows.Scan(&u.ID, &u.WorkspaceID, &u.WorkspaceName, &u.Email, &u.DisplayName, &u.Role, &u.Theme, &u.Timezone, &u.PasswordChangedAt, &u.CreatedAt, &disabled); err != nil {
			return nil, err
		}
		u.Disabled = disabled.Valid && disabled.String != ""
		out = append(out, u)
	}
	return out, rows.Err()
}

// CreateGroup opens a group thread. Agents must belong to the caller.
// memberIDs are other people in the same workspace. The caller is always a member.
func (s *Store) CreateGroup(ctx context.Context, userID, title string, agentIDs, memberIDs []string) (Thread, error) {
	title = strings.TrimSpace(title)
	if title == "" || len(title) > 80 {
		return Thread{}, ErrInvalid
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return Thread{}, err
	}
	if user.Disabled {
		return Thread{}, ErrNotFound
	}
	seenAgent := map[string]bool{}
	var agents []string
	for _, id := range agentIDs {
		id = strings.TrimSpace(id)
		if id == "" || seenAgent[id] {
			return Thread{}, ErrInvalid
		}
		if _, err := s.GetAgent(ctx, userID, id); err != nil {
			return Thread{}, err
		}
		seenAgent[id] = true
		agents = append(agents, id)
	}
	if len(agents) < 2 {
		return Thread{}, ErrInvalid
	}
	humans := []string{userID}
	seenHuman := map[string]bool{userID: true}
	for _, id := range memberIDs {
		id = strings.TrimSpace(id)
		if id == "" || id == userID {
			continue
		}
		if seenHuman[id] {
			return Thread{}, ErrInvalid
		}
		other, err := s.GetUser(ctx, id)
		if err != nil {
			return Thread{}, ErrNotFound
		}
		if other.WorkspaceID != user.WorkspaceID || other.Disabled {
			return Thread{}, ErrNotFound
		}
		seenHuman[id] = true
		humans = append(humans, id)
	}
	id, err := newID()
	if err != nil {
		return Thread{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Thread{}, err
	}
	defer tx.Rollback()
	stamp := s.stamp(s.Now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO threads(id, workspace_id, user_id, kind, agent_id, title, job_id, created_at) VALUES (?, ?, ?, 'group', NULL, ?, '', ?)`,
		id, user.WorkspaceID, userID, title, stamp); err != nil {
		return Thread{}, err
	}
	for _, agentID := range agents {
		if _, err := tx.ExecContext(ctx, `INSERT INTO thread_participants(thread_id, agent_id, user_id, workspace_id) VALUES (?, ?, ?, ?)`,
			id, agentID, userID, user.WorkspaceID); err != nil {
			return Thread{}, err
		}
	}
	for _, memberID := range humans {
		if _, err := tx.ExecContext(ctx, `INSERT INTO thread_members(thread_id, member_id, user_id, workspace_id) VALUES (?, ?, ?, ?)`,
			id, memberID, userID, user.WorkspaceID); err != nil {
			return Thread{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Thread{}, err
	}
	return s.GetThread(ctx, userID, id)
}

// ListGroups returns group threads the caller can open, newest first.
func (s *Store) ListGroups(ctx context.Context, userID string) ([]Thread, error) {
	if _, err := s.GetUser(ctx, userID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t.id, t.workspace_id, t.user_id, t.kind, COALESCE(t.agent_id, ''), t.title, t.cast_session_id, t.created_at
FROM threads t
JOIN thread_members m ON m.thread_id = t.id AND m.member_id = ? AND m.workspace_id = t.workspace_id
JOIN users u ON u.id = ? AND u.workspace_id = t.workspace_id AND u.disabled_at IS NULL
WHERE t.kind = 'group'
ORDER BY t.created_at DESC`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanThreads(rows)
}

// GroupAccess returns a group thread the caller may read.
// Another workspace, a private thread, and a non-member are ErrNotFound.
// The returned thread is owned by its creator. Later writes use that id.
func (s *Store) GroupAccess(ctx context.Context, userID, threadID string) (Thread, error) {
	if strings.TrimSpace(threadID) == "" {
		return Thread{}, ErrNotFound
	}
	var ownerID string
	err := s.db.QueryRowContext(ctx, `SELECT t.user_id FROM threads t
JOIN thread_members m ON m.thread_id = t.id AND m.member_id = ? AND m.workspace_id = t.workspace_id
JOIN users u ON u.id = ? AND u.workspace_id = t.workspace_id AND u.disabled_at IS NULL
WHERE t.id = ? AND t.kind = 'group'`, userID, userID, threadID).Scan(&ownerID)
	if err != nil {
		return Thread{}, scanErr(err)
	}
	return s.GetThread(ctx, ownerID, threadID)
}

// GroupAgents lists the agents on a group the caller can open.
func (s *Store) GroupAgents(ctx context.Context, userID, threadID string) ([]Agent, error) {
	th, err := s.GroupAccess(ctx, userID, threadID)
	if err != nil {
		return nil, err
	}
	ids, err := s.Participants(ctx, th.UserID, th.ID)
	if err != nil {
		return nil, err
	}
	var out []Agent
	for _, id := range ids {
		agent, err := s.GetAgent(ctx, th.UserID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, agent)
	}
	return out, nil
}

// GroupHumans lists the people on a group the caller can open.
func (s *Store) GroupHumans(ctx context.Context, userID, threadID string) ([]User, error) {
	th, err := s.GroupAccess(ctx, userID, threadID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT member_id FROM thread_members WHERE thread_id = ? AND user_id = ? AND workspace_id = ? ORDER BY member_id`, th.ID, th.UserID, th.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []User
	for _, id := range ids {
		u, err := s.GetUser(ctx, id)
		if err != nil {
			return nil, err
		}
		if u.WorkspaceID != th.WorkspaceID {
			return nil, ErrNotFound
		}
		out = append(out, u)
	}
	return out, nil
}

// CreateGroupMessage stores a human post on a shared group.
// The row's user_id is the thread owner. AuthorID is the person who posted.
func (s *Store) CreateGroupMessage(ctx context.Context, userID, threadID, body string) (Message, error) {
	body = strings.TrimSpace(body)
	if body == "" || len(body) > 8000 {
		return Message{}, ErrInvalid
	}
	th, err := s.GroupAccess(ctx, userID, threadID)
	if err != nil {
		return Message{}, err
	}
	id, err := newID()
	if err != nil {
		return Message{}, err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO messages(id, workspace_id, user_id, thread_id, author_kind, author_id, body_md, created_at) VALUES (?, ?, ?, ?, 'user', ?, ?, ?)`,
		id, th.WorkspaceID, th.UserID, th.ID, userID, body, s.stamp(s.Now())); err != nil {
		return Message{}, err
	}
	return s.GetMessage(ctx, th.UserID, id)
}

// GroupOpen reports whether the group still has a reply in progress.
// userID is the caller. The count uses the thread owner's rows.
func (s *Store) GroupOpen(ctx context.Context, userID, threadID string) (bool, error) {
	th, err := s.GroupAccess(ctx, userID, threadID)
	if err != nil {
		return false, err
	}
	var n int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE user_id = ? AND thread_id = ? AND status IN ('queued', 'streaming', 'waiting_approval')`, th.UserID, th.ID).Scan(&n)
	return n > 0, err
}

// MentionedAgents returns participants named with @Name, in the order written.
// A longer name wins over a shorter one that shares the same span.
func MentionedAgents(agents []Agent, body string) []Agent {
	type hit struct {
		agent Agent
		at    int
	}
	lower := strings.ToLower(body)
	order := append([]Agent(nil), agents...)
	for i := 1; i < len(order); i++ {
		j := i
		for j > 0 && len(strings.TrimSpace(order[j].Name)) > len(strings.TrimSpace(order[j-1].Name)) {
			order[j], order[j-1] = order[j-1], order[j]
			j--
		}
	}
	var taken [][2]int
	var hits []hit
	used := map[string]bool{}
	for _, agent := range order {
		name := strings.ToLower(strings.TrimSpace(agent.Name))
		if name == "" || used[agent.ID] {
			continue
		}
		needle := "@" + name
		from := 0
		for from < len(lower) {
			at := strings.Index(lower[from:], needle)
			if at < 0 {
				break
			}
			pos := from + at
			end := pos + len(needle)
			before := mentionBefore(lower, pos)
			after := mentionAfter(lower, end)
			overlap := false
			for _, span := range taken {
				if pos < span[1] && end > span[0] {
					overlap = true
					break
				}
			}
			if before && after && !overlap {
				hits = append(hits, hit{agent: agent, at: pos})
				taken = append(taken, [2]int{pos, end})
				used[agent.ID] = true
				break
			}
			from = pos + 1
		}
	}
	for i := 1; i < len(hits); i++ {
		j := i
		for j > 0 && hits[j].at < hits[j-1].at {
			hits[j], hits[j-1] = hits[j-1], hits[j]
			j--
		}
	}
	out := make([]Agent, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.agent)
	}
	return out
}

func mentionRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '\'' || r == '-'
}

func mentionBefore(s string, pos int) bool {
	if pos == 0 {
		return true
	}
	r, size := utf8.DecodeLastRuneInString(s[:pos])
	if r == utf8.RuneError && size == 1 {
		return true
	}
	return !mentionRune(r)
}

func mentionAfter(s string, end int) bool {
	if end >= len(s) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(s[end:])
	return !mentionRune(r)
}
