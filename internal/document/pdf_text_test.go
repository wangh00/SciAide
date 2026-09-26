package document

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pdfreader "github.com/ledongthuc/pdf"
)

func TestPDFPositionedTJWordsAndLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "positioned.pdf")
	writePDFStream(t, path, `BT /F1 12 Tf 72 720 Td [(Attention) -300 (Is) -300 (All)] TJ 0 -18 Td [(You) -300 (Need)] TJ ET`)
	p, err := Parse(context.Background(), path, FormatPDF)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Units) != 1 || p.Units[0].Content != "Attention Is All You Need" {
		t.Fatalf("positioned text=%+v", p)
	}
}

// Optional local-paper regression; the copyrighted PDF is not checked in.
func TestPDFLocalPaperRegression(t *testing.T) {
	path := os.Getenv("SCIAIDE_TEST_ATTENTION_PDF")
	if path == "" {
		t.Skip("local paper fixture not configured")
	}
	p, err := Parse(context.Background(), path, FormatPDF)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, u := range p.Units {
		text.WriteString(u.Content)
		text.WriteByte('\n')
	}
	for _, phrase := range []string{"Attention Is All You Need", "attention mechanism", "6.2 Model Variations", "28.4 BLEU", "41.8"} {
		if !strings.Contains(text.String(), phrase) {
			t.Errorf("missing phrase %q", phrase)
		}
	}
	if p.Metadata["textPages"] != "15" || p.Truncated {
		t.Fatalf("page coverage=%+v", p.Metadata)
	}
	for _, u := range p.Units {
		if strings.HasPrefix(u.Title, "6 512") || u.Title == "0.2" {
			t.Fatalf("numeric table heading: %+v", u)
		}
	}
}

func TestPDFGlyphSpacingAndColumnOrder(t *testing.T) {
	values := []pdfreader.Text{
		{S: "Attention", X: 10, Y: 100, W: 45, FontSize: 12},
		{S: "Is", X: 58, Y: 100, W: 10, FontSize: 12},
		{S: "All", X: 71, Y: 100, W: 12, FontSize: 12},
		{S: "You", X: 86, Y: 100, W: 16, FontSize: 12},
		{S: "Need", X: 105, Y: 100, W: 23, FontSize: 12},
		{S: "ef", X: 10, Y: 84, W: 8, FontSize: 12},
		{S: "ﬁcient", X: 18, Y: 84, W: 25, FontSize: 12},
		{S: "Right column", X: 250, Y: 100, W: 80, FontSize: 12},
		{S: "next line", X: 250, Y: 84, W: 60, FontSize: 12},
	}
	got, err := joinPDFGlyphs(context.Background(), values)
	if err != nil || got != "Attention Is All You Need\nefficient\nRight column\nnext line" {
		t.Fatalf("text=%q err=%v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = joinPDFGlyphs(ctx, values); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestPDFTableNumbersDoNotBecomeSections(t *testing.T) {
	blocks := splitPDFSections([]string{"Results", "The table follows.", "5.1625.158", "32", "0.2", "1.0 2.0 3.0", "6 512 2048 8 64 64 0.1 0.1 100K", "3.2 Model Architecture", "Attention is useful."})
	if len(blocks) != 2 || blocks[1].title != "3.2 Model Architecture" || !strings.Contains(blocks[0].content, "5.1625.158") {
		t.Fatalf("sections=%+v", blocks)
	}
}
