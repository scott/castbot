package store

import (
	"github.com/scott/castbot/internal/bundle"
	"github.com/scott/castbot/internal/castcfg"
)

// migrateV21 rewrites stored mock model names such as "Provider A / model-large"
// onto the manifest id they stood in for. A model that already selects a
// provider is not touched, so an existing agent keeps working.
// The bundle hash is rebuilt with the yaml so a later pull still matches.
func (s *Store) migrateV21() error {
	type modelRow struct {
		user, id, model string
	}
	rows, err := s.db.Query(`SELECT user_id, id, COALESCE(model, '') FROM agents WHERE instr(COALESCE(model, ''), 'Provider ') > 0`)
	if err != nil {
		return err
	}
	var models []modelRow
	for rows.Next() {
		var row modelRow
		if err := rows.Scan(&row.user, &row.id, &row.model); err != nil {
			rows.Close()
			return err
		}
		models = append(models, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	type textRow struct {
		user, id, version, yaml, instructions string
	}
	brows, err := s.db.Query(`SELECT user_id, agent_id, version, agent_yaml, instructions_md FROM agent_bundles WHERE instr(agent_yaml, 'Provider ') > 0`)
	if err != nil {
		return err
	}
	var bundles []textRow
	for brows.Next() {
		var row textRow
		if err := brows.Scan(&row.user, &row.id, &row.version, &row.yaml, &row.instructions); err != nil {
			brows.Close()
			return err
		}
		bundles = append(bundles, row)
	}
	if err := brows.Err(); err != nil {
		brows.Close()
		return err
	}
	brows.Close()

	type keptRow struct {
		user, id, yaml string
	}
	krows, err := s.db.Query(`SELECT user_id, id, kept_yaml FROM agents WHERE instr(COALESCE(kept_yaml, ''), 'Provider ') > 0`)
	if err != nil {
		return err
	}
	var kept []keptRow
	for krows.Next() {
		var row keptRow
		if err := krows.Scan(&row.user, &row.id, &row.yaml); err != nil {
			krows.Close()
			return err
		}
		kept = append(kept, row)
	}
	if err := krows.Err(); err != nil {
		krows.Close()
		return err
	}
	krows.Close()

	type draftRow struct {
		user, id, payload string
	}
	drows, err := s.db.Query(`SELECT user_id, id, payload FROM create_drafts WHERE instr(payload, 'Provider ') > 0`)
	if err != nil {
		return err
	}
	var drafts []draftRow
	for drows.Next() {
		var row draftRow
		if err := drows.Scan(&row.user, &row.id, &row.payload); err != nil {
			drows.Close()
			return err
		}
		drafts = append(drafts, row)
	}
	if err := drows.Err(); err != nil {
		drows.Close()
		return err
	}
	drows.Close()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, row := range models {
		next, ok := castcfg.RewritePlaceholderModel(row.model)
		if !ok || next == row.model {
			continue
		}
		if _, err := tx.Exec(`UPDATE agents SET model = ? WHERE user_id = ? AND id = ? AND model = ?`, next, row.user, row.id, row.model); err != nil {
			return err
		}
	}
	for _, row := range bundles {
		next, ok := castcfg.RewritePlaceholderText(row.yaml)
		if !ok || next == row.yaml || row.instructions == "" {
			continue
		}
		_, sum, err := bundle.Archive(next, row.instructions)
		if err != nil {
			continue
		}
		if _, err := tx.Exec(`UPDATE agent_bundles SET agent_yaml = ?, sha256 = ? WHERE user_id = ? AND agent_id = ? AND version = ? AND agent_yaml = ?`, next, sum, row.user, row.id, row.version, row.yaml); err != nil {
			return err
		}
	}
	for _, row := range kept {
		next, ok := castcfg.RewritePlaceholderText(row.yaml)
		if !ok || next == row.yaml {
			continue
		}
		if _, err := tx.Exec(`UPDATE agents SET kept_yaml = ? WHERE user_id = ? AND id = ? AND kept_yaml = ?`, next, row.user, row.id, row.yaml); err != nil {
			return err
		}
	}
	for _, row := range drafts {
		next, ok := castcfg.RewritePlaceholderText(row.payload)
		if !ok || next == row.payload {
			continue
		}
		if _, err := tx.Exec(`UPDATE create_drafts SET payload = ? WHERE user_id = ? AND id = ? AND payload = ?`, next, row.user, row.id, row.payload); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version) VALUES (21) ON CONFLICT(version) DO NOTHING`); err != nil {
		return err
	}
	return tx.Commit()
}
