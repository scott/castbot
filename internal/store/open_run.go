package store

import (
	"context"
	"database/sql"
)

// OpenRun is a queued, streaming, or waiting run, plus enough to label it.
type OpenRun struct {
	Run
	HasText     bool
	ToolPayload string
}

// ListOpenRuns returns the caller's runs that have not finished, oldest first.
func (s *Store) ListOpenRuns(ctx context.Context, userID string) ([]OpenRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id, r.workspace_id, r.thread_id, r.agent_id, r.user_id, r.routine_run_id, r.status, r.stream_offset, r.started_at, r.finished_at, r.error,
  EXISTS(SELECT 1 FROM run_events e WHERE e.run_id = r.id AND e.kind = 'text'),
  COALESCE((SELECT e.payload_json FROM run_events e WHERE e.run_id = r.id AND e.kind = 'tool' ORDER BY e.seq DESC LIMIT 1), '')
FROM runs r
WHERE r.user_id = ? AND r.status IN ('queued', 'streaming', 'waiting_approval')
ORDER BY r.started_at, r.rowid`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OpenRun
	for rows.Next() {
		var run OpenRun
		var routine, finished sql.NullString
		var has int
		if err := rows.Scan(&run.ID, &run.WorkspaceID, &run.ThreadID, &run.AgentID, &run.UserID, &routine, &run.Status, &run.StreamOffset, &run.StartedAt, &finished, &run.Error, &has, &run.ToolPayload); err != nil {
			return nil, err
		}
		run.RoutineRunID = routine.String
		run.FinishedAt = finished.String
		run.HasText = has != 0
		out = append(out, run)
	}
	return out, rows.Err()
}
