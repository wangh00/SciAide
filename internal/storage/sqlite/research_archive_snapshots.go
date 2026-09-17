package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/wangh00/SciAide/internal/app/research"
)

func rewriteDiscoverySnapshots(ctx context.Context, tx *sql.Tx, maps *archiveIDMaps, projectID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT query_id,candidate_id,snapshot_json FROM research_query_candidates`)
	if err != nil {
		return err
	}
	type update struct{ query, id, raw string }
	updates := []update{}
	for rows.Next() {
		var item update
		var value research.Candidate
		if err := rows.Scan(&item.query, &item.id, &item.raw); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal([]byte(item.raw), &value); err != nil {
			rows.Close()
			return err
		}
		value.ID, value.ProjectID = item.id, projectID
		for i := range value.Records {
			value.Records[i].ID = maps.get("research_source_records", value.Records[i].ID)
			value.Records[i].ProjectID = projectID
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			rows.Close()
			return err
		}
		item.raw = string(encoded)
		updates = append(updates, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE research_query_candidates SET snapshot_json=? WHERE query_id=? AND candidate_id=?`, item.raw, item.query, item.id); err != nil {
			return err
		}
	}
	return nil
}
