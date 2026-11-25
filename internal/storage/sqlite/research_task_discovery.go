package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/research"
)

func (r *ResearchRepository) TaskDiscoveryPage(ctx context.Context, c research.CandidateListCommand) (research.CandidatePage, error) {
	var valid bool
	if err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM research_queries q
 JOIN research_query_origins o ON o.query_id=q.id JOIN research_tasks t ON t.id=o.task_id AND t.project_id=q.project_id
 WHERE q.project_id=? AND q.id=? AND o.task_id=? AND q.snapshot_version>0 AND t.status<>'archived')`, c.ProjectID, c.QueryID, c.ResearchTaskID).Scan(&valid); err != nil {
		return research.CandidatePage{}, err
	}
	if !valid {
		return research.CandidatePage{}, fmt.Errorf("检索快照不属于当前任务、任务已归档，或缺少原始快照；不能以最新题录替代")
	}
	return r.CandidatePage(ctx, c)
}

func (r *ResearchRepository) TaskDiscoveryCandidate(ctx context.Context, projectID, taskID, queryID, candidateID string) (research.Candidate, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `SELECT s.snapshot_json FROM research_query_candidates s
 JOIN research_queries q ON q.id=s.query_id JOIN research_query_origins o ON o.query_id=q.id
 JOIN research_tasks t ON t.id=o.task_id AND t.project_id=q.project_id
 WHERE q.project_id=? AND q.id=? AND o.task_id=? AND s.candidate_id=? AND q.snapshot_version>0 AND t.status<>'archived'`, projectID, queryID, taskID, candidateID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return research.Candidate{}, fmt.Errorf("原查询候选快照不存在、不属于当前任务或任务已归档；不能用最新题录替代")
	}
	if err != nil {
		return research.Candidate{}, err
	}
	var value research.Candidate
	if err = json.Unmarshal([]byte(raw), &value); err != nil {
		return value, fmt.Errorf("原查询候选快照无效: %w", err)
	}
	if value.ID != candidateID || value.ProjectID != projectID {
		return research.Candidate{}, fmt.Errorf("原查询候选快照身份不一致")
	}
	return value, nil
}
