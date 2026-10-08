package knowledge

import (
	"context"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/attachment"
)

// ReadIndexedEvidence reads intact, verified chunks in document order. Unlike
// keyword retrieval it can reach a page that contains no matching search term.
// Offset counts chunks in the selected locator, not bytes or search ranks.
func (s *Service) ReadIndexedEvidence(ctx context.Context, projectID, taskID, attachmentID, locator string, offset int) ([]EvidenceChunk, bool, error) {
	if offset < 0 {
		return nil, false, fmt.Errorf("invalid evidence offset")
	}
	projectID, taskID = strings.TrimSpace(projectID), strings.TrimSpace(taskID)
	if taskID != "" {
		if err := s.validateTask(ctx, projectID, taskID); err != nil {
			return nil, false, err
		}
	}
	selectedProject, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return nil, false, err
	}
	version, found, err := s.repository.ReadyVersion(ctx, projectID)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, fmt.Errorf("verified evidence index is not ready")
	}
	documents, err := s.repository.ListDocuments(ctx, projectID)
	if err != nil {
		return nil, false, err
	}
	var chosen *Document
	for i := range documents {
		d := &documents[i]
		allowed := d.ScopeKind == attachment.ScopeProjectShared || taskID != "" && d.ScopeKind == attachment.ScopeTask && d.ResearchTaskID == taskID
		if d.AttachmentID == attachmentID && allowed && d.Status == DocumentReady && d.IndexVersionID == version.ID {
			chosen = d
			break
		}
	}
	if chosen == nil {
		return nil, false, fmt.Errorf("attachment has no ready index in this task")
	}
	index, err := openProjectIndex(ctx, selectedProject, version)
	if err != nil {
		return nil, false, err
	}
	defer index.Close()
	return index.readEvidencePage(ctx, chosen.ID, attachmentID, locator, offset)
}

func (i *projectIndex) readEvidencePage(ctx context.Context, documentID, attachmentID, locator string, offset int) ([]EvidenceChunk, bool, error) {
	// Two 1,600-rune chunks fit in the bounded model result together with
	// source identities and menu continuations. Read one extra ID for hasMore.
	rows, err := i.db.QueryContext(ctx, `SELECT id FROM chunks WHERE document_id=? AND attachment_id=? AND (?='' OR locator=?) ORDER BY ordinal LIMIT 3 OFFSET ?`, documentID, attachmentID, locator, locator, offset)
	if err != nil {
		return nil, false, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, false, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	if len(ids) == 0 && offset == 0 && locator != "" {
		return nil, false, fmt.Errorf("indexed locator not found; inspect the selected document first")
	}
	values := []EvidenceChunk{}
	for _, id := range ids[:min(2, len(ids))] {
		value, err := i.EvidenceChunk(ctx, documentID, attachmentID, id)
		if err != nil {
			return nil, false, err
		}
		values = append(values, value)
	}
	return values, len(ids) > 2, nil
}
