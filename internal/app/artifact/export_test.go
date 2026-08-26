package artifact

import (
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/document"
)

func TestValidateParsedDocumentExportRejectsTruncatedSource(t *testing.T) {
	err := validateParsedDocumentExport(document.Parsed{
		Truncated:      true,
		ExtractedRunes: document.MaxExtractedRunes,
		Units:          []document.Unit{{Kind: "paragraph", Content: "partial content"}},
	})
	if err == nil || !strings.Contains(err.Error(), "complete document export limit") {
		t.Fatalf("validateParsedDocumentExport() error = %v", err)
	}
}
