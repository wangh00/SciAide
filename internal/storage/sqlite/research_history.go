package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	appresearch "github.com/wangh00/SciAide/internal/app/research"
	"strings"
)

func queryHistoryMutable(ctx context.Context, tx *sql.Tx, projectID, queryID string) error {
	var exists, active bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM research_queries WHERE id=? AND project_id=?)`, queryID, projectID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("query does not belong to project or was already deleted")
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM research_query_origins o JOIN workflow_runs run ON run.research_task_id=o.task_id WHERE o.query_id=? AND run.project_id=? AND run.status IN ('running','queued','waiting_approval','waiting_human_confirmation','paused','interrupted','failed'))`, queryID, projectID).Scan(&active); err != nil {
		return err
	}
	if active {
		return fmt.Errorf("所选记录关联的科研任务仍可继续，请先结束该任务再删除")
	}
	return nil
}

func (r *ResearchRepository) DeleteQueryCandidates(ctx context.Context, c appresearch.DeleteCandidatesCommand) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := queryHistoryMutable(ctx, tx, c.ProjectID, c.QueryID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range c.CandidateIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM research_candidate_records cr JOIN research_query_records qr ON qr.source_record_id=cr.source_record_id JOIN research_candidates c ON c.id=cr.candidate_id WHERE qr.query_id=? AND c.id=? AND c.project_id=?)`, c.QueryID, id, c.ProjectID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("candidate does not belong to selected query")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM research_query_records WHERE query_id=? AND source_record_id IN(SELECT source_record_id FROM research_candidate_records WHERE candidate_id=?)`, c.QueryID, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM research_query_candidates WHERE query_id=? AND candidate_id=?`, c.QueryID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func historyOriginFilter(origin string) (string, []any) {
	if origin == "" || origin == "all" {
		return "", nil
	}
	if origin == "manual" {
		return ` AND COALESCE(o.task_id,'')=''`, nil
	}
	return ` AND o.task_id=?`, []any{origin}
}

func (r *ResearchRepository) QueryOrigins(ctx context.Context, projectID string) ([]appresearch.QueryOrigin, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT COALESCE(o.task_id,'manual'),COALESCE(MAX(o.task_title),'手动检索 / 未关联任务'),COUNT(*),COALESCE(MAX(t.created_at),'') FROM research_queries q LEFT JOIN research_query_origins o ON o.query_id=q.id LEFT JOIN research_tasks t ON t.id=o.task_id AND t.project_id=q.project_id WHERE q.project_id=? GROUP BY COALESCE(o.task_id,'manual') ORDER BY MAX(q.created_at) DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []appresearch.QueryOrigin{{ID: "all", Title: "项目全部任务"}}
	for rows.Next() {
		var value appresearch.QueryOrigin
		if err := rows.Scan(&value.ID, &value.Title, &value.Count, &value.CreatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
		values[0].Count += value.Count
	}
	return values, rows.Err()
}

func (r *ResearchRepository) QueryHistory(ctx context.Context, c appresearch.QueryPageCommand) ([]appresearch.Query, error) {
	filter, args := historyOriginFilter(c.Origin)
	rows, err := r.db.QueryContext(ctx, `SELECT q.id,q.project_id,q.query_text,q.source_ids_json,q.limit_per_source,q.source_statuses_json,q.partial,q.created_at,q.updated_at,(SELECT COUNT(*) FROM research_query_records qr WHERE qr.query_id=q.id),COALESCE(o.task_id,''),COALESCE(o.task_title,''),(o.task_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM research_tasks t WHERE t.id=o.task_id AND t.project_id=q.project_id AND t.archived_at IS NULL)),q.snapshot_version=0 FROM research_queries q LEFT JOIN research_query_origins o ON o.query_id=q.id WHERE q.project_id=?`+filter+` ORDER BY q.created_at DESC,q.id LIMIT ? OFFSET ?`, append([]any{c.ProjectID}, append(args, c.Limit, c.Offset)...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []appresearch.Query{}
	for rows.Next() {
		var value appresearch.Query
		proxy := &queryOriginScanner{row: rows, taskID: &value.ResearchTaskID, title: &value.ResearchTaskTitle, deleted: &value.TaskDeleted, legacy: &value.LegacySnapshot}
		q, err := scanResearchQuery(proxy)
		if err != nil {
			return nil, err
		}
		q.ResearchTaskID, q.ResearchTaskTitle, q.TaskDeleted, q.LegacySnapshot = value.ResearchTaskID, value.ResearchTaskTitle, value.TaskDeleted, value.LegacySnapshot
		values = append(values, q)
	}
	return values, rows.Err()
}

type queryOriginScanner struct {
	row             rowScanner
	taskID, title   *string
	deleted, legacy *bool
}

func (s *queryOriginScanner) Scan(args ...any) error {
	return s.row.Scan(append(args, s.taskID, s.title, s.deleted, s.legacy)...)
}

func (r *ResearchRepository) DeleteQueryHistory(ctx context.Context, c appresearch.DeleteQueriesCommand) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ids := []string{}
	seen := map[string]bool{}
	if c.Origin != "" {
		filter, args := historyOriginFilter(c.Origin)
		rows, err := tx.QueryContext(ctx, `SELECT q.id FROM research_queries q LEFT JOIN research_query_origins o ON o.query_id=q.id WHERE q.project_id=?`+filter, append([]any{c.ProjectID}, args...)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	} else {
		for _, id := range c.QueryIDs {
			id = strings.TrimSpace(id)
			if !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
		}
	}
	for _, id := range ids {
		if err := queryHistoryMutable(ctx, tx, c.ProjectID, id); err != nil {
			return err
		}
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM research_queries WHERE id=? AND project_id=?`, id, c.ProjectID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
