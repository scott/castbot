package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/scott/castbot/internal/turn"
)

// migrateV37 stores the progress lines that led up to an agent reply.
// body_md stays the full reply. The chat shows those lines as a step log
// and keeps the last stretch as the message.
func (s *Store) migrateV37() error {
	if err := s.ensureColumn("messages", "steps_md", `ALTER TABLE messages ADD COLUMN steps_md TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (37) ON CONFLICT(version) DO NOTHING`)
	return err
}

func textChunksTx(ctx context.Context, tx *sql.Tx, runID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT payload_json FROM run_events WHERE run_id = ? AND kind = 'text' ORDER BY seq`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var body struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(payload), &body) != nil || body.Text == "" {
			continue
		}
		out = append(out, body.Text)
	}
	return out, rows.Err()
}

// stepsJSON is the progress prefix when it matches body exactly.
// A reply that is one turn stores nothing.
func stepsJSON(chunks []string, body string) string {
	if len(chunks) == 0 || body == "" {
		return ""
	}
	beats, answer := turn.Split(chunks)
	if len(beats) == 0 || strings.Join(beats, "")+answer != body {
		return ""
	}
	raw, err := json.Marshal(beats)
	if err != nil {
		return ""
	}
	return string(raw)
}
