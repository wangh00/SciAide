package knowledge

import (
	"slices"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/document"
)

func TestBoundedChunkingIsStableAndKeepsSourceSpans(t *testing.T) {
	content := strings.Repeat("The alpha kinase result remained reproducible across cohorts. ", 90)
	documentValue := Document{ID: "document", AttachmentID: "attachment", ParserSchemaVersion: document.SchemaVersion, ChunkingVersion: ChunkingVersion}
	parsed := document.Parsed{SchemaVersion: document.SchemaVersion, Units: []document.Unit{{Index: 1, Kind: "page", Locator: "page:4", Content: content}}}
	first, err := buildChunks(documentValue, parsed)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildChunks(documentValue, parsed)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) < 3 || len(first) != len(second) {
		t.Fatalf("chunk counts = %d and %d", len(first), len(second))
	}
	for index := range first {
		if len([]rune(first[index].Content)) > maximumChunkRunes || first[index].SourceEnd <= first[index].SourceStart || first[index].Locator != "page:4" {
			t.Fatalf("invalid bounded chunk = %#v", first[index])
		}
		if first[index].ID != second[index].ID || first[index].SourceStart != second[index].SourceStart || first[index].SourceEnd != second[index].SourceEnd {
			t.Fatalf("chunking is not stable: %#v != %#v", first[index], second[index])
		}
		if index > 0 && first[index].SourceStart >= first[index-1].SourceEnd {
			t.Fatalf("chunk overlap is missing between %d and %d", index-1, index)
		}
	}
}

func TestNormalizedTermsCoverChineseAndScientificIdentifiers(t *testing.T) {
	terms := normalizedTerms("蛋白质表达 BRCA1 H2O αSynuclein")
	for _, wanted := range []string{"蛋白", "白质", "质表", "表达", "brca1", "h2o", "αsynuclein"} {
		if !slices.Contains(terms, wanted) {
			t.Fatalf("normalized terms %v do not contain %q", terms, wanted)
		}
	}
	query := uniqueQueryTerms("蛋白表达 BRCA1")
	if len(query) != 4 || ftsQuery(query) != `"蛋白" OR "白表" OR "表达" OR "brca1"` {
		t.Fatalf("query terms = %v, FTS=%q", query, ftsQuery(query))
	}
}

func TestKnowledgeResultBudgetIsBounded(t *testing.T) {
	values := make([]Match, 20)
	for index := range values {
		values[index] = Match{Name: "paper.pdf", Locator: "page:1", Snippet: strings.Repeat("证据", 700)}
	}
	result := fitSearchResultBudget(values, maxSearchResultRunes)
	if len(result) == 0 || len(result) >= len(values) {
		t.Fatalf("bounded result count = %d", len(result))
	}
	used := 0
	for index, value := range result {
		used += len([]rune(value.Name)) + len([]rune(value.Locator)) + len([]rune(value.Title)) + len([]rune(value.Snippet)) + 64
		if value.Rank != index+1 || len([]rune(value.Snippet)) > maxSearchSnippetRunes+3 {
			t.Fatalf("bounded result item = %#v", value)
		}
	}
	if used > maxSearchResultRunes {
		t.Fatalf("knowledge result used %d runes", used)
	}
}
