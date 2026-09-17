package knowledge

import (
	"context"
	"fmt"
	"strings"
)

func (s *Service) SearchResearchEvidence(ctx context.Context, projectID, taskID string, o SearchOptions) (SearchResult, error) {
	if len(o.DocumentIDs) != 1 || o.Limit < 1 || o.Limit > 3 {
		return SearchResult{}, fmt.Errorf("research evidence requires one document and at most three chunks")
	}
	o.EvidenceMode = true
	if taskID != "" {
		return s.SearchWithTask(ctx, projectID, taskID, o)
	}
	return s.SearchWithOptions(ctx, projectID, o)
}

// Research citations use intact verified chunks, never UI search snippets.
func (i *projectIndex) completeResearchEvidence(ctx context.Context, matches []Match, o SearchOptions) ([]Match, error) {
	if len(o.DocumentIDs) != 1 {
		return matches, nil
	}
	metadata := false
	sectionQuotes := map[string]string{}
	var sourceName string
	if err := i.db.QueryRowContext(ctx, `SELECT original_name FROM documents WHERE document_id=?`, o.DocumentIDs[0]).Scan(&sourceName); err == nil {
		metadata = strings.Contains(strings.ToLower(sourceName), "-metadata.")
	}
	for _, m := range matches {
		metadata = metadata || strings.Contains(strings.ToLower(m.Name), "-metadata.")
	}
	if metadata {
		rows, err := i.db.QueryContext(ctx, `SELECT c.id,c.attachment_id FROM chunks c WHERE c.document_id=? ORDER BY c.ordinal LIMIT 65`, o.DocumentIDs[0])
		if err != nil {
			return nil, err
		}
		type identity struct{ id, attachment string }
		ids := []identity{}
		for rows.Next() {
			var v identity
			if err := rows.Scan(&v.id, &v.attachment); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		candidates := []Match{}
		inAbstract := false
		finished := false
		for _, id := range ids {
			if finished {
				break
			}
			c, err := i.EvidenceChunk(ctx, o.DocumentIDs[0], id.attachment, id.id)
			if err != nil {
				return nil, err
			}
			text := c.Content
			start := strings.Index(text, "## Abstract")
			end := strings.Index(text, "## Source records")
			relevant := inAbstract || start >= 0
			if start >= 0 {
				inAbstract = true
			}
			if end >= 0 {
				finished = true
				inAbstract = false
				if start < 0 && strings.TrimSpace(text[:end]) == "" {
					relevant = false
				}
			}
			if !relevant || strings.Contains(text, "No abstract was supplied by the selected public source.") {
				continue
			}
			from, to := 0, len(text)
			if start >= 0 {
				from = start + len("## Abstract")
			}
			if end >= from {
				to = end
			}
			quote := strings.TrimSpace(text[from:to])
			if quote == "" {
				continue
			}
			candidates = append(candidates, Match{ChunkID: c.ChunkID, DocumentID: c.DocumentID, AttachmentID: c.AttachmentID, Name: c.SourceName, MIMEType: c.MIMEType, Locator: c.Locator, Title: c.Title, Snippet: quote, SourceStart: c.SourceStart, SourceEnd: c.SourceEnd})
			sectionQuotes[c.ChunkID] = quote
		}
		if len(candidates) > 0 {
			matches = candidates
		}
	}
	if len(matches) > o.Limit {
		matches = matches[:o.Limit]
	}
	for n := range matches {
		m := &matches[n]
		c, err := i.EvidenceChunk(ctx, m.DocumentID, m.AttachmentID, m.ChunkID)
		if err != nil {
			return nil, err
		}
		if quote, ok := sectionQuotes[m.ChunkID]; ok {
			if !strings.Contains(c.Content, quote) {
				return nil, fmt.Errorf("abstract chunk changed")
			}
			m.Snippet = quote
		} else {
			m.Snippet = c.Content
		}
		m.SourceStart = c.SourceStart
		m.SourceEnd = c.SourceEnd
		m.Title = c.Title
		m.Rank = n + 1
	}
	return matches, nil
}
