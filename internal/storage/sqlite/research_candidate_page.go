package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	research "github.com/wangh00/SciAide/internal/app/research"
)

func (r *ResearchRepository) CandidateForTask(ctx context.Context, projectID, candidateID, taskID string) (research.Candidate, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT s.snapshot_json FROM research_queries q JOIN research_query_origins o ON o.query_id=q.id JOIN research_query_candidates s ON s.query_id=q.id WHERE q.project_id=? AND o.task_id=? AND s.candidate_id=? ORDER BY q.created_at,q.id`, projectID, taskID, candidateID)
	if err != nil {
		return research.Candidate{}, err
	}
	defer rows.Close()
	value := research.Candidate{ID: candidateID, ProjectID: projectID}
	for rows.Next() {
		var encoded string
		var snapshot research.Candidate
		if err := rows.Scan(&encoded); err != nil {
			return value, err
		}
		if err := json.Unmarshal([]byte(encoded), &snapshot); err != nil {
			return value, err
		}
		value.Records = append(value.Records, snapshot.Records...)
		value.Aliases = append(value.Aliases, snapshot.Aliases...)
		value.CreatedAt = snapshot.CreatedAt
		value.UpdatedAt = snapshot.UpdatedAt
	}
	if err := rows.Err(); err != nil {
		return value, err
	}
	rows.Close()
	if len(value.Records) == 0 {
		return value, fmt.Errorf("candidate is not in this task's discovery snapshots")
	}
	value.Preferred = research.PreferredWork(value.Records)
	if err := r.applyCandidateReview(ctx, &value, taskID); err != nil {
		return value, err
	}
	state, found, err := r.GetCandidateTaskImport(ctx, projectID, candidateID, taskID)
	if err != nil {
		return value, err
	}
	if found {
		value.ImportStatus, value.ImportKind, value.AttachmentID, value.ImportError = state.Status, state.Kind, state.AttachmentID, state.ErrorMessage
	}
	return value, nil
}

func (r *ResearchRepository) CandidatePage(ctx context.Context, c research.CandidateListCommand) (research.CandidatePage, error) {
	var owner string
	var version int
	if err := r.db.QueryRowContext(ctx, `SELECT COALESCE(o.task_id,''),q.snapshot_version FROM research_queries q LEFT JOIN research_query_origins o ON o.query_id=q.id WHERE q.id=? AND q.project_id=?`, c.QueryID, c.ProjectID).Scan(&owner, &version); err != nil {
		return research.CandidatePage{}, err
	}
	if c.ResearchTaskID != "" && c.ResearchTaskID != owner {
		return research.CandidatePage{}, fmt.Errorf("query does not belong to task")
	}
	c.ResearchTaskID = owner
	if version == 0 {
		// Old content cannot be reconstructed; the UI labels this legacy view.
		values, err := r.ListCandidates(ctx, c.ProjectID, c.QueryID)
		if err != nil {
			return research.CandidatePage{}, err
		}
		for i := range values {
			if err := r.applyCandidateReview(ctx, &values[i], owner); err != nil {
				return research.CandidatePage{}, err
			}
		}
		return research.FilterCandidatePage(values, c), nil
	}
	return r.candidateSnapshotPage(ctx, c, "")
}

func (r *ResearchRepository) candidateSnapshotPage(ctx context.Context, c research.CandidateListCommand, candidateID string) (research.CandidatePage, error) {
	review := `CASE WHEN ?='' THEN candidate.review_status ELSE COALESCE(review.review_status,'pending') END`
	base := ` FROM research_query_candidates s JOIN research_candidates candidate ON candidate.id=s.candidate_id LEFT JOIN research_candidate_reviews review ON review.candidate_id=s.candidate_id AND review.project_id=candidate.project_id AND review.task_id=? LEFT JOIN research_candidate_task_imports imported ON imported.candidate_id=s.candidate_id AND imported.project_id=candidate.project_id AND imported.research_task_id=? WHERE s.query_id=? AND candidate.project_id=?`
	args := []any{c.ResearchTaskID, c.ResearchTaskID, c.QueryID, c.ProjectID}
	if candidateID != "" {
		base += ` AND s.candidate_id=?`
		args = append(args, candidateID)
	}
	if c.Status != "" {
		base += ` AND (` + review + `)=?`
		args = append(args, c.ResearchTaskID, c.Status)
	}
	if search := strings.TrimSpace(c.Search); search != "" {
		base += ` AND instr(lower(json_extract(s.snapshot_json,'$.preferred.title') || ' ' || COALESCE(json_extract(s.snapshot_json,'$.preferred.authors'),'') || ' ' || COALESCE(json_extract(s.snapshot_json,'$.preferred.venue'),'')),lower(?))>0`
		args = append(args, search)
	}
	page := research.CandidatePage{Items: []research.Candidate{}, Offset: c.Offset, Limit: c.Limit}
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*)`+base, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	order := `COALESCE(json_extract(s.snapshot_json,'$.preferred.score'),0) DESC`
	switch c.Sort {
	case "year_desc":
		order = `COALESCE(json_extract(s.snapshot_json,'$.preferred.year'),0) DESC`
	case "cited_desc":
		order = `COALESCE(json_extract(s.snapshot_json,'$.preferred.citedByCount'),0) DESC`
	case "title":
		order = `lower(json_extract(s.snapshot_json,'$.preferred.title'))`
	case "updated":
		order = `COALESCE(review.updated_at,json_extract(s.snapshot_json,'$.updatedAt')) DESC`
	}
	imports := `candidate.import_status,candidate.import_kind,COALESCE(candidate.attachment_id,''),candidate.import_error`
	if c.ResearchTaskID != "" {
		imports = `COALESCE(imported.import_status,'not_imported'),COALESCE(imported.import_kind,''),COALESCE(imported.attachment_id,''),COALESCE(imported.import_error,'')`
	}
	rows, err := r.db.QueryContext(ctx, `SELECT s.snapshot_json,`+review+`,CASE WHEN ?='' THEN candidate.exclusion_reason ELSE COALESCE(review.exclusion_reason,'') END,CASE WHEN ?='' THEN candidate.note ELSE COALESCE(review.note,'') END,`+imports+base+` ORDER BY `+order+`,s.candidate_id LIMIT ? OFFSET ?`, append([]any{c.ResearchTaskID, c.ResearchTaskID, c.ResearchTaskID}, append(args, c.Limit, c.Offset)...)...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw, status, reason, note string
		var imported research.Candidate
		if err := rows.Scan(&raw, &status, &reason, &note, &imported.ImportStatus, &imported.ImportKind, &imported.AttachmentID, &imported.ImportError); err != nil {
			return page, err
		}
		var value research.Candidate
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return page, err
		}
		value.ReviewStatus, value.ExclusionReason, value.Note = research.ReviewStatus(status), reason, note
		value.ImportStatus, value.ImportKind, value.AttachmentID, value.ImportError = imported.ImportStatus, imported.ImportKind, imported.AttachmentID, imported.ImportError
		page.Items = append(page.Items, value)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	rows.Close()
	return page, nil
}

func (r *ResearchRepository) applyCandidateReview(ctx context.Context, value *research.Candidate, taskID string) error {
	if taskID == "" {
		return nil
	}
	value.ReviewStatus, value.ExclusionReason, value.Note = research.ReviewPending, "", ""
	value.ImportStatus, value.ImportKind, value.AttachmentID, value.ImportError = research.ImportNotImported, "", "", ""
	err := r.db.QueryRowContext(ctx, `SELECT review_status,exclusion_reason,note FROM research_candidate_reviews WHERE project_id=? AND candidate_id=? AND task_id=?`, value.ProjectID, value.ID, taskID).Scan(&value.ReviewStatus, &value.ExclusionReason, &value.Note)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}
