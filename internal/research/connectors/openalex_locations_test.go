package connectors

import "testing"

func TestOpenAlexPreservesOpenCopies(t *testing.T) {
	w := openAlexWork{ID: "https://openalex.org/W1", Title: "paper"}
	w.BestOpenAccess.PDFURL = "https://www.nature.com/a.pdf"
	w.Locations = []openAlexLocation{{PDFURL: w.BestOpenAccess.PDFURL, IsOA: true}, {PDFURL: "https://arxiv.org/pdf/1234.56789", IsOA: true}, {PDFURL: "https://example.org/closed.pdf"}}
	got := openAlexWorks([]openAlexWork{w})
	if len(got) != 1 || len(got[0].PDFURLs) != 2 || got[0].PDFURLs[1] != "https://arxiv.org/pdf/1234.56789" {
		t.Fatalf("copies=%+v", got)
	}
}
