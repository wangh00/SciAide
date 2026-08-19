package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/document"
)

const SearchKnowledgeName = "builtin.knowledge.search"

type KnowledgeSearcher interface {
	SearchWithOptions(ctx context.Context, projectID string, options knowledge.SearchOptions) (knowledge.SearchResult, error)
}

type SearchKnowledge struct{ knowledge KnowledgeSearcher }

func NewSearchKnowledge(searcher KnowledgeSearcher) *SearchKnowledge {
	return &SearchKnowledge{knowledge: searcher}
}

func (*SearchKnowledge) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{
		QualifiedName: SearchKnowledgeName,
		Description:   "Search all explicitly imported documents in the current research project. Uses bounded FTS5/BM25 by default and optional hybrid semantic retrieval when configured. Returns compact ranked snippets with exact source locators and stable [K-...] references. Cite evidence only with the exact reference returned for that snippet. For project-source questions, search first, reformulate once when no result is found, and use builtin.document.read for deeper reading of selected evidence.",
		InputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false,"required":["query"],"properties":{"query":{"type":"string","minLength":1,"maxLength":200},"limit":{"type":"integer","minimum":1,"maximum":20},"documentIds":{"type":"array","maxItems":20,"items":{"type":"string","minLength":1,"maxLength":128}},"formats":{"type":"array","maxItems":6,"uniqueItems":true,"items":{"type":"string","enum":["pdf","docx","xlsx","text","markdown","csv"]}}}}`),
		OutputSchema:  json.RawMessage(`{"type":"object","required":["query","matches","totalMatches","status"],"properties":{"query":{"type":"string"},"matches":{"type":"array","items":{"type":"object"}},"totalMatches":{"type":"integer"},"status":{"type":"object"}}}`),
		Risk:          tool.RiskLow,
		Permissions:   []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "."}},
		Idempotent:    true,
		Version:       "3",
	}, nil
}

func (t *SearchKnowledge) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if t == nil || t.knowledge == nil {
		return tool.Result{}, fmt.Errorf("knowledge search is not configured")
	}
	var args struct {
		Query       string            `json:"query"`
		Limit       int               `json:"limit"`
		DocumentIDs []string          `json:"documentIds"`
		Formats     []document.Format `json:"formats"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	result, err := t.knowledge.SearchWithOptions(ctx, invocation.ProjectID, knowledge.SearchOptions{Query: args.Query, Limit: args.Limit, DocumentIDs: args.DocumentIDs, Formats: args.Formats})
	if err != nil {
		return tool.Result{}, err
	}
	type compactMatch struct {
		Reference     string   `json:"reference"`
		DocumentID    string   `json:"documentId"`
		AttachmentID  string   `json:"attachmentId"`
		Name          string   `json:"name"`
		Format        string   `json:"format"`
		Locator       string   `json:"locator"`
		Title         string   `json:"title,omitempty"`
		Rank          int      `json:"rank"`
		Score         float64  `json:"score"`
		MatchedTerms  []string `json:"matchedTerms,omitempty"`
		SourceStart   int      `json:"sourceStart"`
		SourceEnd     int      `json:"sourceEnd"`
		LexicalRank   int      `json:"lexicalRank,omitempty"`
		SemanticRank  int      `json:"semanticRank,omitempty"`
		SemanticScore float64  `json:"semanticScore,omitempty"`
	}
	compact := make([]compactMatch, 0, len(result.Matches))
	for _, item := range result.Matches {
		reference := citation.KnowledgeReference(invocation.RunID, item.IndexVersionID, item.ChunkID, citation.QuoteSHA256(item.Snippet))
		compact = append(compact, compactMatch{
			Reference: reference, DocumentID: item.DocumentID, AttachmentID: item.AttachmentID, Name: item.Name, Format: string(item.Format),
			Locator: item.Locator, Title: item.Title, Rank: item.Rank, Score: item.Score, MatchedTerms: item.MatchedTerms,
			SourceStart: item.SourceStart, SourceEnd: item.SourceEnd,
			LexicalRank: item.LexicalRank, SemanticRank: item.SemanticRank, SemanticScore: item.SemanticScore,
		})
	}
	structured, err := json.Marshal(struct {
		Query            string                  `json:"query"`
		Matches          []compactMatch          `json:"matches"`
		TotalMatches     int                     `json:"totalMatches"`
		Status           knowledge.ProjectStatus `json:"status"`
		RetrievalMode    string                  `json:"retrievalMode"`
		EmbeddingWarning string                  `json:"embeddingWarning,omitempty"`
	}{result.Query, compact, result.TotalMatches, result.Status, result.RetrievalMode, result.EmbeddingWarning})
	if err != nil {
		return tool.Result{}, err
	}
	var text strings.Builder
	if len(result.Matches) > 0 {
		text.WriteString("Use only the exact [K-...] marker attached to an excerpt when citing it. Never invent or alter a marker.\n\n")
	}
	citations := make([]tool.CitationRef, 0, len(result.Matches))
	artifacts := make([]tool.ArtifactRef, 0)
	seenArtifacts := map[string]struct{}{}
	for _, item := range result.Matches {
		quoteSHA256 := citation.QuoteSHA256(item.Snippet)
		reference := citation.KnowledgeReference(invocation.RunID, item.IndexVersionID, item.ChunkID, quoteSHA256)
		fmt.Fprintf(&text, "%s | rank %d | %s | %s\n%s\n\n", reference, item.Rank, item.Name, item.Locator, item.Snippet)
		citations = append(citations, tool.CitationRef{
			ID: item.AttachmentID, Kind: citation.KindKnowledgeChunk, Reference: reference,
			ProjectID: invocation.ProjectID, IndexVersionID: item.IndexVersionID, DocumentID: item.DocumentID,
			AttachmentID: item.AttachmentID, ChunkID: item.ChunkID, SourceName: item.Name, MIMEType: item.MIMEType,
			Locator: item.Locator, Title: item.Title, Quote: item.Snippet, QuoteSHA256: quoteSHA256,
			SourceStart: item.SourceStart, SourceEnd: item.SourceEnd,
		})
		if _, exists := seenArtifacts[item.AttachmentID]; !exists {
			seenArtifacts[item.AttachmentID] = struct{}{}
			artifacts = append(artifacts, tool.ArtifactRef{ID: item.AttachmentID, Name: item.Name, MIMEType: item.MIMEType})
		}
	}
	if len(result.Matches) == 0 {
		fmt.Fprintf(&text, "No matching snippets were found in %d indexed project documents.", result.Status.Ready)
	}
	if result.EmbeddingWarning != "" {
		fmt.Fprintf(&text, "\n\n%s", result.EmbeddingWarning)
	}
	return tool.Result{
		Status: tool.ResultSuccess, Text: strings.TrimSpace(text.String()), Structured: structured,
		Artifacts: artifacts, Citations: citations, Truncated: result.TotalMatches > len(result.Matches),
	}, nil
}
