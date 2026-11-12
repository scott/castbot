package store

import (
	"context"
	"strings"
)

// paletteKeys is the pickable set. c0 is the legacy default and is not assigned.
var paletteKeys = []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9"}

// PaletteKeys returns the pickable color keys in stable order.
func PaletteKeys() []string {
	out := make([]string, len(paletteKeys))
	copy(out, paletteKeys)
	return out
}

// ValidAgentColor reports whether key is a pickable palette key.
// c0, empty, and raw hex values are not valid.
func ValidAgentColor(key string) bool {
	for _, item := range paletteKeys {
		if item == key {
			return true
		}
	}
	return false
}

// pickColor chooses an unused key, then the least-used key.
// Ties keep palette order, so the choice is stable.
func pickColor(counts map[string]int) string {
	best := paletteKeys[0]
	bestN := counts[best]
	for _, key := range paletteKeys[1:] {
		if n := counts[key]; n < bestN {
			best = key
			bestN = n
		}
	}
	return best
}

// NextAgentColor is the auto-pick for one user's next agent.
// Deleted agents do not hold a color. A key is reused only after every
// pickable key is already in use, and then the least-used key wins.
func (s *Store) NextAgentColor(ctx context.Context, userID string) (string, error) {
	counts, err := s.colorCounts(ctx, userID)
	if err != nil {
		return "", err
	}
	return pickColor(counts), nil
}

func (s *Store) colorCounts(ctx context.Context, userID string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT avatar_color, COUNT(*) FROM agents WHERE user_id = ? AND deleted_at IS NULL GROUP BY avatar_color`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var color string
		var n int
		if err := rows.Scan(&color, &n); err != nil {
			return nil, err
		}
		counts[strings.TrimSpace(color)] += n
	}
	return counts, rows.Err()
}

// SetAgentColor stores a pickable key on one of the caller's agents.
// An unknown key, c0, or a hex value is rejected and the row is unchanged.
func (s *Store) SetAgentColor(ctx context.Context, userID, id, color string) error {
	if !ValidAgentColor(strings.TrimSpace(color)) {
		return ErrInvalid
	}
	if _, err := s.GetAgent(ctx, userID, id); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE agents SET avatar_color = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, strings.TrimSpace(color), id, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// migrateV20 assigns a pickable color to agents still on the legacy default.
// A color that is already pickable is left alone, including on a second open.
// The update touches avatar_color only.
func (s *Store) migrateV20() error {
	rows, err := s.db.Query(`SELECT user_id, id, COALESCE(avatar_color, ''), CASE WHEN deleted_at IS NULL THEN 0 ELSE 1 END FROM agents ORDER BY user_id, created_at, id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type colorAgent struct {
		id, color string
		deleted   bool
	}
	byUser := map[string][]colorAgent{}
	var order []string
	for rows.Next() {
		var user, id, color string
		var deleted int
		if err := rows.Scan(&user, &id, &color, &deleted); err != nil {
			return err
		}
		if _, ok := byUser[user]; !ok {
			order = append(order, user)
		}
		byUser[user] = append(byUser[user], colorAgent{id: id, color: strings.TrimSpace(color), deleted: deleted != 0})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, user := range order {
		counts := map[string]int{}
		for _, row := range byUser[user] {
			if !row.deleted && ValidAgentColor(row.color) {
				counts[row.color]++
			}
		}
		for _, row := range byUser[user] {
			if row.color != "" && row.color != "c0" {
				continue
			}
			next := pickColor(counts)
			if _, err := tx.Exec(`UPDATE agents SET avatar_color = ? WHERE id = ? AND user_id = ? AND (avatar_color IS NULL OR TRIM(avatar_color) = '' OR avatar_color = 'c0')`, next, row.id, user); err != nil {
				return err
			}
			if !row.deleted {
				counts[next]++
			}
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version) VALUES (20) ON CONFLICT(version) DO NOTHING`); err != nil {
		return err
	}
	return tx.Commit()
}
