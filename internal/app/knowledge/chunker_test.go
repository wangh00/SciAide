package knowledge

import (
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/wangh00/SciAide/internal/document"
)

func TestChunkBoundariesCoverOriginalWithoutSplittingWordsOrDecimals(t *testing.T) {
	for _, value := range []string{
		"  \n" + strings.Repeat("Exercise interventions had higher drop-out rates (risk ratio 1.31; CI 1.09 to 1.57). Heterogeneity was 0.03.\n\n", 80),
		strings.Repeat("interventions ", 800),
		strings.Repeat("运动改善抑郁症状。研究仍需核验；", 600),
		strings.Repeat("x", 5000),
	} {
		runes := []rune(value)
		spans := splitUnitContent(value)
		covered := make([]bool, len(runes))
		for i, s := range spans {
			if s.Content != string(runes[s.Start:s.End]) || s.End-s.Start > maximumChunkRunes {
				t.Fatal("inexact/unbounded source span")
			}
			if i > 0 && s.Start <= spans[i-1].Start {
				t.Fatal("no progress")
			}
			for j := s.Start; j < s.End; j++ {
				covered[j] = true
			}
			if strings.Contains(value, " ") && s.Start > 0 && unicode.IsLetter(runes[s.Start-1]) && unicode.IsLetter(runes[s.Start]) {
				t.Fatal("split word at overlap")
			}
			if s.End < len(runes) && runes[s.End-1] == '.' && unicode.IsDigit(runes[s.End]) {
				t.Fatal("split decimal")
			}
		}
		for i, r := range runes {
			if !unicode.IsSpace(r) && !covered[i] {
				t.Fatal("source gap")
			}
		}
	}
	if strongBoundary([]rune("1.31"), 1) {
		t.Fatal("decimal treated as sentence")
	}
}

func TestChunkVersionChangesIdentityNotOldSnapshot(t *testing.T) {
	parsed := document.Parsed{SchemaVersion: document.SchemaVersion, Units: []document.Unit{{Index: 1, Locator: "page:1", Content: "Exact source."}}}
	doc := Document{ID: "doc", ParserSchemaVersion: document.SchemaVersion, ChunkingVersion: "bounded-unit-v2"}
	old, _ := buildChunks(doc, parsed)
	doc.ChunkingVersion = ChunkingVersion
	current, _ := buildChunks(doc, parsed)
	if old[0].ID == current[0].ID || old[0].Content != "Exact source." {
		t.Fatal("index identity not isolated")
	}
}

func TestPendingV2JobRetainsOriginalOffsets(t *testing.T) {
	content := "  " + strings.Repeat("interventions ", 400)
	parsed := document.Parsed{SchemaVersion: document.SchemaVersion, Units: []document.Unit{{Index: 1, Locator: "page:1", Content: content}}}
	doc := Document{ID: "doc", ParserSchemaVersion: document.SchemaVersion, ChunkingVersion: "bounded-unit-v2"}
	old, _ := buildChunks(doc, parsed)
	// v2 trimmed the unit, cut at the first space >=1200, then overlapped
	// exactly 80 runes even when that started inside an English word.
	if old[0].SourceStart != 0 || old[0].SourceEnd != 1203 || old[1].SourceStart != 1123 {
		t.Fatalf("v2 changed: %+v", old[:2])
	}
	doc.ChunkingVersion = ChunkingVersion
	current, _ := buildChunks(doc, parsed)
	if current[0].SourceStart != 2 || current[1].SourceStart == old[1].SourceStart {
		t.Fatal("new algorithm not isolated")
	}
}

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
