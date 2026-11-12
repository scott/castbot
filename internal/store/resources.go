package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Section is a sidebar group owned by one user.
type Section struct {
	ID          string
	WorkspaceID string
	UserID      string
	Name        string
	Kind        string
	Position    int
	CreatedAt   string
}

// Agent is a seeded or draft agent owned by one user.
type Agent struct {
	ID          string
	WorkspaceID string
	UserID      string
	SectionID   string
	Name        string
	Title       string
	Description string
	Color       string
	Model       string
	AvatarPath  string
	Status      string
	Activity    string
	Unread      int
	Approvals   int
	IdleTimeout int
	Size        string
	Position    int
	CreatedAt   string
}

// Thread is a conversation owned by one user.
type Thread struct {
	ID            string
	WorkspaceID   string
	UserID        string
	Kind          string
	AgentID       string
	Title         string
	CastSessionID string
	CreatedAt     string
}

// Message is one turn in a thread. UserID is the owner, not the author.
type Message struct {
	ID          string
	WorkspaceID string
	UserID      string
	ThreadID    string
	AuthorKind  string
	AuthorID    string
	Body        string
	Origin      string
	JournalSeq  int
	CreatedAt   string
}

// AgentInput is the writable set of agent fields.
type AgentInput struct {
	SectionID   string
	Name        string
	Title       string
	Description string
	Color       string
	Model       string
	Status      string
	Activity    string
	Unread      int
	Approvals   int
	IdleTimeout int
	Size        string
	CreatedAt   time.Time
	// PendingGreet leaves greeted_at empty so the boot path says hello once.
	// Other creates are marked greeted so an existing agent is not greeted again.
	PendingGreet bool
}

func insertSection(ctx context.Context, tx *sql.Tx, wid, uid, name, kind string, pos int, stamp string) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sections(id, workspace_id, user_id, name, kind, position, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, wid, uid, name, kind, pos, stamp)
	return id, err
}

// ListSections returns the caller's sections in sidebar order.
func (s *Store) ListSections(ctx context.Context, userID string) ([]Section, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, user_id, name, kind, position, created_at FROM sections WHERE user_id = ? ORDER BY position, name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSections(rows)
}

// GetSection returns one of the caller's sections.
func (s *Store) GetSection(ctx context.Context, userID, id string) (Section, error) {
	var sec Section
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, name, kind, position, created_at FROM sections WHERE id = ? AND user_id = ?`, id, userID).
		Scan(&sec.ID, &sec.WorkspaceID, &sec.UserID, &sec.Name, &sec.Kind, &sec.Position, &sec.CreatedAt)
	if err != nil {
		return Section{}, scanErr(err)
	}
	return sec, nil
}

// SystemSection returns the caller's pinned or agents section.
func (s *Store) SystemSection(ctx context.Context, userID, kind string) (Section, error) {
	var sec Section
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, name, kind, position, created_at FROM sections WHERE user_id = ? AND kind = ?`, userID, kind).
		Scan(&sec.ID, &sec.WorkspaceID, &sec.UserID, &sec.Name, &sec.Kind, &sec.Position, &sec.CreatedAt)
	if err != nil {
		return Section{}, scanErr(err)
	}
	return sec, nil
}

// CreateSection adds a custom section for the caller.
func (s *Store) CreateSection(ctx context.Context, userID, name string) (Section, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 40 {
		return Section{}, ErrInvalid
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return Section{}, err
	}
	var pos int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM sections WHERE user_id = ?`, userID).Scan(&pos); err != nil {
		return Section{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Section{}, err
	}
	defer tx.Rollback()
	id, err := insertSection(ctx, tx, user.WorkspaceID, user.ID, name, "custom", pos, s.stamp(s.Now()))
	if err != nil {
		return Section{}, err
	}
	if err := tx.Commit(); err != nil {
		return Section{}, err
	}
	return s.GetSection(ctx, userID, id)
}

// UpdateSection renames a custom section.
func (s *Store) UpdateSection(ctx context.Context, userID, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 40 {
		return ErrInvalid
	}
	sec, err := s.GetSection(ctx, userID, id)
	if err != nil {
		return err
	}
	if sec.Kind != "custom" {
		return ErrProtected
	}
	res, err := s.db.ExecContext(ctx, `UPDATE sections SET name = ? WHERE id = ? AND user_id = ?`, name, id, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// DeleteSection removes a custom section that has no agents.
func (s *Store) DeleteSection(ctx context.Context, userID, id string) error {
	sec, err := s.GetSection(ctx, userID, id)
	if err != nil {
		return err
	}
	if sec.Kind != "custom" {
		return ErrProtected
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE section_id = ? AND user_id = ? AND deleted_at IS NULL`, id, userID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrConflict
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM sections WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

func scanSections(rows *sql.Rows) ([]Section, error) {
	var out []Section
	for rows.Next() {
		var sec Section
		if err := rows.Scan(&sec.ID, &sec.WorkspaceID, &sec.UserID, &sec.Name, &sec.Kind, &sec.Position, &sec.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, sec)
	}
	return out, rows.Err()
}

// ListAgents returns the caller's live agents.
func (s *Store) ListAgents(ctx context.Context, userID string) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, agentSelect+` WHERE a.user_id = ? AND a.deleted_at IS NULL ORDER BY s.position, a.position, a.created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgents(rows)
}

// ListAgentsInSection lists agents in one of the caller's sections.
// A section the caller does not own is ErrNotFound.
func (s *Store) ListAgentsInSection(ctx context.Context, userID, sectionID string) ([]Agent, error) {
	if _, err := s.GetSection(ctx, userID, sectionID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, agentSelect+` WHERE a.user_id = ? AND a.section_id = ? AND a.deleted_at IS NULL ORDER BY a.position, a.created_at`, userID, sectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgents(rows)
}

// GetAgent returns one live agent owned by the caller.
func (s *Store) GetAgent(ctx context.Context, userID, id string) (Agent, error) {
	var a Agent
	err := s.db.QueryRowContext(ctx, agentSelect+` WHERE a.id = ? AND a.user_id = ? AND a.deleted_at IS NULL`, id, userID).Scan(agentScan(&a)...)
	if err != nil {
		return Agent{}, scanErr(err)
	}
	return a, nil
}

const agentSelect = `SELECT a.id, a.workspace_id, a.user_id, a.section_id, a.name, a.title, a.description, a.avatar_color, a.model, a.avatar_path, a.status, a.activity, a.unread, a.approvals, a.idle_timeout, a.size, a.position, a.created_at FROM agents a JOIN sections s ON s.id = a.section_id AND s.user_id = a.user_id`

func agentScan(a *Agent) []any {
	return []any{&a.ID, &a.WorkspaceID, &a.UserID, &a.SectionID, &a.Name, &a.Title, &a.Description, &a.Color, &a.Model, &a.AvatarPath, &a.Status, &a.Activity, &a.Unread, &a.Approvals, &a.IdleTimeout, &a.Size, &a.Position, &a.CreatedAt}
}

func scanAgents(rows *sql.Rows) ([]Agent, error) {
	var out []Agent
	for rows.Next() {
		var a Agent
		if err := rows.Scan(agentScan(&a)...); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CreateAgent inserts an agent and its direct-message thread.
func (s *Store) CreateAgent(ctx context.Context, userID string, in AgentInput) (Agent, Thread, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 40 {
		return Agent{}, Thread{}, ErrInvalid
	}
	sec, err := s.GetSection(ctx, userID, in.SectionID)
	if err != nil {
		return Agent{}, Thread{}, err
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE user_id = ? AND deleted_at IS NULL AND lower(name) = lower(?)`, userID, name).Scan(&n); err != nil {
		return Agent{}, Thread{}, err
	}
	if n > 0 {
		return Agent{}, Thread{}, ErrConflict
	}
	status := in.Status
	if status == "" {
		status = "draft"
	}
	switch status {
	case "draft", "provisioning", "ready", "paused", "failed":
	default:
		return Agent{}, Thread{}, ErrInvalid
	}
	color := strings.TrimSpace(in.Color)
	if color == "" {
		color, err = s.NextAgentColor(ctx, userID)
		if err != nil {
			return Agent{}, Thread{}, err
		}
	} else if !ValidAgentColor(color) {
		return Agent{}, Thread{}, ErrInvalid
	}
	size := in.Size
	if size == "" {
		size = "1:2048"
	}
	idle := in.IdleTimeout
	if idle == 0 {
		idle = 300
	}
	var pos int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM agents WHERE user_id = ? AND section_id = ?`, userID, sec.ID).Scan(&pos); err != nil {
		return Agent{}, Thread{}, err
	}
	created := in.CreatedAt
	if created.IsZero() {
		created = s.Now()
	}
	stamp := s.stamp(created)
	aid, err := newID()
	if err != nil {
		return Agent{}, Thread{}, err
	}
	tid, err := newID()
	if err != nil {
		return Agent{}, Thread{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Agent{}, Thread{}, err
	}
	defer tx.Rollback()
	var greeted any
	if !in.PendingGreet {
		greeted = stamp
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agents(id, workspace_id, user_id, section_id, name, title, description, avatar_color, model, status, activity, unread, approvals, idle_timeout, size, position, created_at, greeted_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		aid, sec.WorkspaceID, userID, sec.ID, name, strings.TrimSpace(in.Title), strings.TrimSpace(in.Description), color, strings.TrimSpace(in.Model), status, strings.TrimSpace(in.Activity), in.Unread, in.Approvals, idle, size, pos, stamp, greeted)
	if err != nil {
		return Agent{}, Thread{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO threads(id, workspace_id, user_id, kind, agent_id, title, created_at) VALUES (?, ?, ?, 'dm', ?, ?, ?)`,
		tid, sec.WorkspaceID, userID, aid, name, stamp)
	if err != nil {
		return Agent{}, Thread{}, err
	}
	if err := tx.Commit(); err != nil {
		return Agent{}, Thread{}, err
	}
	agent, err := s.GetAgent(ctx, userID, aid)
	if err != nil {
		return Agent{}, Thread{}, err
	}
	thread, err := s.GetThread(ctx, userID, tid)
	return agent, thread, err
}

// SetAgentModel stores the caller's chosen model id.
func (s *Store) SetAgentModel(ctx context.Context, userID, agentID, model string) error {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return err
	}
	model = strings.TrimSpace(model)
	if model == "" || len(model) > 200 {
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `UPDATE agents SET model = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, model, agentID, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// UpdateAgent changes the title of one of the caller's agents.
func (s *Store) UpdateAgent(ctx context.Context, userID, id, title string) error {
	if len(title) > 80 {
		return ErrInvalid
	}
	if _, err := s.GetAgent(ctx, userID, id); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE agents SET title = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, strings.TrimSpace(title), id, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// SaveAgentFields updates the form fields of one of the caller's agents.
// The bundle is saved separately. An unknown section is not found.
func (s *Store) SaveAgentFields(ctx context.Context, userID, id string, in AgentInput) error {
	name := strings.TrimSpace(in.Name)
	title := strings.TrimSpace(in.Title)
	model := strings.TrimSpace(in.Model)
	if name == "" || len(name) > 40 || len(title) > 80 || len(model) > 200 {
		return ErrInvalid
	}
	color := strings.TrimSpace(in.Color)
	if color != "" && !ValidAgentColor(color) {
		return ErrInvalid
	}
	if _, err := s.GetAgent(ctx, userID, id); err != nil {
		return err
	}
	if _, err := s.GetSection(ctx, userID, in.SectionID); err != nil {
		return err
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE user_id = ? AND deleted_at IS NULL AND id != ? AND lower(name) = lower(?)`, userID, id, name).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrConflict
	}
	query := `UPDATE agents SET section_id = ?, name = ?, title = ?, model = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`
	args := []any{in.SectionID, name, title, model, id, userID}
	if color != "" {
		query = `UPDATE agents SET section_id = ?, name = ?, title = ?, model = ?, avatar_color = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`
		args = []any{in.SectionID, name, title, model, color, id, userID}
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// PutAvatar stores an image for one of the caller's agents.
// mime is a content type the handler already checked. bytes are the image.
func (s *Store) PutAvatar(ctx context.Context, userID, agentID, mime string, body []byte) error {
	if len(body) == 0 || len(body) > 512<<10 {
		return ErrInvalid
	}
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return ErrInvalid
	}
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_avatars(agent_id, workspace_id, user_id, mime, bytes) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(agent_id) DO UPDATE SET mime = excluded.mime, bytes = excluded.bytes, workspace_id = excluded.workspace_id
WHERE agent_avatars.user_id = excluded.user_id`, agentID, agent.WorkspaceID, userID, mime, body); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE agents SET avatar_path = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, mime, agentID, userID)
	if err != nil {
		return err
	}
	if err := rowsAffected(res); err != nil {
		return err
	}
	return tx.Commit()
}

// GetAvatar returns one of the caller's agent images.
func (s *Store) GetAvatar(ctx context.Context, userID, agentID string) (string, []byte, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return "", nil, err
	}
	var mime string
	var body []byte
	err := s.db.QueryRowContext(ctx, `SELECT mime, bytes FROM agent_avatars WHERE agent_id = ? AND user_id = ?`, agentID, userID).Scan(&mime, &body)
	if err != nil {
		return "", nil, scanErr(err)
	}
	return mime, body, nil
}

// DeleteAgent marks an agent deleted. The row stays so ids are not reused.
func (s *Store) DeleteAgent(ctx context.Context, userID, id string) error {
	if _, err := s.GetAgent(ctx, userID, id); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE agents SET status = 'deleted', deleted_at = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, s.stamp(s.Now()), id, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// ListThreads returns the caller's threads.
func (s *Store) ListThreads(ctx context.Context, userID string) ([]Thread, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, user_id, kind, COALESCE(agent_id, ''), title, cast_session_id, created_at FROM threads WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanThreads(rows)
}

// ListThreadsByAgent lists threads for one of the caller's agents.
func (s *Store) ListThreadsByAgent(ctx context.Context, userID, agentID string) ([]Thread, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, user_id, kind, COALESCE(agent_id, ''), title, cast_session_id, created_at FROM threads WHERE user_id = ? AND agent_id = ? ORDER BY created_at`, userID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanThreads(rows)
}

// GetThread returns one of the caller's threads.
func (s *Store) GetThread(ctx context.Context, userID, id string) (Thread, error) {
	var t Thread
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, kind, COALESCE(agent_id, ''), title, cast_session_id, created_at FROM threads WHERE id = ? AND user_id = ?`, id, userID).
		Scan(&t.ID, &t.WorkspaceID, &t.UserID, &t.Kind, &t.AgentID, &t.Title, &t.CastSessionID, &t.CreatedAt)
	if err != nil {
		return Thread{}, scanErr(err)
	}
	return t, nil
}

// CreateThread adds a thread. AgentID, when set, must be the caller's agent.
func (s *Store) CreateThread(ctx context.Context, userID, kind, agentID, title string) (Thread, error) {
	if kind != "dm" && kind != "group" && kind != "routine" && kind != "cli" {
		return Thread{}, ErrInvalid
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return Thread{}, err
	}
	if agentID != "" {
		if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
			return Thread{}, err
		}
	}
	id, err := newID()
	if err != nil {
		return Thread{}, err
	}
	var agent any
	if agentID != "" {
		agent = agentID
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO threads(id, workspace_id, user_id, kind, agent_id, title, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, user.WorkspaceID, userID, kind, agent, strings.TrimSpace(title), s.stamp(s.Now()))
	if err != nil {
		return Thread{}, err
	}
	return s.GetThread(ctx, userID, id)
}

// UpdateThread renames a thread.
func (s *Store) UpdateThread(ctx context.Context, userID, id, title string) error {
	if len(title) > 120 {
		return ErrInvalid
	}
	if _, err := s.GetThread(ctx, userID, id); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE threads SET title = ? WHERE id = ? AND user_id = ?`, strings.TrimSpace(title), id, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// DeleteThread removes a thread and its messages.
func (s *Store) DeleteThread(ctx context.Context, userID, id string) error {
	if _, err := s.GetThread(ctx, userID, id); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM run_events WHERE run_id IN (SELECT id FROM runs WHERE thread_id = ? AND user_id = ?)`, id, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE thread_id = ? AND user_id = ?`, id, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE thread_id = ? AND user_id = ?`, id, userID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM threads WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if err := rowsAffected(res); err != nil {
		return err
	}
	return tx.Commit()
}

func scanThreads(rows *sql.Rows) ([]Thread, error) {
	var out []Thread
	for rows.Next() {
		var t Thread
		if err := rows.Scan(&t.ID, &t.WorkspaceID, &t.UserID, &t.Kind, &t.AgentID, &t.Title, &t.CastSessionID, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListMessages returns messages in one of the caller's threads.
func (s *Store) ListMessages(ctx context.Context, userID, threadID string) ([]Message, error) {
	if _, err := s.GetThread(ctx, userID, threadID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, user_id, thread_id, author_kind, author_id, body_md, origin, journal_seq, created_at FROM messages WHERE user_id = ? AND thread_id = ? ORDER BY created_at, COALESCE(journal_seq, 0), rowid`, userID, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// GetMessage returns one of the caller's messages.
func (s *Store) GetMessage(ctx context.Context, userID, id string) (Message, error) {
	var m Message
	var seq sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, thread_id, author_kind, author_id, body_md, origin, journal_seq, created_at FROM messages WHERE id = ? AND user_id = ?`, id, userID).
		Scan(&m.ID, &m.WorkspaceID, &m.UserID, &m.ThreadID, &m.AuthorKind, &m.AuthorID, &m.Body, &m.Origin, &seq, &m.CreatedAt)
	if seq.Valid {
		m.JournalSeq = int(seq.Int64)
	}
	if err != nil {
		return Message{}, scanErr(err)
	}
	return m, nil
}

// CreateMessage appends a message to one of the caller's threads.
func (s *Store) CreateMessage(ctx context.Context, userID, threadID, authorKind, authorID, body string) (Message, error) {
	body = strings.TrimSpace(body)
	if body == "" || len(body) > 8000 {
		return Message{}, ErrInvalid
	}
	if authorKind != "user" && authorKind != "agent" && authorKind != "system" {
		return Message{}, ErrInvalid
	}
	th, err := s.GetThread(ctx, userID, threadID)
	if err != nil {
		return Message{}, err
	}
	id, err := newID()
	if err != nil {
		return Message{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO messages(id, workspace_id, user_id, thread_id, author_kind, author_id, body_md, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, th.WorkspaceID, userID, threadID, authorKind, authorID, body, s.stamp(s.Now()))
	if err != nil {
		return Message{}, err
	}
	return s.GetMessage(ctx, userID, id)
}

// UpdateMessage replaces the body of one of the caller's messages.
func (s *Store) UpdateMessage(ctx context.Context, userID, id, body string) error {
	body = strings.TrimSpace(body)
	if body == "" || len(body) > 8000 {
		return ErrInvalid
	}
	if _, err := s.GetMessage(ctx, userID, id); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE messages SET body_md = ? WHERE id = ? AND user_id = ?`, body, id, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// DeleteMessage removes one of the caller's messages.
func (s *Store) DeleteMessage(ctx context.Context, userID, id string) error {
	if _, err := s.GetMessage(ctx, userID, id); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

func scanMessages(rows *sql.Rows) ([]Message, error) {
	var out []Message
	for rows.Next() {
		var m Message
		var seq sql.NullInt64
		if err := rows.Scan(&m.ID, &m.WorkspaceID, &m.UserID, &m.ThreadID, &m.AuthorKind, &m.AuthorID, &m.Body, &m.Origin, &seq, &m.CreatedAt); err != nil {
			return nil, err
		}
		if seq.Valid {
			m.JournalSeq = int(seq.Int64)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
