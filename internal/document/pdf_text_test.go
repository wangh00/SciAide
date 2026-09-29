package document

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	pdfreader "github.com/ledongthuc/pdf"
)

func TestPDFSimpleFontUnicodeAndDifferences(t *testing.T) {
	for _, tc := range []struct{ name, encoding, cmap, stream, want string }{
		{"unicode", `/Encoding << /BaseEncoding /WinAnsiEncoding /Differences [16 /zero.tf 17 /f_f_i] >>`, `2 beginbfchar <10> <0036> <11> <006600660069> endbfchar`, `BT /F1 12 Tf 72 720 Td [(x) <10> <11>] TJ ET`, "x6ffi"},
		{"range", `/Encoding /WinAnsiEncoding`, `1 beginbfrange <10> <12> <0030> endbfrange`, `BT /F1 12 Tf 72 720 Td (x) Tj <101112> Tj ET`, "x012"},
		{"range-array", `/Encoding /WinAnsiEncoding`, `1 beginbfrange <10> <11> [<2212> <0035>] endbfrange`, `BT /F1 12 Tf 72 720 Td <1011> Tj ET`, "−5"},
		{"differences", `/Encoding << /BaseEncoding /WinAnsiEncoding /Differences [16 /six.tf /f_f /fi.g0330] >>`, "", `BT /F1 12 Tf 72 720 Td <10111292> Tj ET`, "6fffi’"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mapping.pdf")
			font := `<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica ` + tc.encoding
			if tc.cmap != "" {
				font += ` /ToUnicode 6 0 R`
			}
			font += ` >>`
			objects := []string{`<< /Type /Catalog /Pages 2 0 R >>`, `<< /Type /Pages /Kids [3 0 R] /Count 1 >>`, `<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>`, font, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(tc.stream), tc.stream)}
			if tc.cmap != "" {
				objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(tc.cmap), tc.cmap))
			}
			writePDFObjects(t, path, objects)
			p, err := Parse(context.Background(), path, FormatPDF)
			if err != nil || len(p.Units) != 1 || p.Units[0].Content != tc.want {
				t.Fatalf("parsed=%+v err=%v want=%q", p, err, tc.want)
			}
		})
	}
}

func TestPDFUnknownGlyphIsNotInvented(t *testing.T) {
	text, err := joinPDFGlyphs(context.Background(), []pdfreader.Text{{S: "\uf800", FontSize: 12}})
	if err != nil || text != "[无法解码字形:U+F800]" {
		t.Fatalf("%q %v", text, err)
	}
	if text, err := joinPDFGlyphs(context.Background(), []pdfreader.Text{{S: "\x13", FontSize: 12}}); err != nil || text != "[无法解码字形:U+0013]" {
		t.Fatal("unmapped control must be explicitly marked")
	}
}

func TestPDFBMJUnicodeRegression(t *testing.T) {
	path := os.Getenv("SCIAIDE_TEST_BMJ_PDF")
	if path == "" {
		t.Skip("local paper fixture not configured")
	}
	p, err := Parse(context.Background(), path, FormatPDF)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, unit := range p.Units {
		text.WriteString(unit.Content)
		text.WriteByte('\n')
	}
	for _, phrase := range []string{"n=1210", "−0.62", "−0.80 to −0.45", "n=1047", "−0.55", "n=643", "−0.49", "95%"} {
		if !strings.Contains(text.String(), phrase) {
			t.Errorf("missing verified original text %q", phrase)
		}
	}
	for _, r := range text.String() {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			t.Fatalf("unmapped character %U", r)
		}
	}
	if p.Metadata["textPages"] != "17" || p.Truncated {
		t.Fatalf("coverage=%v truncated=%v", p.Metadata, p.Truncated)
	}
	if output := os.Getenv("SCIAIDE_TEST_BMJ_PARSED_OUTPUT"); output != "" {
		data, err := json.MarshalIndent(p, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(output, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

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
