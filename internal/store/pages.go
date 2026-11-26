package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/scott/castbot/internal/page"
)

const (
	// PagePrivate is the default. Only the owner and the creating agent can read it.
	PagePrivate = "private"
	// PageWorkspace lets every member of the workspace read it.
	PageWorkspace = "workspace"
	// PageInvited lets the owner and the named people and agents read it.
	PageInvited = "invited"
	// PageLink lets anyone with the token read it.
	PageLink = "link"

	maxPagesPerAgent     = 50
	maxPagesPerWorkspace = 200
)

// Page is one hosted report. Body is empty on list results.
type Page struct {
	ID          string
	WorkspaceID string
	UserID      string
	AgentID     string
	Title       string
	Token       string
	Visibility  string
	Body        string
	Revision    int
	CreatedAt   string
	UpdatedAt   string
}

// PageGrant is one person or agent who can read an invited page.
type PageGrant struct {
	ID        string
	PageID    string
	Kind      string
	GranteeID string
}

const pageSelect = `SELECT id, workspace_id, user_id, agent_id, title, token, visibility, body, revision, created_at, updated_at FROM pages`

const pageListSelect = `SELECT id, workspace_id, user_id, agent_id, title, token, visibility, '', revision, created_at, updated_at FROM pages`

// CreatePage stores revision 1 as a private page for this agent.
func (s *Store) CreatePage(ctx context.Context, userID, agentID, title, body string) (Page, error) {
	title, body, err := cleanPageWrite(title, body, true)
	if err != nil {
		return Page{}, err
	}
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return Page{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Page{}, err
	}
	defer tx.Rollback()
	var agentN, workspaceN int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pages WHERE user_id = ? AND agent_id = ? AND deleted_at IS NULL`, userID, agentID).Scan(&agentN); err != nil {
		return Page{}, err
	}
	if agentN >= maxPagesPerAgent {
		return Page{}, ErrPageCap
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pages WHERE workspace_id = ? AND deleted_at IS NULL`, agent.WorkspaceID).Scan(&workspaceN); err != nil {
		return Page{}, err
	}
	if workspaceN >= maxPagesPerWorkspace {
		return Page{}, ErrPageCap
	}
	id, err := newID()
	if err != nil {
		return Page{}, err
	}
	stamp := s.stamp(s.Now())
	var token string
	for try := 0; try < 3; try++ {
		token, err = newPageToken()
		if err != nil {
			return Page{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO pages(id, workspace_id, user_id, agent_id, title, token, visibility, body, revision, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			id, agent.WorkspaceID, userID, agentID, title, token, PagePrivate, body, stamp, stamp)
		if err == nil {
			break
		}
		if !strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Page{}, err
		}
	}
	if err != nil {
		return Page{}, err
	}
	if err := tx.Commit(); err != nil {
		return Page{}, err
	}
	return s.pageByID(ctx, userID, id)
}

// UpdatePage replaces the body of a page this agent created.
// revision 0 does not check the current revision. A non-zero revision must match.
func (s *Store) UpdatePage(ctx context.Context, userID, agentID, key, body string, revision int) (Page, error) {
	_, body, err := cleanPageWrite("", body, false)
	if err != nil || revision < 0 {
		return Page{}, ErrInvalid
	}
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return Page{}, err
	}
	current, err := s.ownedPage(ctx, userID, agentID, key)
	if err != nil {
		return Page{}, err
	}
	if revision != 0 && current.Revision != revision {
		return Page{}, ErrRevision
	}
	stamp := s.stamp(s.Now())
	res, err := s.db.ExecContext(ctx, `UPDATE pages SET body = ?, revision = revision + 1, updated_at = ? WHERE id = ? AND user_id = ? AND agent_id = ? AND deleted_at IS NULL AND (? = 0 OR revision = ?)`,
		body, stamp, current.ID, userID, agentID, revision, revision)
	if err != nil {
		return Page{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Page{}, err
	}
	if n == 0 {
		again, err := s.ownedPage(ctx, userID, agentID, key)
		if err != nil {
			return Page{}, err
		}
		if revision != 0 && again.Revision != revision {
			return Page{}, ErrRevision
		}
		return Page{}, ErrNotFound
	}
	return s.pageByID(ctx, userID, current.ID)
}

// RenamePage changes the title and leaves the token and the body revision alone.
// An empty agentID is the owner's session. A set agentID must be the creating agent.
func (s *Store) RenamePage(ctx context.Context, userID, agentID, key, title string) (Page, error) {
	title, ok := page.CleanTitle(title)
	if !ok {
		return Page{}, ErrInvalid
	}
	current, err := s.ownedPage(ctx, userID, agentID, key)
	if err != nil {
		return Page{}, err
	}
	stamp := s.stamp(s.Now())
	res, err := s.db.ExecContext(ctx, `UPDATE pages SET title = ?, updated_at = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
		title, stamp, current.ID, userID)
	if err != nil {
		return Page{}, err
	}
	if err := rowsAffected(res); err != nil {
		return Page{}, err
	}
	return s.pageByID(ctx, userID, current.ID)
}

// DeletePage soft-deletes a page. The token is not issued again.
func (s *Store) DeletePage(ctx context.Context, userID, agentID, key string) error {
	current, err := s.ownedPage(ctx, userID, agentID, key)
	if err != nil {
		return err
	}
	stamp := s.stamp(s.Now())
	res, err := s.db.ExecContext(ctx, `UPDATE pages SET deleted_at = ?, updated_at = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
		stamp, stamp, current.ID, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// SharePage replaces who can read a page. Invited requires at least one person or agent in the workspace.
// Leaving invited clears the grant list.
func (s *Store) SharePage(ctx context.Context, userID, agentID, key, visibility string, userIDs, agentIDs []string) (Page, error) {
	visibility = strings.TrimSpace(visibility)
	if !pageVisOK(visibility) {
		return Page{}, ErrInvalid
	}
	userIDs = uniqueIDs(userIDs)
	agentIDs = uniqueIDs(agentIDs)
	current, err := s.ownedPage(ctx, userID, agentID, key)
	if err != nil {
		return Page{}, err
	}
	if visibility == PageInvited {
		userIDs = dropID(userIDs, current.UserID)
		agentIDs = dropID(agentIDs, current.AgentID)
		if len(userIDs)+len(agentIDs) == 0 {
			return Page{}, ErrInvalid
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Page{}, err
	}
	defer tx.Rollback()
	if visibility == PageInvited {
		for _, id := range userIDs {
			var got string
			err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id = ? AND workspace_id = ? AND disabled_at IS NULL`, id, current.WorkspaceID).Scan(&got)
			if errors.Is(err, sql.ErrNoRows) {
				return Page{}, ErrNotFound
			}
			if err != nil {
				return Page{}, err
			}
		}
		for _, id := range agentIDs {
			var got string
			err := tx.QueryRowContext(ctx, `SELECT id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`, id, current.WorkspaceID).Scan(&got)
			if errors.Is(err, sql.ErrNoRows) {
				return Page{}, ErrNotFound
			}
			if err != nil {
				return Page{}, err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM page_grants WHERE page_id = ? AND user_id = ? AND workspace_id = ?`, current.ID, current.UserID, current.WorkspaceID); err != nil {
		return Page{}, err
	}
	stamp := s.stamp(s.Now())
	if visibility == PageInvited {
		for _, id := range userIDs {
			if err := insertPageGrant(ctx, tx, current, "user", id, stamp); err != nil {
				return Page{}, err
			}
		}
		for _, id := range agentIDs {
			if err := insertPageGrant(ctx, tx, current, "agent", id, stamp); err != nil {
				return Page{}, err
			}
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE pages SET visibility = ?, updated_at = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
		visibility, stamp, current.ID, current.UserID)
	if err != nil {
		return Page{}, err
	}
	if err := rowsAffected(res); err != nil {
		return Page{}, err
	}
	if err := tx.Commit(); err != nil {
		return Page{}, err
	}
	return s.pageByID(ctx, userID, current.ID)
}

// ListAgentPages returns pages this agent created, newest first, without bodies.
func (s *Store) ListAgentPages(ctx context.Context, userID, agentID string) ([]Page, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return nil, err
	}
	return s.listPages(ctx, pageListSelect+` WHERE user_id = ? AND agent_id = ? AND deleted_at IS NULL ORDER BY updated_at DESC, id`, userID, agentID)
}

// ListPagesSharedWithAgent returns workspace pages and invited agent grants.
// It does not list every link page, and it skips pages this agent created.
func (s *Store) ListPagesSharedWithAgent(ctx context.Context, userID, agentID string) ([]Page, error) {
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return nil, err
	}
	return s.listPages(ctx, pageListSelect+` p WHERE p.deleted_at IS NULL AND p.workspace_id = ? AND NOT (p.user_id = ? AND p.agent_id = ?) AND (
  p.visibility = ?
  OR EXISTS (
    SELECT 1 FROM page_grants g
    WHERE g.page_id = p.id AND g.workspace_id = p.workspace_id AND g.user_id = p.user_id
      AND g.grantee_kind = 'agent' AND g.grantee_id = ?
  )
) ORDER BY p.updated_at DESC, p.id`, agent.WorkspaceID, userID, agentID, PageWorkspace, agentID)
}

// ListOwnedPages returns every live page this person owns, newest first.
func (s *Store) ListOwnedPages(ctx context.Context, userID string) ([]Page, error) {
	if _, err := s.GetUser(ctx, userID); err != nil {
		return nil, err
	}
	return s.listPages(ctx, pageListSelect+` WHERE user_id = ? AND deleted_at IS NULL ORDER BY updated_at DESC, id`, userID)
}

// ListPagesSharedWithUser returns workspace pages and invited user grants in this workspace.
func (s *Store) ListPagesSharedWithUser(ctx context.Context, userID string) ([]Page, error) {
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.listPages(ctx, pageListSelect+` p WHERE p.deleted_at IS NULL AND p.workspace_id = ? AND p.user_id != ? AND (
  p.visibility = ?
  OR EXISTS (
    SELECT 1 FROM page_grants g
    WHERE g.page_id = p.id AND g.workspace_id = p.workspace_id AND g.user_id = p.user_id
      AND g.grantee_kind = 'user' AND g.grantee_id = ?
  )
) ORDER BY p.updated_at DESC, p.id`, user.WorkspaceID, userID, PageWorkspace, userID)
}

// PageCard is a chat chip for a page. Revision is the revision when the card
// was attached. CurrentRevision is the page now. The row stays after a delete
// so the title can still be shown. Open uses the token and the page route.
type PageCard struct {
	MessageID       string
	PageID          string
	Revision        int
	Title           string
	Token           string
	Visibility      string
	CurrentRevision int
	WorkspaceName   string
}

// AddPageCard records one card on a message the caller owns.
// A later attach of the same page on that message updates the stored revision.
// Readers load cards by workspace and message id, because a group message is
// stored under the thread owner and a member is not that user id.
func (s *Store) AddPageCard(ctx context.Context, userID, messageID, pageID string, revision int) error {
	if revision < 0 || strings.TrimSpace(messageID) == "" || strings.TrimSpace(pageID) == "" {
		return ErrInvalid
	}
	row, err := s.pageByID(ctx, userID, pageID)
	if err != nil {
		return err
	}
	var workspaceID string
	err = s.db.QueryRowContext(ctx, `SELECT workspace_id FROM messages WHERE id = ? AND user_id = ? AND workspace_id = ?`,
		messageID, userID, row.WorkspaceID).Scan(&workspaceID)
	if err != nil {
		return scanErr(err)
	}
	id, err := newID()
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO page_cards(id, workspace_id, user_id, message_id, page_id, revision, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(message_id, page_id) DO UPDATE SET revision = excluded.revision`,
		id, row.WorkspaceID, userID, messageID, pageID, revision, s.stamp(s.Now()))
	return err
}

// PageCardsForMessages returns cards for messages in one workspace.
// It does not require page_cards.user_id to equal the viewer.
func (s *Store) PageCardsForMessages(ctx context.Context, workspaceID string, messageIDs []string) ([]PageCard, error) {
	if strings.TrimSpace(workspaceID) == "" || len(messageIDs) == 0 {
		return nil, nil
	}
	var ids []string
	seen := map[string]bool{}
	for _, id := range messageIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var out []PageCard
	for start := 0; start < len(ids); start += 400 {
		end := start + 400
		if end > len(ids) {
			end = len(ids)
		}
		part, err := s.pageCardsBatch(ctx, workspaceID, ids[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, part...)
	}
	return out, nil
}

func (s *Store) pageCardsBatch(ctx context.Context, workspaceID string, ids []string) ([]PageCard, error) {
	args := make([]any, 0, len(ids)+1)
	args = append(args, workspaceID)
	holders := make([]string, len(ids))
	for i, id := range ids {
		holders[i] = "?"
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c.message_id, c.page_id, c.revision, p.title, p.token, p.visibility, p.revision, COALESCE(w.name, '')
FROM page_cards c
JOIN pages p ON p.id = c.page_id
LEFT JOIN workspaces w ON w.id = p.workspace_id
WHERE c.workspace_id = ? AND c.message_id IN (`+strings.Join(holders, ",")+`)
ORDER BY c.created_at, c.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PageCard
	for rows.Next() {
		var row PageCard
		if err := rows.Scan(&row.MessageID, &row.PageID, &row.Revision, &row.Title, &row.Token, &row.Visibility, &row.CurrentRevision, &row.WorkspaceName); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// AgentPagesTouched returns live pages this agent saved at or after since.
// since is a run start, so an older page is not attached to a later reply.
func (s *Store) AgentPagesTouched(ctx context.Context, userID, agentID, since string) ([]Page, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(since) == "" {
		return nil, nil
	}
	return s.listPages(ctx, pageListSelect+` WHERE user_id = ? AND agent_id = ? AND deleted_at IS NULL AND updated_at >= ? ORDER BY updated_at, id`, userID, agentID, since)
}

// PageGrants returns the invite list for a page the caller owns.
func (s *Store) PageGrants(ctx context.Context, userID, pageID string) ([]PageGrant, error) {
	page, err := s.ownedPage(ctx, userID, "", pageID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, page_id, grantee_kind, grantee_id FROM page_grants WHERE page_id = ? AND user_id = ? AND workspace_id = ? ORDER BY created_at, id`,
		page.ID, page.UserID, page.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PageGrant
	for rows.Next() {
		var g PageGrant
		if err := rows.Scan(&g.ID, &g.PageID, &g.Kind, &g.GranteeID); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// PageForUser reads a page the signed-in person may see.
// key is a page id or a link token. A link page in another workspace requires the token.
func (s *Store) PageForUser(ctx context.Context, userID, key string) (Page, error) {
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return Page{}, err
	}
	row, viaToken, err := s.loadLivePage(ctx, key)
	if err != nil {
		return Page{}, err
	}
	if row.UserID == userID {
		return row, nil
	}
	if row.Visibility == PageLink && viaToken {
		return row, nil
	}
	if row.WorkspaceID != user.WorkspaceID {
		return Page{}, ErrNotFound
	}
	switch row.Visibility {
	case PageWorkspace:
		return row, nil
	case PageInvited:
		ok, err := s.pageGranted(ctx, row, "user", userID)
		if err != nil || !ok {
			if err != nil {
				return Page{}, err
			}
			return Page{}, ErrNotFound
		}
		return row, nil
	default:
		return Page{}, ErrNotFound
	}
}

// PageForAgent reads a page this agent may see.
// The creating agent can read its own page by id. Anyone else needs a matching grant, workspace visibility, or the link token.
func (s *Store) PageForAgent(ctx context.Context, userID, agentID, key string) (Page, error) {
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return Page{}, err
	}
	row, viaToken, err := s.loadLivePage(ctx, key)
	if err != nil {
		return Page{}, err
	}
	if row.UserID == userID && row.AgentID == agentID {
		return row, nil
	}
	if row.Visibility == PageLink && viaToken {
		return row, nil
	}
	if row.WorkspaceID != agent.WorkspaceID {
		return Page{}, ErrNotFound
	}
	switch row.Visibility {
	case PageWorkspace:
		return row, nil
	case PageInvited:
		ok, err := s.pageGranted(ctx, row, "agent", agentID)
		if err != nil || !ok {
			if err != nil {
				return Page{}, err
			}
			return Page{}, ErrNotFound
		}
		return row, nil
	default:
		return Page{}, ErrNotFound
	}
}

// PageRequiresSignIn reports a live page that is not shared by link.
// A missing or deleted token is false, so the caller can answer 404
// without sending a stranger to the login page.
func (s *Store) PageRequiresSignIn(ctx context.Context, token string) (bool, error) {
	if !pageTokenShape(token) {
		return false, nil
	}
	row, _, err := s.loadLivePage(ctx, token)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return row.Visibility != PageLink, nil
}

// PageByLink reads a page that was shared with anyone who has the token.
// The lookup is by token alone because the visitor has no account.
func (s *Store) PageByLink(ctx context.Context, token string) (Page, error) {
	if !pageTokenShape(token) {
		return Page{}, ErrNotFound
	}
	row, _, err := s.loadLivePage(ctx, token)
	if err != nil {
		return Page{}, err
	}
	if row.Visibility != PageLink {
		return Page{}, ErrNotFound
	}
	return row, nil
}

func cleanPageWrite(title, body string, withTitle bool) (string, string, error) {
	if withTitle {
		clean, ok := page.CleanTitle(title)
		if !ok {
			return "", "", ErrInvalid
		}
		title = clean
	}
	body = strings.TrimSpace(body)
	if _, err := page.Parse(body); err != nil {
		return "", "", ErrInvalid
	}
	return title, body, nil
}

func (s *Store) ownedPage(ctx context.Context, userID, agentID, key string) (Page, error) {
	row, _, err := s.loadLivePage(ctx, key)
	if err != nil {
		return Page{}, err
	}
	if row.UserID != userID {
		return Page{}, ErrNotFound
	}
	if agentID != "" && row.AgentID != agentID {
		return Page{}, ErrNotFound
	}
	return row, nil
}

func (s *Store) pageByID(ctx context.Context, userID, id string) (Page, error) {
	var row Page
	err := s.db.QueryRowContext(ctx, pageSelect+` WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, id, userID).Scan(pageScan(&row)...)
	if err != nil {
		return Page{}, scanErr(err)
	}
	return row, nil
}

func (s *Store) loadLivePage(ctx context.Context, key string) (Page, bool, error) {
	if !pageTokenShape(key) && !pageIDShape(key) {
		return Page{}, false, ErrNotFound
	}
	var row Page
	err := s.db.QueryRowContext(ctx, pageSelect+` WHERE deleted_at IS NULL AND (token = ? OR id = ?)`, key, key).Scan(pageScan(&row)...)
	if err != nil {
		return Page{}, false, scanErr(err)
	}
	return row, pageTokenShape(key) && row.Token == key, nil
}

func (s *Store) pageGranted(ctx context.Context, row Page, kind, grantee string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM page_grants WHERE page_id = ? AND workspace_id = ? AND user_id = ? AND grantee_kind = ? AND grantee_id = ?`,
		row.ID, row.WorkspaceID, row.UserID, kind, grantee).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) listPages(ctx context.Context, query string, args ...any) ([]Page, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Page
	for rows.Next() {
		var row Page
		if err := rows.Scan(pageScan(&row)...); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func pageScan(row *Page) []any {
	return []any{&row.ID, &row.WorkspaceID, &row.UserID, &row.AgentID, &row.Title, &row.Token, &row.Visibility, &row.Body, &row.Revision, &row.CreatedAt, &row.UpdatedAt}
}

func insertPageGrant(ctx context.Context, tx *sql.Tx, row Page, kind, grantee, stamp string) error {
	id, err := newID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO page_grants(id, workspace_id, user_id, page_id, grantee_kind, grantee_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, row.WorkspaceID, row.UserID, row.ID, kind, grantee, stamp)
	return err
}

func pageVisOK(v string) bool {
	switch v {
	case PagePrivate, PageWorkspace, PageInvited, PageLink:
		return true
	default:
		return false
	}
}

func pageTokenShape(s string) bool {
	if len(s) != 22 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

func pageIDShape(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

func newPageToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func uniqueIDs(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func dropID(ids []string, skip string) []string {
	var out []string
	for _, id := range ids {
		if id != skip {
			out = append(out, id)
		}
	}
	return out
}
