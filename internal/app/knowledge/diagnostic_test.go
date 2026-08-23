package knowledge

import (
	"testing"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/document"
)

func TestParseDiagnosticClassifiesCoverageAndProgress(t *testing.T) {
	poor := buildParseDiagnostic(attachment.Attachment{
		Format: document.FormatPDF, Status: attachment.StatusReady, SizeBytes: 100, UnitCount: 2, ExtractedRunes: 100,
		ParseMetadata: map[string]string{"pages": "10", "textPages": "2", "emptyPages": "8", "sections": "2", "structureParser": "pdf-v2"},
	})
	if poor.Quality != ParseQualityPoor || poor.Pages != 10 || poor.TextPages != 2 || len(poor.Warnings) == 0 || poor.Parser != "pdf-v2" {
		t.Fatalf("poor PDF diagnostic = %#v", poor)
	}
	good := buildParseDiagnostic(attachment.Attachment{
		Format: document.FormatDOCX, Status: attachment.StatusReady, UnitCount: 20, ExtractedRunes: 4000,
		ParseMetadata: map[string]string{"headings": "4", "tables": "2", "structureParser": "docx-v2"},
	})
	if good.Quality != ParseQualityGood || good.Headings != 4 || good.Tables != 2 || good.Summary == "" {
		t.Fatalf("good DOCX diagnostic = %#v", good)
	}
	job := &ImportJob{Status: JobRunning, Stage: "chunking"}
	if progress := jobProgress(DocumentIndexing, job); progress != 55 {
		t.Fatalf("chunking progress = %d", progress)
	}
}
