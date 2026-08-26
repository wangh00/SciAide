package sqlite

import (
	"bytes"
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

type bibliographyRecord struct {
	id       string
	sourceID string
	recordID string
	work     appresearch.Work
	at       time.Time
}

func syncResearchBibliography(ctx context.Context, executor researchSQLExecutor, projectID, candidateID string, at time.Time) error {
	records, err := loadBibliographyRecords(ctx, executor, projectID, candidateID)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return fmt.Errorf("research candidate has no source records")
	}
	var bibliographyID string
	var canonicalJSON, selectedJSON string
	err = executor.QueryRowContext(ctx, `SELECT id,canonical_json,selected_sources_json FROM research_bibliographies WHERE project_id=? AND candidate_id=?`, projectID, candidateID).Scan(&bibliographyID, &canonicalJSON, &selectedJSON)
	if errors.Is(err, sql.ErrNoRows) {
		bibliographyID, err = id.New()
		if err != nil {
			return err
		}
		data, selected := initialBibliography(records)
		canonical, marshalErr := json.Marshal(data)
		if marshalErr != nil {
			return marshalErr
		}
		selection, marshalErr := json.Marshal(selected)
		if marshalErr != nil {
			return marshalErr
		}
		canonicalJSON, selectedJSON = string(canonical), string(selection)
		if _, err = executor.ExecContext(ctx, `INSERT INTO research_bibliographies(id,project_id,candidate_id,revision_number,canonical_json,selected_sources_json,created_at,updated_at) VALUES (?,?,?,1,?,?,?,?)`, bibliographyID, projectID, candidateID, canonicalJSON, selectedJSON, formatTime(at), formatTime(at)); err != nil {
			return fmt.Errorf("create research bibliography: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("read research bibliography: %w", err)
	}
	for _, record := range records {
		data := appresearch.BibliographyFromWork(record.work)
		for _, field := range appresearch.BibliographyFields() {
			value, nonempty := appresearch.BibliographyFieldValue(data, field)
			if !nonempty {
				continue
			}
			fieldID, idErr := id.New()
			if idErr != nil {
				return idErr
			}
			_, err = executor.ExecContext(ctx, `INSERT OR IGNORE INTO research_bibliography_field_sources(id,bibliography_id,field_name,source_record_id,source_id_snapshot,source_record_id_snapshot,value_json,value_sha256,observed_at) VALUES (?,?,?,?,?,?,?,?,?)`,
				fieldID, bibliographyID, field, record.id, record.sourceID, record.recordID, string(value), appresearch.BibliographyValueSHA256(value), formatTime(record.at))
			if err != nil {
				return fmt.Errorf("save bibliography field source: %w", err)
			}
		}
	}
	if err := refreshAutoSelectedBibliography(ctx, executor, projectID, bibliographyID, canonicalJSON, selectedJSON, records, at); err != nil {
		return err
	}
	return syncBibliographyMaterialFromCandidate(ctx, executor, projectID, candidateID, bibliographyID, at)
}

func refreshAutoSelectedBibliography(ctx context.Context, executor researchSQLExecutor, projectID, bibliographyID, canonicalJSON, selectedJSON string, records []bibliographyRecord, at time.Time) error {
	var data appresearch.BibliographyData
	selected := map[string]string{}
	if json.Unmarshal([]byte(canonicalJSON), &data) != nil || json.Unmarshal([]byte(selectedJSON), &selected) != nil {
		return fmt.Errorf("decode research bibliography during source refresh")
	}
	byID := make(map[string]bibliographyRecord, len(records))
	for _, record := range records {
		byID[record.id] = record
	}
	type fieldChange struct {
		field    string
		previous json.RawMessage
		next     json.RawMessage
		record   bibliographyRecord
	}
	changes := []fieldChange{}
	for _, field := range appresearch.BibliographyFields() {
		selection := selected[field]
		if !strings.HasPrefix(selection, "auto:") {
			continue
		}
		record, exists := byID[strings.TrimPrefix(selection, "auto:")]
		if !exists {
			continue
		}
		next, nonempty := appresearch.BibliographyFieldValue(appresearch.BibliographyFromWork(record.work), field)
		if !nonempty {
			continue
		}
		previous, _ := appresearch.BibliographyFieldValue(data, field)
		if bytes.Equal(previous, next) {
			continue
		}
		changes = append(changes, fieldChange{field: field, previous: previous, next: next, record: record})
	}
	if len(changes) == 0 {
		return nil
	}
	var revision int
	if err := executor.QueryRowContext(ctx, `SELECT revision_number FROM research_bibliographies WHERE id=? AND project_id=?`, bibliographyID, projectID).Scan(&revision); err != nil {
		return err
	}
	nextRevision := revision + 1
	for _, change := range changes {
		if err := appresearch.SetBibliographyField(&data, change.field, change.next); err != nil {
			return err
		}
		revisionID, err := id.New()
		if err != nil {
			return err
		}
		if _, err := executor.ExecContext(ctx, `INSERT INTO research_bibliography_revisions(id,bibliography_id,revision_number,field_name,previous_json,next_json,source_kind,source_record_id_snapshot,reason,created_at) VALUES (?,?,?,?,?,?,'source_selection',?,'source record refreshed',?)`, revisionID, bibliographyID, nextRevision, change.field, string(change.previous), string(change.next), change.record.recordID, formatTime(at)); err != nil {
			return fmt.Errorf("record automatic bibliography refresh: %w", err)
		}
	}
	data = appresearch.NormalizeBibliography(data)
	canonical, _ := json.Marshal(data)
	result, err := executor.ExecContext(ctx, `UPDATE research_bibliographies SET revision_number=?,canonical_json=?,updated_at=? WHERE id=? AND project_id=? AND revision_number=?`, nextRevision, string(canonical), formatTime(at), bibliographyID, projectID, revision)
	if err != nil {
		return fmt.Errorf("refresh automatic bibliography fields: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("research bibliography changed during automatic refresh")
	}
	return nil
}

func loadBibliographyRecords(ctx context.Context, executor researchSQLExecutor, projectID, candidateID string) ([]bibliographyRecord, error) {
	rows, err := queryRows(ctx, executor, `SELECT sr.id,sr.source_id,sr.source_record_id,sr.work_json,sr.updated_at FROM research_candidate_records cr JOIN research_source_records sr ON sr.id=cr.source_record_id JOIN research_candidates c ON c.id=cr.candidate_id WHERE c.id=? AND c.project_id=? ORDER BY cr.ordinal,sr.id`, candidateID, projectID)
	if err != nil {
		return nil, fmt.Errorf("load bibliography source records: %w", err)
	}
	defer rows.Close()
	values := []bibliographyRecord{}
	for rows.Next() {
		var value bibliographyRecord
		var workJSON, observed string
		if err := rows.Scan(&value.id, &value.sourceID, &value.recordID, &workJSON, &observed); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(workJSON), &value.work); err != nil {
			return nil, fmt.Errorf("decode bibliography source record: %w", err)
		}
		value.at, err = parseTime(observed)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func initialBibliography(records []bibliographyRecord) (appresearch.BibliographyData, map[string]string) {
	ordered := append([]bibliographyRecord(nil), records...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := bibliographyCompleteness(ordered[i].work), bibliographyCompleteness(ordered[j].work)
		if left != right {
			return left > right
		}
		if ordered[i].sourceID != ordered[j].sourceID {
			return ordered[i].sourceID < ordered[j].sourceID
		}
		return ordered[i].recordID < ordered[j].recordID
	})
	result := appresearch.BibliographyData{Authors: []appresearch.BibliographicAuthor{}}
	selected := map[string]string{}
	for _, record := range ordered {
		data := appresearch.BibliographyFromWork(record.work)
		for _, field := range appresearch.BibliographyFields() {
			if _, exists := selected[field]; exists {
				continue
			}
			raw, nonempty := appresearch.BibliographyFieldValue(data, field)
			if !nonempty || appresearch.SetBibliographyField(&result, field, raw) != nil {
				continue
			}
			selected[field] = "auto:" + record.id
		}
	}
	return appresearch.NormalizeBibliography(result), selected
}

func bibliographyCompleteness(value appresearch.Work) int {
	data := appresearch.BibliographyFromWork(value)
	score := 0
	for _, field := range appresearch.BibliographyFields() {
		if _, ok := appresearch.BibliographyFieldValue(data, field); ok {
			score++
		}
	}
	return score
}

func syncBibliographyMaterialFromCandidate(ctx context.Context, executor researchSQLExecutor, projectID, candidateID, bibliographyID string, at time.Time) error {
	var attachmentID, attachmentSHA, documentID string
	var kind appresearch.ImportKind
	err := executor.QueryRowContext(ctx, `SELECT COALESCE(c.attachment_id,''),c.import_kind,COALESCE(a.sha256,''),COALESCE((SELECT kd.id FROM knowledge_documents kd WHERE kd.project_id=c.project_id AND kd.attachment_id=c.attachment_id ORDER BY kd.created_at LIMIT 1),'') FROM research_candidates c LEFT JOIN attachments a ON a.id=c.attachment_id AND a.project_id=c.project_id WHERE c.id=? AND c.project_id=?`, candidateID, projectID).Scan(&attachmentID, &kind, &attachmentSHA, &documentID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("research candidate not found")
	}
	if err != nil {
		return fmt.Errorf("read research import material: %w", err)
	}
	if attachmentID == "" || attachmentSHA == "" || (kind != appresearch.ImportFullText && kind != appresearch.ImportMetadataAbstract) {
		return nil
	}
	materialID, err := id.New()
	if err != nil {
		return err
	}
	level := appresearch.EvidenceMetadataAbstract
	if kind == appresearch.ImportFullText {
		level = appresearch.EvidenceFullText
	}
	_, err = executor.ExecContext(ctx, `INSERT INTO research_bibliography_materials(id,bibliography_id,project_id,attachment_id,knowledge_document_id,attachment_id_snapshot,knowledge_document_id_snapshot,attachment_sha256_snapshot,import_kind,evidence_level,created_at,updated_at) VALUES (?,?,?,?,NULLIF(?,''),?,?,?,?,?,?,?) ON CONFLICT(bibliography_id,attachment_sha256_snapshot) DO UPDATE SET attachment_id=excluded.attachment_id,knowledge_document_id=excluded.knowledge_document_id,knowledge_document_id_snapshot=CASE WHEN excluded.knowledge_document_id_snapshot<>'' THEN excluded.knowledge_document_id_snapshot ELSE research_bibliography_materials.knowledge_document_id_snapshot END,updated_at=CASE WHEN COALESCE(research_bibliography_materials.attachment_id,'')<>excluded.attachment_id_snapshot OR COALESCE(research_bibliography_materials.knowledge_document_id,'')<>excluded.knowledge_document_id_snapshot THEN excluded.updated_at ELSE research_bibliography_materials.updated_at END`,
		materialID, bibliographyID, projectID, attachmentID, documentID, attachmentID, documentID, attachmentSHA, kind, level, formatTime(at), formatTime(at))
	if err != nil {
		return fmt.Errorf("bind research bibliography material: %w", err)
	}
	return nil
}

func (r *ResearchRepository) GetBibliography(ctx context.Context, projectID, candidateID string) (appresearch.Bibliography, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return appresearch.Bibliography{}, err
	}
	defer tx.Rollback()
	if err := syncResearchBibliography(ctx, tx, strings.TrimSpace(projectID), strings.TrimSpace(candidateID), time.Now().UTC()); err != nil {
		return appresearch.Bibliography{}, err
	}
	value, err := loadBibliography(ctx, tx, projectID, candidateID)
	if err != nil {
		return value, err
	}
	if err := tx.Commit(); err != nil {
		return value, err
	}
	return value, nil
}

func (r *ResearchRepository) CitationSnapshotForAttachment(ctx context.Context, projectID, attachmentID string, at time.Time) (appresearch.CitationSnapshot, error) {
	projectID, attachmentID = strings.TrimSpace(projectID), strings.TrimSpace(attachmentID)
	var bibliographyID, canonicalJSON string
	var revision int
	var level appresearch.EvidenceLevel
	err := r.db.QueryRowContext(ctx, `SELECT b.id,b.revision_number,b.canonical_json,m.evidence_level FROM research_bibliography_materials m JOIN research_bibliographies b ON b.id=m.bibliography_id AND b.project_id=m.project_id WHERE m.project_id=? AND (m.attachment_id=? OR m.attachment_id_snapshot=?) ORDER BY m.updated_at DESC,m.id LIMIT 1`, projectID, attachmentID, attachmentID).Scan(&bibliographyID, &revision, &canonicalJSON, &level)
	if errors.Is(err, sql.ErrNoRows) {
		return appresearch.CitationSnapshot{}, fmt.Errorf("citation attachment is not bound to a research bibliography")
	}
	if err != nil {
		return appresearch.CitationSnapshot{}, fmt.Errorf("load citation bibliography snapshot: %w", err)
	}
	var data appresearch.BibliographyData
	if json.Unmarshal([]byte(canonicalJSON), &data) != nil {
		return appresearch.CitationSnapshot{}, fmt.Errorf("citation bibliography snapshot is invalid")
	}
	encoded, err := json.Marshal(appresearch.BibliographySnapshot{
		SchemaVersion: 1, BibliographyID: bibliographyID, Revision: revision,
		Data: data, CapturedAt: at.UTC(),
	})
	if err != nil {
		return appresearch.CitationSnapshot{}, err
	}
	return appresearch.CitationSnapshot{BibliographyID: bibliographyID, Bibliography: encoded, EvidenceLevel: level}, nil
}

func (r *ResearchRepository) ReviseBibliography(ctx context.Context, command appresearch.ReviseBibliographyCommand, at time.Time) (appresearch.Bibliography, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return appresearch.Bibliography{}, err
	}
	defer tx.Rollback()
	if err := syncResearchBibliography(ctx, tx, command.ProjectID, command.CandidateID, at); err != nil {
		return appresearch.Bibliography{}, err
	}
	current, err := loadBibliography(ctx, tx, command.ProjectID, command.CandidateID)
	if err != nil {
		return appresearch.Bibliography{}, err
	}
	changed := []string{}
	for _, field := range appresearch.BibliographyFields() {
		before, _ := appresearch.BibliographyFieldValue(current.Data, field)
		after, _ := appresearch.BibliographyFieldValue(command.Data, field)
		if !bytes.Equal(before, after) {
			changed = append(changed, field)
		}
	}
	if len(changed) == 0 {
		if err := tx.Commit(); err != nil {
			return current, err
		}
		return current, nil
	}
	nextRevision := current.Revision + 1
	for _, field := range changed {
		previous, _ := appresearch.BibliographyFieldValue(current.Data, field)
		next, _ := appresearch.BibliographyFieldValue(command.Data, field)
		revisionID, idErr := id.New()
		if idErr != nil {
			return appresearch.Bibliography{}, idErr
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO research_bibliography_revisions(id,bibliography_id,revision_number,field_name,previous_json,next_json,source_kind,source_record_id_snapshot,reason,created_at) VALUES (?,?,?,?,?,?,'user_edit','',?,?)`, revisionID, current.ID, nextRevision, field, string(previous), string(next), command.Reason, formatTime(at)); err != nil {
			return appresearch.Bibliography{}, fmt.Errorf("save bibliography revision: %w", err)
		}
		current.SelectedSources[field] = "user"
	}
	canonical, _ := json.Marshal(command.Data)
	selected, _ := json.Marshal(current.SelectedSources)
	if _, err := tx.ExecContext(ctx, `UPDATE research_bibliographies SET revision_number=?,canonical_json=?,selected_sources_json=?,updated_at=? WHERE id=? AND project_id=? AND revision_number=?`, nextRevision, string(canonical), string(selected), formatTime(at), current.ID, command.ProjectID, current.Revision); err != nil {
		return appresearch.Bibliography{}, fmt.Errorf("update research bibliography: %w", err)
	}
	updated, err := loadBibliography(ctx, tx, command.ProjectID, command.CandidateID)
	if err != nil {
		return updated, err
	}
	if err := tx.Commit(); err != nil {
		return updated, err
	}
	return updated, nil
}

func (r *ResearchRepository) SelectBibliographySource(ctx context.Context, command appresearch.SelectBibliographySourceCommand, at time.Time) (appresearch.Bibliography, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return appresearch.Bibliography{}, err
	}
	defer tx.Rollback()
	if err := syncResearchBibliography(ctx, tx, command.ProjectID, command.CandidateID, at); err != nil {
		return appresearch.Bibliography{}, err
	}
	current, err := loadBibliography(ctx, tx, command.ProjectID, command.CandidateID)
	if err != nil {
		return current, err
	}
	var raw, sourceRecordSnapshot string
	err = tx.QueryRowContext(ctx, `SELECT bfs.value_json,bfs.source_record_id_snapshot FROM research_bibliography_field_sources bfs JOIN research_candidate_records cr ON cr.source_record_id=bfs.source_record_id WHERE bfs.bibliography_id=? AND bfs.field_name=? AND bfs.source_record_id=? AND cr.candidate_id=? ORDER BY bfs.observed_at DESC,bfs.id DESC LIMIT 1`, current.ID, command.Field, command.SourceRecordID, command.CandidateID).Scan(&raw, &sourceRecordSnapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return current, fmt.Errorf("selected bibliography field source was not found")
	}
	if err != nil {
		return current, err
	}
	previous, _ := appresearch.BibliographyFieldValue(current.Data, command.Field)
	if err := appresearch.SetBibliographyField(&current.Data, command.Field, json.RawMessage(raw)); err != nil {
		return current, err
	}
	current.Data = appresearch.NormalizeBibliography(current.Data)
	next, _ := appresearch.BibliographyFieldValue(current.Data, command.Field)
	if bytes.Equal(previous, next) && current.SelectedSources[command.Field] == "source:"+command.SourceRecordID {
		if err := tx.Commit(); err != nil {
			return current, err
		}
		return current, nil
	}
	nextRevision := current.Revision + 1
	revisionID, err := id.New()
	if err != nil {
		return current, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO research_bibliography_revisions(id,bibliography_id,revision_number,field_name,previous_json,next_json,source_kind,source_record_id_snapshot,reason,created_at) VALUES (?,?,?,?,?,?,'source_selection',?,?,?)`, revisionID, current.ID, nextRevision, command.Field, string(previous), string(next), sourceRecordSnapshot, command.Reason, formatTime(at)); err != nil {
		return current, fmt.Errorf("save bibliography source selection: %w", err)
	}
	current.SelectedSources[command.Field] = "source:" + command.SourceRecordID
	canonical, _ := json.Marshal(current.Data)
	selected, _ := json.Marshal(current.SelectedSources)
	if _, err := tx.ExecContext(ctx, `UPDATE research_bibliographies SET revision_number=?,canonical_json=?,selected_sources_json=?,updated_at=? WHERE id=? AND project_id=? AND revision_number=?`, nextRevision, string(canonical), string(selected), formatTime(at), current.ID, command.ProjectID, current.Revision); err != nil {
		return current, err
	}
	updated, err := loadBibliography(ctx, tx, command.ProjectID, command.CandidateID)
	if err != nil {
		return updated, err
	}
	if err := tx.Commit(); err != nil {
		return updated, err
	}
	return updated, nil
}

func loadBibliography(ctx context.Context, queryer artifactTxQueryer, projectID, candidateID string) (appresearch.Bibliography, error) {
	var value appresearch.Bibliography
	var canonical, selected, created, updated string
	err := queryer.QueryRowContext(ctx, `SELECT id,project_id,candidate_id,revision_number,canonical_json,selected_sources_json,created_at,updated_at FROM research_bibliographies WHERE project_id=? AND candidate_id=?`, strings.TrimSpace(projectID), strings.TrimSpace(candidateID)).Scan(&value.ID, &value.ProjectID, &value.CandidateID, &value.Revision, &canonical, &selected, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return value, fmt.Errorf("research bibliography not found")
	}
	if err != nil {
		return value, err
	}
	if err := json.Unmarshal([]byte(canonical), &value.Data); err != nil {
		return value, err
	}
	if err := json.Unmarshal([]byte(selected), &value.SelectedSources); err != nil {
		return value, err
	}
	if value.SelectedSources == nil {
		value.SelectedSources = map[string]string{}
	}
	value.CreatedAt, err = parseTime(created)
	if err == nil {
		value.UpdatedAt, err = parseTime(updated)
	}
	if err != nil {
		return value, err
	}
	if err := loadBibliographyRelations(ctx, queryer, &value); err != nil {
		return value, err
	}
	return value, nil
}

func loadBibliographyRelations(ctx context.Context, queryer artifactTxQueryer, value *appresearch.Bibliography) error {
	rows, err := queryer.QueryContext(ctx, `SELECT id,field_name,COALESCE(source_record_id,''),source_id_snapshot,source_record_id_snapshot,value_json,value_sha256,observed_at FROM research_bibliography_field_sources WHERE bibliography_id=? ORDER BY field_name,observed_at,id`, value.ID)
	if err != nil {
		return err
	}
	value.FieldSources = []appresearch.BibliographyFieldSource{}
	for rows.Next() {
		var item appresearch.BibliographyFieldSource
		var raw, observed string
		if err := rows.Scan(&item.ID, &item.Field, &item.SourceRecordID, &item.SourceIDSnapshot, &item.SourceRecordIDSnapshot, &raw, &item.ValueSHA256, &observed); err != nil {
			_ = rows.Close()
			return err
		}
		item.Value = json.RawMessage(raw)
		item.ObservedAt, err = parseTime(observed)
		if err != nil {
			_ = rows.Close()
			return err
		}
		value.FieldSources = append(value.FieldSources, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = queryer.QueryContext(ctx, `SELECT id,revision_number,field_name,previous_json,next_json,source_kind,source_record_id_snapshot,reason,created_at FROM research_bibliography_revisions WHERE bibliography_id=? ORDER BY revision_number,field_name,id`, value.ID)
	if err != nil {
		return err
	}
	value.Revisions = []appresearch.BibliographyRevision{}
	for rows.Next() {
		var item appresearch.BibliographyRevision
		var previous, next, created string
		if err := rows.Scan(&item.ID, &item.Revision, &item.Field, &previous, &next, &item.SourceKind, &item.SourceRecordIDSnapshot, &item.Reason, &created); err != nil {
			_ = rows.Close()
			return err
		}
		item.Previous, item.Next = json.RawMessage(previous), json.RawMessage(next)
		item.CreatedAt, err = parseTime(created)
		if err != nil {
			_ = rows.Close()
			return err
		}
		value.Revisions = append(value.Revisions, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = queryer.QueryContext(ctx, `SELECT id,COALESCE(attachment_id,''),COALESCE(knowledge_document_id,''),attachment_id_snapshot,knowledge_document_id_snapshot,attachment_sha256_snapshot,import_kind,evidence_level,created_at,updated_at FROM research_bibliography_materials WHERE bibliography_id=? ORDER BY created_at,id`, value.ID)
	if err != nil {
		return err
	}
	value.Materials = []appresearch.BibliographyMaterial{}
	for rows.Next() {
		var item appresearch.BibliographyMaterial
		var created, updated string
		if err := rows.Scan(&item.ID, &item.AttachmentID, &item.KnowledgeDocumentID, &item.AttachmentIDSnapshot, &item.KnowledgeDocumentIDSnapshot, &item.AttachmentSHA256Snapshot, &item.ImportKind, &item.EvidenceLevel, &created, &updated); err != nil {
			_ = rows.Close()
			return err
		}
		item.CreatedAt, err = parseTime(created)
		if err == nil {
			item.UpdatedAt, err = parseTime(updated)
		}
		if err != nil {
			_ = rows.Close()
			return err
		}
		value.Materials = append(value.Materials, item)
	}
	return rows.Close()
}

func (r *ResearchRepository) ListEvidence(ctx context.Context, projectID, candidateID string) ([]appresearch.EvidenceEntry, error) {
	bibliography, err := r.GetBibliography(ctx, projectID, candidateID)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id,project_id,bibliography_id,field_kind,content,provenance,review_status,evidence_level,COALESCE(attachment_id,''),COALESCE(knowledge_document_id,''),attachment_id_snapshot,knowledge_document_id_snapshot,index_version_id_snapshot,chunk_id_snapshot,source_name_snapshot,locator_snapshot,quote_text_snapshot,quote_sha256,source_start,source_end,created_at,updated_at FROM research_evidence_entries WHERE project_id=? AND bibliography_id=? ORDER BY field_kind,created_at,id`, projectID, bibliography.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []appresearch.EvidenceEntry{}
	for rows.Next() {
		value, err := scanEvidenceEntry(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *ResearchRepository) SaveEvidence(ctx context.Context, command appresearch.SaveEvidenceCommand, snapshot *appresearch.EvidenceSnapshot, level appresearch.EvidenceLevel, at time.Time) (appresearch.EvidenceEntry, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return appresearch.EvidenceEntry{}, err
	}
	defer tx.Rollback()
	if err := syncResearchBibliography(ctx, tx, command.ProjectID, command.CandidateID, at); err != nil {
		return appresearch.EvidenceEntry{}, err
	}
	var bibliographyID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM research_bibliographies WHERE project_id=? AND candidate_id=?`, command.ProjectID, command.CandidateID).Scan(&bibliographyID); err != nil {
		return appresearch.EvidenceEntry{}, err
	}
	value := appresearch.EvidenceSnapshot{}
	if snapshot != nil {
		value = *snapshot
	}
	evidenceID, err := id.New()
	if err != nil {
		return appresearch.EvidenceEntry{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO research_evidence_entries(id,project_id,bibliography_id,field_kind,content,provenance,review_status,evidence_level,attachment_id,knowledge_document_id,attachment_id_snapshot,knowledge_document_id_snapshot,index_version_id_snapshot,chunk_id_snapshot,source_name_snapshot,locator_snapshot,quote_text_snapshot,quote_sha256,source_start,source_end,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),?,?,?,?,?,?,?,?,?,?,?,?)`,
		evidenceID, command.ProjectID, bibliographyID, command.Field, command.Content, command.Provenance, command.ReviewStatus, level, value.AttachmentID, value.DocumentID, value.AttachmentID, value.DocumentID, value.IndexVersionID, value.ChunkID, value.SourceName, value.Locator, value.Quote, value.QuoteSHA256, value.SourceStart, value.SourceEnd, formatTime(at), formatTime(at))
	if err != nil {
		return appresearch.EvidenceEntry{}, fmt.Errorf("save evidence matrix entry: %w", err)
	}
	result := appresearch.EvidenceEntry{ID: evidenceID, ProjectID: command.ProjectID, BibliographyID: bibliographyID, Field: command.Field, Content: command.Content, Provenance: command.Provenance, ReviewStatus: command.ReviewStatus, EvidenceLevel: level, CreatedAt: at, UpdatedAt: at}
	if snapshot != nil {
		copyValue := *snapshot
		result.Evidence = &copyValue
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func (r *ResearchRepository) ReviewEvidence(ctx context.Context, command appresearch.ReviewEvidenceCommand, at time.Time) (appresearch.EvidenceEntry, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return appresearch.EvidenceEntry{}, err
	}
	defer tx.Rollback()
	var bibliographyID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM research_bibliographies WHERE project_id=? AND candidate_id=?`, command.ProjectID, command.CandidateID).Scan(&bibliographyID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return appresearch.EvidenceEntry{}, fmt.Errorf("research bibliography not found")
		}
		return appresearch.EvidenceEntry{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE research_evidence_entries SET review_status=?,updated_at=? WHERE id=? AND project_id=? AND bibliography_id=?`,
		command.ReviewStatus, formatTime(at), command.EvidenceID, command.ProjectID, bibliographyID)
	if err != nil {
		return appresearch.EvidenceEntry{}, fmt.Errorf("review evidence matrix entry: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return appresearch.EvidenceEntry{}, fmt.Errorf("evidence matrix entry not found")
	}
	row := tx.QueryRowContext(ctx, `SELECT id,project_id,bibliography_id,field_kind,content,provenance,review_status,evidence_level,COALESCE(attachment_id,''),COALESCE(knowledge_document_id,''),attachment_id_snapshot,knowledge_document_id_snapshot,index_version_id_snapshot,chunk_id_snapshot,source_name_snapshot,locator_snapshot,quote_text_snapshot,quote_sha256,source_start,source_end,created_at,updated_at FROM research_evidence_entries WHERE id=? AND project_id=? AND bibliography_id=?`, command.EvidenceID, command.ProjectID, bibliographyID)
	value, err := scanEvidenceEntry(row)
	if err != nil {
		return appresearch.EvidenceEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return appresearch.EvidenceEntry{}, err
	}
	return value, nil
}

func (r *ResearchRepository) DeleteEvidence(ctx context.Context, projectID, candidateID, evidenceID string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM research_evidence_entries WHERE id=? AND project_id=? AND bibliography_id=(SELECT id FROM research_bibliographies WHERE project_id=? AND candidate_id=?)`, evidenceID, projectID, projectID, candidateID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("evidence matrix entry not found")
	}
	return nil
}

func scanEvidenceEntry(row rowScanner) (appresearch.EvidenceEntry, error) {
	var item appresearch.EvidenceEntry
	var liveAttachmentID, liveDocumentID, attachmentID, documentID, indexID, chunkID, sourceName, locator, quote, quoteSHA, created, updated string
	var sourceStart, sourceEnd int
	if err := row.Scan(&item.ID, &item.ProjectID, &item.BibliographyID, &item.Field, &item.Content, &item.Provenance, &item.ReviewStatus, &item.EvidenceLevel, &liveAttachmentID, &liveDocumentID, &attachmentID, &documentID, &indexID, &chunkID, &sourceName, &locator, &quote, &quoteSHA, &sourceStart, &sourceEnd, &created, &updated); err != nil {
		return item, err
	}
	if chunkID != "" || quote != "" {
		item.Evidence = &appresearch.EvidenceSnapshot{IndexVersionID: indexID, DocumentID: documentID, AttachmentID: attachmentID, ChunkID: chunkID, SourceName: sourceName, Locator: locator, Quote: quote, QuoteSHA256: quoteSHA, SourceStart: sourceStart, SourceEnd: sourceEnd}
	}
	var err error
	item.CreatedAt, err = parseTime(created)
	if err == nil {
		item.UpdatedAt, err = parseTime(updated)
	}
	return item, err
}
