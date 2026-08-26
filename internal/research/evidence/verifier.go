package evidence

import (
	"context"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/research"
)

type KnowledgeService interface {
	SearchWithOptions(ctx context.Context, projectID string, options knowledge.SearchOptions) (knowledge.SearchResult, error)
	ReadEvidenceChunk(ctx context.Context, projectID, indexVersionID, documentID, attachmentID, chunkID string) (knowledge.EvidenceChunk, error)
}

type Verifier struct{ knowledge KnowledgeService }

func New(service KnowledgeService) (*Verifier, error) {
	if service == nil {
		return nil, fmt.Errorf("knowledge evidence service is required")
	}
	return &Verifier{knowledge: service}, nil
}

func (v *Verifier) EvidenceChunk(ctx context.Context, projectID string, reference research.EvidenceReference) (research.EvidenceSnapshot, error) {
	value, err := v.knowledge.ReadEvidenceChunk(ctx, projectID, reference.IndexVersionID, reference.DocumentID, reference.AttachmentID, reference.ChunkID)
	if err != nil {
		return research.EvidenceSnapshot{}, err
	}
	quote := strings.TrimSpace(value.Content)
	return research.EvidenceSnapshot{
		IndexVersionID: value.IndexVersionID, DocumentID: value.DocumentID, AttachmentID: value.AttachmentID,
		ChunkID: value.ChunkID, SourceName: value.SourceName, Locator: value.Locator, Quote: quote,
		QuoteSHA256: citation.QuoteSHA256(quote), SourceStart: value.SourceStart, SourceEnd: value.SourceEnd,
	}, nil
}

func (v *Verifier) Search(ctx context.Context, projectID, query string, documentIDs []string) ([]research.EvidenceSearchMatch, error) {
	result, err := v.knowledge.SearchWithOptions(ctx, projectID, knowledge.SearchOptions{Query: query, Limit: 12, DocumentIDs: documentIDs})
	if err != nil {
		return nil, err
	}
	values := make([]research.EvidenceSearchMatch, 0, len(result.Matches))
	for _, value := range result.Matches {
		values = append(values, research.EvidenceSearchMatch{
			Reference:  research.EvidenceReference{IndexVersionID: value.IndexVersionID, DocumentID: value.DocumentID, AttachmentID: value.AttachmentID, ChunkID: value.ChunkID},
			SourceName: value.Name, Locator: value.Locator, Title: value.Title, Snippet: value.Snippet, Rank: value.Rank,
		})
	}
	return values, nil
}
