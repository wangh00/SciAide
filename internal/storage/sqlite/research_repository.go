package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	appresearch "github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/id"
)

type ResearchRepository struct{ db *sql.DB }

func NewResearchRepository(db *sql.DB) *ResearchRepository { return &ResearchRepository{db: db} }

func (r *ResearchRepository) SaveSearch(ctx context.Context, projectID, queryKey string, command appresearch.SearchCommand, result appresearch.SearchResult, at time.Time) (appresearch.Query, error) {
	projectID, queryKey = strings.TrimSpace(projectID), strings.TrimSpace(queryKey)
	if projectID == "" || len(queryKey) != 64 {
		return appresearch.Query{}, fmt.Errorf("research query identity is invalid")
	}
	sourceIDs := normalizeSourceIDs(command.SourceIDs, result.Sources)
	sourceJSON, err := json.Marshal(sourceIDs)
	if err != nil {
		return appresearch.Query{}, err
	}
	statusesJSON, err := json.Marshal(nonNilSourceStatuses(result.Sources))
	if err != nil {
		return appresearch.Query{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return appresearch.Query{}, fmt.Errorf("begin research search persistence: %w", err)
	}
	defer tx.Rollback()
	queryID := ""
	createdAt := at
	err = tx.QueryRowContext(ctx, `SELECT id,created_at FROM research_queries WHERE project_id=? AND query_key=?`, projectID, queryKey).Scan(&queryID, newTimeScanner(&createdAt))
	if errors.Is(err, sql.ErrNoRows) {
		queryID, err = id.New()
		if err != nil {
			return appresearch.Query{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO research_queries(id,project_id,query_text,query_key,source_ids_json,limit_per_source,source_statuses_json,partial,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			queryID, projectID, result.Query, queryKey, string(sourceJSON), command.Limit, string(statusesJSON), result.Partial, formatTime(at), formatTime(at))
	} else if err == nil {
		var version int
		if err := tx.QueryRowContext(ctx, `SELECT snapshot_version FROM research_queries WHERE id=?`, queryID).Scan(&version); err != nil {
			return appresearch.Query{}, err
		}
		if version > 0 {
			return scanResearchQuery(tx.QueryRowContext(ctx, `SELECT q.id,q.project_id,q.query_text,q.source_ids_json,q.limit_per_source,q.source_statuses_json,q.partial,q.created_at,q.updated_at,(SELECT COUNT(*) FROM research_query_records WHERE query_id=q.id) FROM research_queries q WHERE q.id=?`, queryID))
		}
		_, err = tx.ExecContext(ctx, `UPDATE research_queries SET query_text=?,source_ids_json=?,limit_per_source=?,source_statuses_json=?,partial=?,updated_at=? WHERE id=? AND project_id=?`,
			result.Query, string(sourceJSON), command.Limit, string(statusesJSON), result.Partial, formatTime(at), queryID, projectID)
	} else {
		return appresearch.Query{}, fmt.Errorf("read research query: %w", err)
	}
	if err != nil {
		return appresearch.Query{}, fmt.Errorf("save research query: %w", err)
	}
	if command.ResearchTaskID != "" {
		result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO research_query_origins(query_id,task_id,task_title) SELECT ?,id,title FROM research_tasks WHERE id=? AND project_id=? AND archived_at IS NULL`, queryID, command.ResearchTaskID, projectID)
		if err != nil {
			return appresearch.Query{}, err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return appresearch.Query{}, fmt.Errorf("query task ownership is invalid")
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM research_query_records WHERE query_id=?`, queryID); err != nil {
		return appresearch.Query{}, fmt.Errorf("reset research query results: %w", err)
	}
	touchedCandidates := map[string]struct{}{}
	snapshots := map[string]appresearch.Candidate{}
	for ordinal, work := range result.Works {
		recordID, existingCandidateID, aliases, err := saveResearchSourceRecord(ctx, tx, projectID, work, at)
		if err != nil {
			return appresearch.Query{}, err
		}
		candidateID := existingCandidateID
		if candidateID == "" {
			candidateID, err = findOrCreateResearchCandidate(ctx, tx, projectID, work, aliases, at)
			if err != nil {
				return appresearch.Query{}, err
			}
		}
		if err := linkResearchCandidateRecord(ctx, tx, candidateID, recordID); err != nil {
			return appresearch.Query{}, err
		}
		touchedCandidates[candidateID] = struct{}{}
		snapshot := snapshots[candidateID]
		snapshot.ID, snapshot.ProjectID = candidateID, projectID
		snapshot.CreatedAt, snapshot.UpdatedAt = at, at
		snapshot.Records = append(snapshot.Records, appresearch.SourceRecord{ID: recordID, ProjectID: projectID, Work: work, FirstSeenAt: at, UpdatedAt: at})
		snapshot.Aliases = append(snapshot.Aliases, aliases...)
		snapshot.Preferred = appresearch.PreferredWork(snapshot.Records)
		snapshots[candidateID] = snapshot
		for _, alias := range aliases {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO research_candidate_aliases(candidate_id,project_id,alias_kind,alias_value,created_at) VALUES (?,?,?,?,?)`, candidateID, projectID, alias.Kind, alias.Value, formatTime(at)); err != nil {
				return appresearch.Query{}, fmt.Errorf("save research candidate alias: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO research_query_records(query_id,source_record_id,ordinal) VALUES (?,?,?)`, queryID, recordID, ordinal); err != nil {
			return appresearch.Query{}, fmt.Errorf("save research query result: %w", err)
		}
	}
	for candidateID, snapshot := range snapshots {
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return appresearch.Query{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO research_query_candidates(query_id,candidate_id,snapshot_json) VALUES(?,?,?)`, queryID, candidateID, string(encoded)); err != nil {
			return appresearch.Query{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE research_queries SET snapshot_version=1 WHERE id=?`, queryID); err != nil {
		return appresearch.Query{}, err
	}
	for candidateID := range touchedCandidates {
		if err := syncResearchBibliography(ctx, tx, projectID, candidateID, at); err != nil {
			return appresearch.Query{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return appresearch.Query{}, fmt.Errorf("commit research search persistence: %w", err)
	}
	return appresearch.Query{ID: queryID, ProjectID: projectID, Text: result.Query, SourceIDs: sourceIDs, LimitPerSource: command.Limit, Sources: nonNilSourceStatuses(result.Sources), Partial: result.Partial, ResultCount: len(result.Works), CreatedAt: createdAt, UpdatedAt: at}, nil
}

func (r *ResearchRepository) ListQueries(ctx context.Context, projectID string, limit int) ([]appresearch.Query, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	return r.QueryHistory(ctx, appresearch.QueryPageCommand{ProjectID: strings.TrimSpace(projectID), Limit: limit})
}

func (r *ResearchRepository) ListCandidates(ctx context.Context, projectID, queryID string) ([]appresearch.Candidate, error) {
	queryID = strings.TrimSpace(queryID)
	query := `SELECT c.id,c.project_id,c.candidate_key,c.review_status,c.exclusion_reason,c.note,c.import_status,c.import_kind,COALESCE(c.attachment_id,''),c.import_error,c.created_at,c.updated_at FROM research_candidates c WHERE c.project_id=?`
	args := []any{strings.TrimSpace(projectID)}
	if queryID != "" {
		query += ` AND EXISTS (SELECT 1 FROM research_candidate_records cr JOIN research_query_records qr ON qr.source_record_id=cr.source_record_id WHERE cr.candidate_id=c.id AND qr.query_id=?)`
		args = append(args, queryID)
	}
	query += ` ORDER BY c.updated_at DESC,c.id`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list research candidates: %w", err)
	}
	defer rows.Close()
	values := []appresearch.Candidate{}
	for rows.Next() {
		value, err := scanResearchCandidate(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range values {
		if err := r.loadCandidateDetails(ctx, &values[index]); err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (r *ResearchRepository) GetCandidate(ctx context.Context, projectID, candidateID string) (appresearch.Candidate, error) {
	value, err := scanResearchCandidate(r.db.QueryRowContext(ctx, `SELECT id,project_id,candidate_key,review_status,exclusion_reason,note,import_status,import_kind,COALESCE(attachment_id,''),import_error,created_at,updated_at FROM research_candidates WHERE project_id=? AND id=?`, strings.TrimSpace(projectID), strings.TrimSpace(candidateID)))
	if errors.Is(err, sql.ErrNoRows) {
		return appresearch.Candidate{}, fmt.Errorf("research candidate not found")
	}
	if err != nil {
		return appresearch.Candidate{}, err
	}
	if err := r.loadCandidateDetails(ctx, &value); err != nil {
		return appresearch.Candidate{}, err
	}
	return value, nil
}

func (r *ResearchRepository) GetCandidateTaskImport(ctx context.Context, projectID, candidateID, researchTaskID string) (appresearch.CandidateTaskImport, bool, error) {
	var value appresearch.CandidateTaskImport
	var createdAt, updatedAt string
	err := r.db.QueryRowContext(ctx, `SELECT id,project_id,candidate_id,research_task_id,import_status,import_kind,COALESCE(attachment_id,''),import_error,created_at,updated_at FROM research_candidate_task_imports WHERE project_id=? AND candidate_id=? AND research_task_id=?`, strings.TrimSpace(projectID), strings.TrimSpace(candidateID), strings.TrimSpace(researchTaskID)).Scan(&value.ID, &value.ProjectID, &value.CandidateID, &value.ResearchTaskID, &value.Status, &value.Kind, &value.AttachmentID, &value.ErrorMessage, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return appresearch.CandidateTaskImport{}, false, nil
	}
	if err != nil {
		return appresearch.CandidateTaskImport{}, false, fmt.Errorf("read research task candidate import: %w", err)
	}
	var parseErr error
	value.CreatedAt, parseErr = parseTime(createdAt)
	if parseErr == nil {
		value.UpdatedAt, parseErr = parseTime(updatedAt)
	}
	if parseErr != nil {
		return appresearch.CandidateTaskImport{}, false, parseErr
	}
	return value, true, nil
}

func (r *ResearchRepository) UpdateCandidateTaskImport(ctx context.Context, command appresearch.ImportStateCommand) (appresearch.CandidateTaskImport, error) {
	projectID, candidateID, taskID := strings.TrimSpace(command.ProjectID), strings.TrimSpace(command.CandidateID), strings.TrimSpace(command.ResearchTaskID)
	if projectID == "" || candidateID == "" || taskID == "" {
		return appresearch.CandidateTaskImport{}, fmt.Errorf("research task candidate import identity is required")
	}
	if command.Status != appresearch.ImportNotImported && command.Status != appresearch.ImportImporting && command.Status != appresearch.ImportImported && command.Status != appresearch.ImportFailed {
		return appresearch.CandidateTaskImport{}, fmt.Errorf("research task candidate import status is invalid")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return appresearch.CandidateTaskImport{}, fmt.Errorf("begin research task candidate import update: %w", err)
	}
	defer tx.Rollback()
	var importID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM research_candidate_task_imports WHERE project_id=? AND candidate_id=? AND research_task_id=?`, projectID, candidateID, taskID).Scan(&importID)
	if errors.Is(err, sql.ErrNoRows) {
		importID, err = id.New()
		if err != nil {
			return appresearch.CandidateTaskImport{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO research_candidate_task_imports(id,project_id,candidate_id,research_task_id,import_status,import_kind,attachment_id,import_error,created_at,updated_at) VALUES (?,?,?,?,?,?,NULLIF(?,''),?,?,?)`, importID, projectID, candidateID, taskID, command.Status, command.Kind, command.AttachmentID, command.ErrorMessage, formatTime(command.At), formatTime(command.At))
	} else if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE research_candidate_task_imports SET import_status=?,import_kind=?,attachment_id=NULLIF(?,''),import_error=?,updated_at=? WHERE id=? AND project_id=?`, command.Status, command.Kind, command.AttachmentID, command.ErrorMessage, formatTime(command.At), importID, projectID)
	} else {
		return appresearch.CandidateTaskImport{}, fmt.Errorf("read research task candidate import: %w", err)
	}
	if err != nil {
		return appresearch.CandidateTaskImport{}, fmt.Errorf("save research task candidate import: %w", err)
	}
	if command.Status == appresearch.ImportImported && command.AttachmentID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE research_candidate_task_imports SET pending_sha256='',pending_kind='' WHERE id=? AND pending_sha256=(SELECT sha256 FROM attachments WHERE id=?)`, importID, command.AttachmentID); err != nil {
			return appresearch.CandidateTaskImport{}, err
		}
		if err := syncBibliographyMaterialForAttachment(ctx, tx, projectID, candidateID, command.AttachmentID, taskID, command.Kind, command.At); err != nil {
			return appresearch.CandidateTaskImport{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return appresearch.CandidateTaskImport{}, fmt.Errorf("commit research task candidate import: %w", err)
	}
	value, found, err := r.GetCandidateTaskImport(ctx, projectID, candidateID, taskID)
	if err != nil {
		return appresearch.CandidateTaskImport{}, err
	}
	if !found {
		return appresearch.CandidateTaskImport{}, fmt.Errorf("research task candidate import disappeared")
	}
	return value, nil
}

func (r *ResearchRepository) UpdateReview(ctx context.Context, command appresearch.ReviewCommand, at time.Time) (appresearch.Candidate, error) {
	if command.ResearchTaskID != "" {
		var writable bool
		if err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM research_tasks WHERE id=? AND project_id=? AND archived_at IS NULL)`, command.ResearchTaskID, command.ProjectID).Scan(&writable); err != nil {
			return appresearch.Candidate{}, err
		}
		if !writable {
			return appresearch.Candidate{}, fmt.Errorf("research review task is read only")
		}
		value, err := r.CandidateForTask(ctx, command.ProjectID, command.CandidateID, command.ResearchTaskID)
		if err != nil {
			return appresearch.Candidate{}, err
		}
		_, err = r.db.ExecContext(ctx, `INSERT INTO research_candidate_reviews(project_id,task_id,candidate_id,review_status,exclusion_reason,note,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(project_id,task_id,candidate_id) DO UPDATE SET review_status=excluded.review_status,exclusion_reason=excluded.exclusion_reason,note=excluded.note,updated_at=excluded.updated_at`, command.ProjectID, command.ResearchTaskID, command.CandidateID, command.Status, command.ExclusionReason, command.Note, formatTime(at))
		value.ReviewStatus, value.ExclusionReason, value.Note = command.Status, command.ExclusionReason, command.Note
		return value, err
	}
	result, err := r.db.ExecContext(ctx, `UPDATE research_candidates SET review_status=?,exclusion_reason=?,note=?,updated_at=? WHERE id=? AND project_id=?`, command.Status, command.ExclusionReason, command.Note, formatTime(at), command.CandidateID, command.ProjectID)
	if err != nil {
		return appresearch.Candidate{}, fmt.Errorf("update research candidate review: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return appresearch.Candidate{}, fmt.Errorf("research candidate not found")
	}
	return r.GetCandidate(ctx, command.ProjectID, command.CandidateID)
}

func (r *ResearchRepository) UpdateImportState(ctx context.Context, command appresearch.ImportStateCommand) (appresearch.Candidate, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return appresearch.Candidate{}, fmt.Errorf("begin research candidate import update: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE research_candidates SET import_status=?,import_kind=?,attachment_id=NULLIF(?,''),import_error=?,updated_at=? WHERE id=? AND project_id=?`, command.Status, command.Kind, command.AttachmentID, command.ErrorMessage, formatTime(command.At), command.CandidateID, command.ProjectID)
	if err != nil {
		return appresearch.Candidate{}, fmt.Errorf("update research candidate import: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return appresearch.Candidate{}, fmt.Errorf("research candidate not found")
	}
	if command.Status == appresearch.ImportImported && command.AttachmentID != "" {
		if err := syncResearchBibliography(ctx, tx, command.ProjectID, command.CandidateID, command.At); err != nil {
			return appresearch.Candidate{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return appresearch.Candidate{}, fmt.Errorf("commit research candidate import update: %w", err)
	}
	return r.GetCandidate(ctx, command.ProjectID, command.CandidateID)
}

func (r *ResearchRepository) RecoverImports(ctx context.Context, at time.Time) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var count int64
	for _, table := range []string{"research_candidates", "research_candidate_task_imports"} {
		result, err := tx.ExecContext(ctx, `UPDATE `+table+` SET import_status=CASE WHEN attachment_id IS NOT NULL AND import_kind<>'' THEN 'imported' ELSE 'failed' END,import_error='application stopped before the research import completed; previous material retained',updated_at=? WHERE import_status='importing'`, formatTime(at))
		if err != nil {
			return 0, fmt.Errorf("recover research imports: %w", err)
		}
		n, _ := result.RowsAffected()
		count += n
	}
	if err := tx.Commit(); err != nil {
		return count, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT project_id,candidate_id,research_task_id FROM research_candidate_task_imports WHERE pending_sha256<>''`)
	if err != nil {
		return count, err
	}
	pending := [][3]string{}
	for rows.Next() {
		var identity [3]string
		if err := rows.Scan(&identity[0], &identity[1], &identity[2]); err != nil {
			rows.Close()
			return count, err
		}
		pending = append(pending, identity)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return count, err
	}
	for _, identity := range pending {
		if _, err := r.RecoverMaterialIntent(ctx, identity[0], identity[1], identity[2]); err != nil {
			return count, err
		}
	}
	return count, nil
}

type researchSQLExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func saveResearchSourceRecord(ctx context.Context, tx researchSQLExecutor, projectID string, work appresearch.Work, at time.Time) (string, string, []appresearch.CandidateAlias, error) {
	recordID := ""
	err := tx.QueryRowContext(ctx, `SELECT id FROM research_source_records WHERE project_id=? AND source_id=? AND source_record_id=?`, projectID, work.SourceID, work.SourceRecordID).Scan(&recordID)
	existingCandidateID := ""
	if err == nil {
		linkErr := tx.QueryRowContext(ctx, `SELECT candidate_id FROM research_candidate_records WHERE source_record_id=?`, recordID).Scan(&existingCandidateID)
		if linkErr != nil && !errors.Is(linkErr, sql.ErrNoRows) {
			return "", "", nil, fmt.Errorf("read research source candidate: %w", linkErr)
		}
	}
	raw := work.RawSnapshot
	work.RawSnapshot = nil
	workJSON, marshalErr := json.Marshal(work)
	if marshalErr != nil {
		return "", "", nil, marshalErr
	}
	if len(raw) == 0 || !json.Valid(raw) || raw[0] != '{' {
		raw = json.RawMessage(`{}`)
	}
	if errors.Is(err, sql.ErrNoRows) {
		recordID, err = id.New()
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO research_source_records(id,project_id,source_id,source_record_id,work_json,raw_snapshot_json,first_seen_at,updated_at) VALUES (?,?,?,?,?,?,?,?)`, recordID, projectID, work.SourceID, work.SourceRecordID, string(workJSON), string(raw), formatTime(at), formatTime(at))
		}
	} else if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE research_source_records SET work_json=?,raw_snapshot_json=?,updated_at=? WHERE id=? AND project_id=?`, string(workJSON), string(raw), formatTime(at), recordID, projectID)
	}
	if err != nil {
		return "", "", nil, fmt.Errorf("save research source record: %w", err)
	}
	return recordID, existingCandidateID, appresearch.CandidateAliases(work), nil
}

func findOrCreateResearchCandidate(ctx context.Context, tx researchSQLExecutor, projectID string, work appresearch.Work, aliases []appresearch.CandidateAlias, at time.Time) (string, error) {
	candidates := map[string]struct{}{}
	hasStrongIdentifier := false
	for _, alias := range aliases {
		if alias.Kind != "title_year" {
			hasStrongIdentifier = true
			break
		}
	}
	for _, alias := range aliases {
		if hasStrongIdentifier && alias.Kind == "title_year" {
			continue
		}
		rows, err := queryRows(ctx, tx, `SELECT candidate_id FROM research_candidate_aliases WHERE project_id=? AND alias_kind=? AND alias_value=? ORDER BY candidate_id`, projectID, alias.Kind, alias.Value)
		if err != nil {
			return "", fmt.Errorf("find research candidate alias: %w", err)
		}
		for rows.Next() {
			var candidateID string
			if err := rows.Scan(&candidateID); err != nil {
				_ = rows.Close()
				return "", err
			}
			candidates[candidateID] = struct{}{}
		}
		_ = rows.Close()
	}
	if len(candidates) > 1 {
		// Conflicting identifiers are evidence of ambiguous records. Preserve the
		// record independently instead of silently merging existing candidates.
		candidates = map[string]struct{}{}
	}
	for candidateID := range candidates {
		return candidateID, nil
	}
	candidateID, err := id.New()
	if err != nil {
		return "", err
	}
	key := appresearch.CandidateKey(work)
	if _, err := tx.ExecContext(ctx, `INSERT INTO research_candidates(id,project_id,candidate_key,created_at,updated_at) VALUES (?,?,?,?,?)`, candidateID, projectID, key, formatTime(at), formatTime(at)); err != nil {
		return "", fmt.Errorf("create research candidate: %w", err)
	}
	return candidateID, nil
}

func linkResearchCandidateRecord(ctx context.Context, tx researchSQLExecutor, candidateID, recordID string) error {
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT candidate_id FROM research_candidate_records WHERE source_record_id=?`, recordID).Scan(&existing)
	if err == nil {
		if existing != candidateID {
			return fmt.Errorf("research source record already belongs to another candidate")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var ordinal int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal),-1)+1 FROM research_candidate_records WHERE candidate_id=?`, candidateID).Scan(&ordinal); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO research_candidate_records(candidate_id,source_record_id,ordinal) VALUES (?,?,?)`, candidateID, recordID, ordinal); err != nil {
		return fmt.Errorf("link research candidate record: %w", err)
	}
	return nil
}

func (r *ResearchRepository) loadCandidateDetails(ctx context.Context, value *appresearch.Candidate) error {
	rows, err := r.db.QueryContext(ctx, `SELECT sr.id,sr.project_id,sr.work_json,sr.raw_snapshot_json,sr.first_seen_at,sr.updated_at FROM research_candidate_records cr JOIN research_source_records sr ON sr.id=cr.source_record_id WHERE cr.candidate_id=? ORDER BY cr.ordinal`, value.ID)
	if err != nil {
		return fmt.Errorf("load research candidate records: %w", err)
	}
	defer rows.Close()
	value.Records = []appresearch.SourceRecord{}
	for rows.Next() {
		var record appresearch.SourceRecord
		var workJSON, rawJSON, firstSeen, updated string
		if err := rows.Scan(&record.ID, &record.ProjectID, &workJSON, &rawJSON, &firstSeen, &updated); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(workJSON), &record.Work); err != nil {
			return fmt.Errorf("decode research source record: %w", err)
		}
		if rawJSON != "{}" {
			record.Work.RawSnapshot = json.RawMessage(rawJSON)
		}
		var err error
		record.FirstSeenAt, err = parseTime(firstSeen)
		if err == nil {
			record.UpdatedAt, err = parseTime(updated)
		}
		if err != nil {
			return err
		}
		value.Records = append(value.Records, record)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	aliasRows, err := r.db.QueryContext(ctx, `SELECT alias_kind,alias_value FROM research_candidate_aliases WHERE candidate_id=? ORDER BY alias_kind,alias_value`, value.ID)
	if err != nil {
		return err
	}
	defer aliasRows.Close()
	value.Aliases = []appresearch.CandidateAlias{}
	for aliasRows.Next() {
		var alias appresearch.CandidateAlias
		if err := aliasRows.Scan(&alias.Kind, &alias.Value); err != nil {
			return err
		}
		value.Aliases = append(value.Aliases, alias)
	}
	if err := aliasRows.Err(); err != nil {
		return err
	}
	value.Preferred = appresearch.PreferredWork(value.Records)
	return nil
}

func scanResearchQuery(row rowScanner) (appresearch.Query, error) {
	var value appresearch.Query
	var sourcesJSON, statusesJSON, created, updated string
	if err := row.Scan(&value.ID, &value.ProjectID, &value.Text, &sourcesJSON, &value.LimitPerSource, &statusesJSON, &value.Partial, &created, &updated, &value.ResultCount); err != nil {
		return appresearch.Query{}, err
	}
	if err := json.Unmarshal([]byte(sourcesJSON), &value.SourceIDs); err != nil {
		return appresearch.Query{}, err
	}
	if err := json.Unmarshal([]byte(statusesJSON), &value.Sources); err != nil {
		return appresearch.Query{}, err
	}
	var err error
	value.CreatedAt, err = parseTime(created)
	if err == nil {
		value.UpdatedAt, err = parseTime(updated)
	}
	return value, err
}

func scanResearchCandidate(row rowScanner) (appresearch.Candidate, error) {
	var value appresearch.Candidate
	var created, updated string
	if err := row.Scan(&value.ID, &value.ProjectID, &value.CandidateKey, &value.ReviewStatus, &value.ExclusionReason, &value.Note, &value.ImportStatus, &value.ImportKind, &value.AttachmentID, &value.ImportError, &created, &updated); err != nil {
		return appresearch.Candidate{}, err
	}
	var err error
	value.CreatedAt, err = parseTime(created)
	if err == nil {
		value.UpdatedAt, err = parseTime(updated)
	}
	return value, err
}

func normalizeSourceIDs(values []string, statuses []appresearch.SourceSearch) []string {
	if len(values) == 0 {
		values = make([]string, 0, len(statuses))
		for _, status := range statuses {
			values = append(values, status.SourceID)
		}
	}
	seen := map[string]struct{}{}
	result := []string{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func nonNilSourceStatuses(values []appresearch.SourceSearch) []appresearch.SourceSearch {
	if values == nil {
		return []appresearch.SourceSearch{}
	}
	return values
}

type timeScanner struct{ destination *time.Time }

func newTimeScanner(destination *time.Time) *timeScanner {
	return &timeScanner{destination: destination}
}
func (s *timeScanner) Scan(value any) error {
	text, ok := value.(string)
	if !ok {
		bytes, bytesOK := value.([]byte)
		if !bytesOK {
			return fmt.Errorf("invalid time value")
		}
		text = string(bytes)
	}
	parsed, err := parseTime(text)
	if err == nil {
		*s.destination = parsed
	}
	return err
}

// sql.Tx exposes QueryContext but the narrow executor above intentionally does
// not. Keep row iteration isolated to this adapter helper.
func queryRows(ctx context.Context, executor researchSQLExecutor, query string, args ...any) (*sql.Rows, error) {
	if tx, ok := executor.(*sql.Tx); ok {
		return tx.QueryContext(ctx, query, args...)
	}
	if db, ok := executor.(*sql.DB); ok {
		return db.QueryContext(ctx, query, args...)
	}
	return nil, fmt.Errorf("research SQL executor cannot query rows")
}
