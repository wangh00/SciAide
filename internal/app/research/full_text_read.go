package research

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/document"
)

// Read a selected task candidate for targeted verification without replacing
// the task's imported attachment or its frozen citation index.
func (s *DiscoveryService) FullTextAvailability(candidate Candidate) string {
	if reader, ok := s.materializer.(interface{ FullTextAvailability(Candidate) string }); ok {
		return reader.FullTextAvailability(candidate)
	}
	return "unknown"
}

func (s *DiscoveryService) ReadCandidateFullText(ctx context.Context, projectID, taskID, candidateID, query string) (map[string]any, error) {
	if strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("full-text verification requires a research task")
	}
	query = strings.TrimSpace(query)
	if n := utf8.RuneCountInString(query); n < 3 || n > 300 {
		return nil, fmt.Errorf("full-text verification query must contain 3 to 300 characters")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, ok := s.repository.(interface {
		CandidateForTask(context.Context, string, string, string) (Candidate, error)
	}); !ok {
		return nil, fmt.Errorf("task-scoped full-text verification is unavailable")
	}
	c, err := s.GetCandidateForTask(ctx, projectID, candidateID, taskID)
	if err != nil {
		return nil, err
	}
	if c.ReviewStatus != ReviewIncluded {
		return nil, fmt.Errorf("full-text verification requires a selected candidate")
	}
	p, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if s.materializer == nil {
		return nil, fmt.Errorf("full-text reader unavailable")
	}
	snapshot, _ := json.Marshal(c.Records)
	key := fmt.Sprintf("%s/%s/%s/%x", projectID, taskID, candidateID, sha256.Sum256(snapshot))
	if parsed, sha, ok := s.fullTexts.get(key); ok {
		return fullTextResult(ctx, c, parsed, sha, query, true)
	}
	material, err := s.materializer.Materialize(ctx, p, c, MaterializeFullText)
	if err != nil {
		var unavailable interface{ FullTextUnavailable() bool }
		if ctx.Err() == nil && errors.As(err, &unavailable) && unavailable.FullTextUnavailable() {
			return map[string]any{"candidateId": c.ID, "title": c.Preferred.Title, "status": "unavailable", "reason": err.Error(), "fullTextRead": false, "citationStatus": "verification_unavailable_preserve_existing_evidence", "retryRecommended": false}, nil
		}
		return nil, err
	}
	defer s.materializer.Cleanup(material)
	parsed, err := document.Parse(ctx, material.Path, document.FormatPDF)
	if err != nil {
		return nil, err
	}
	s.fullTexts.put(key, parsed, material.SHA256)
	return fullTextResult(ctx, c, parsed, material.SHA256, query, false)
}

func fullTextResult(ctx context.Context, c Candidate, parsed document.Parsed, sha, query string, cached bool) (map[string]any, error) {
	units, err := selectFullTextUnits(ctx, parsed.Units, query)
	if err != nil {
		return nil, err
	}
	return map[string]any{"candidateId": c.ID, "title": c.Preferred.Title, "status": "available", "doi": c.Preferred.Identifiers.DOI, "sha256": sha, "cacheHit": cached, "units": units, "matchedUnits": len(units), "fullTextRead": false, "citationStatus": "verification_only_not_an_issued_citation", "parserTruncated": parsed.Truncated, "selectionLimited": true}, nil
}

func selectFullTextUnits(ctx context.Context, source []document.Unit, query string) ([]document.Unit, error) {
	terms := strings.Fields(strings.ToLower(query))
	units := []document.Unit{}
	used := 0
	for _, u := range source {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		match := false
		lower := strings.ToLower(u.Content)
		for _, term := range terms {
			if strings.Contains(lower, term) {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		if used+len([]rune(u.Content)) > 10000 {
			continue
		}
		units = append(units, u)
		used += len([]rune(u.Content))
		if len(units) >= 6 {
			break
		}
	}
	return units, nil
}
