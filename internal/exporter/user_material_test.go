package exporter

import (
	"strings"
	"testing"
)

func TestUserMaterialReferenceDisclosesUnverifiedBibliography(t *testing.T) {
	value := Citation{SourceName: "notes.txt", Locator: "paragraph 2", Bibliography: Bibliography{Title: "notes.txt", WorkType: "user_material"}}
	for _, style := range []CitationStyle{CitationAPA7, CitationGB7714} {
		result := formatReference(value, 1, style)
		if !strings.Contains(result, "用户提供资料") || !strings.Contains(result, "paragraph 2") || strings.Contains(result, "[J]") {
			t.Fatalf("undisclosed local source: %s", result)
		}
	}
}
