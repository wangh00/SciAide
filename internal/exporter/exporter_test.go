package exporter

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/document"
)

func TestRenderDocumentsAreDeterministicAndReadable(t *testing.T) {
	version := SourceVersion{
		ID: "version-1", SHA256: strings.Repeat("a", 64), CreatedAt: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC),
		Citations: []Citation{{Ordinal: 0, Reference: "[K-ABCDEF123456]", SourceName: "论文.pdf", Title: "可重复实验", Locator: "page:2", Quote: "可信证据", QuoteSHA256: strings.Repeat("b", 64)}},
	}
	source := []byte("# 研究结论\n\n中文内容由证据支持 [K-ABCDEF123456]，伪造标记 [K-000000000000]。\n\n```text\n第一行\n第二行\n```\n\n| 指标 | 结果 |\n| --- | --- |\n| 准确率 | 98% |")
	doc, err := BuildDocument("科研报告", source, version, CitationGB7714)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name   string
		format document.Format
		render func(Document) ([]byte, error)
	}{{"report.docx", document.FormatDOCX, RenderDOCX}, {"report.pdf", document.FormatPDF, RenderPDF}} {
		first, err := fixture.render(doc)
		if err != nil {
			t.Fatalf("%s render: %v", fixture.name, err)
		}
		second, err := fixture.render(doc)
		if err != nil || !bytes.Equal(first, second) {
			t.Fatalf("%s is not deterministic: bytes=%d/%d err=%v", fixture.name, len(first), len(second), err)
		}
		path := filepath.Join(t.TempDir(), fixture.name)
		if err := os.WriteFile(path, first, 0o600); err != nil {
			t.Fatal(err)
		}
		parsed, err := document.Parse(context.Background(), path, fixture.format)
		if err != nil {
			t.Fatalf("reopen %s: %v", fixture.name, err)
		}
		var text strings.Builder
		for _, unit := range parsed.Units {
			text.WriteString(unit.Content)
			text.WriteByte('\n')
		}
		got := text.String()
		if fixture.format == document.FormatPDF {
			for _, marker := range [][]byte{[]byte("/ToUnicode"), []byte("/FontFile2"), []byte("/Type /Page")} {
				if !bytes.Contains(first, marker) {
					t.Fatalf("%s does not contain required PDF structure %q", fixture.name, marker)
				}
			}
			if parsed.Metadata["pages"] == "" || parsed.ExtractedRunes == 0 {
				t.Fatalf("%s parse metadata = %#v, runes=%d", fixture.name, parsed.Metadata, parsed.ExtractedRunes)
			}
			continue
		}
		for _, want := range []string{"研究", "中文内容", "[1]", "未验证引用", "第一行\n第二行", "参考文献"} {
			if !strings.Contains(got, want) {
				t.Fatalf("%s extracted text does not contain %q: %q", fixture.name, want, got)
			}
		}
	}
}

func TestCitationRenderingPreservesStoredMarkersAndEvidence(t *testing.T) {
	marker := "[K-ABCDEF123456]"
	source := []byte("Evidence " + marker)
	version := SourceVersion{Citations: []Citation{{Ordinal: 0, Reference: marker, SourceName: "paper.pdf", Title: "Evidence", Quote: "Evidence"}}}
	for _, style := range []CitationStyle{CitationGB7714, CitationAPA7} {
		doc, err := BuildDocument("Report", source, version, style)
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Blocks) != 1 || strings.Contains(doc.Blocks[0].Text, marker) {
			t.Fatalf("reader-facing citation was not rendered: %#v", doc.Blocks)
		}
		if string(source) != "Evidence "+marker || version.Citations[0].Reference != marker {
			t.Fatal("rendering changed the stored source or citation snapshot")
		}
		if len(doc.References) != 1 || doc.References[0].Marker != marker || doc.References[0].Quote != "Evidence" {
			t.Fatalf("rendering lost the evidence mapping: %#v", doc.References)
		}
	}
}

func TestWideAndTallTablesRenderInBoundedSegments(t *testing.T) {
	header := make([]string, 64)
	row := make([]string, 64)
	for column := range header {
		header[column] = fmt.Sprintf("Column %d", column+1)
		row[column] = fmt.Sprintf("value-%d", column+1)
	}
	row[0] = "row-key"
	segments := tableColumnSegments([][]string{header, row}, maxRenderedTableColumns)
	if len(segments) != 9 {
		t.Fatalf("tableColumnSegments() count = %d", len(segments))
	}
	reassembled := append([]string(nil), segments[0][1]...)
	for index, segment := range segments {
		if len(segment) != 2 || len(segment[0]) > maxRenderedTableColumns {
			t.Fatalf("segment %d shape = %#v", index, segment)
		}
		if index > 0 {
			if segment[1][0] != "row-key" {
				t.Fatalf("segment %d did not repeat row identity", index)
			}
			reassembled = append(reassembled, segment[1][1:]...)
		}
	}
	if len(reassembled) != len(row) {
		t.Fatalf("reassembled columns = %d", len(reassembled))
	}
	for index := range row {
		if reassembled[index] != row[index] {
			t.Fatalf("reassembled column %d = %q, want %q", index, reassembled[index], row[index])
		}
	}

	longCell := "START-OF-CELL " + strings.Repeat("长单元格内容。Long cell content. ", 300) + " END-OF-CELL"
	documentValue, err := BuildStructuredDocument("Layout regression", []Block{
		{Kind: BlockTable, Rows: [][]string{header, row}},
		{Kind: BlockTable, Rows: [][]string{{"Key", "Details"}, {"long-row", longCell}}},
	}, SourceVersion{ID: "wide-version", SHA256: strings.Repeat("e", 64), CreatedAt: time.Unix(0, 0).UTC()}, CitationGB7714)
	if err != nil {
		t.Fatal(err)
	}
	docxBytes, err := RenderDOCX(documentValue)
	if err != nil {
		t.Fatal(err)
	}
	docxReader, err := zip.NewReader(bytes.NewReader(docxBytes), int64(len(docxBytes)))
	if err != nil {
		t.Fatal(err)
	}
	var documentXML []byte
	for _, file := range docxReader.File {
		if file.Name != "word/document.xml" {
			continue
		}
		input, openErr := file.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		documentXML, err = io.ReadAll(input)
		closeErr := input.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("read document.xml: %v / %v", err, closeErr)
		}
	}
	if tables := bytes.Count(documentXML, []byte("<w:tbl>")); tables != 10 {
		t.Fatalf("DOCX table segments = %d", tables)
	}
	if headers := bytes.Count(documentXML, []byte("<w:tblHeader/>")); headers != 10 {
		t.Fatalf("DOCX repeating headers = %d", headers)
	}

	pdfBytes, err := RenderPDF(documentValue)
	if err != nil {
		t.Fatal(err)
	}
	pdfPath := filepath.Join(t.TempDir(), "wide-tall.pdf")
	if err := os.WriteFile(pdfPath, pdfBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, err := document.Parse(context.Background(), pdfPath, document.FormatPDF)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := strconv.Atoi(parsed.Metadata["pages"])
	if err != nil || pages < 2 || parsed.ExtractedRunes == 0 {
		t.Fatalf("rendered PDF metadata = %#v, runes=%d", parsed.Metadata, parsed.ExtractedRunes)
	}
	var extracted strings.Builder
	for _, unit := range parsed.Units {
		extracted.WriteString(unit.Content)
	}
	if !strings.Contains(extracted.String(), "START-OF-CELL") || !strings.Contains(extracted.String(), "END-OF-CELL") {
		t.Fatalf("rendered PDF lost long-cell boundaries")
	}
}

func TestAPAReferenceDoesNotInventMissingMetadata(t *testing.T) {
	doc, err := BuildDocument("Report", []byte("Evidence [K-ABCDEF123456]."), SourceVersion{
		ID: "version", SHA256: strings.Repeat("c", 64), CreatedAt: time.Unix(0, 0).UTC(),
		Citations: []Citation{{Reference: "[K-ABCDEF123456]", SourceName: "paper.pdf", QuoteSHA256: strings.Repeat("d", 64)}},
	}, CitationAPA7)
	if err != nil || len(doc.References) != 1 {
		t.Fatalf("BuildDocument() = %#v, %v", doc, err)
	}
	if strings.Contains(doc.References[0].Entry, "未知作者") || !strings.Contains(doc.References[0].Entry, "paper.pdf. (n.d.).") || !strings.Contains(doc.References[0].Entry, "未提供") {
		t.Fatalf("APA entry = %q", doc.References[0].Entry)
	}
	if len(doc.Blocks) != 1 || !strings.Contains(doc.Blocks[0].Text, "(paper.pdf, n.d.)") || strings.Contains(doc.Blocks[0].Text, "[1]") {
		t.Fatalf("APA in-text citation = %#v", doc.Blocks)
	}
}

func TestCompleteBibliographyRendersGBTAndAPAWithoutGuessing(t *testing.T) {
	citation := Citation{
		Reference: "[K-ABCDEF123456]", EvidenceLevel: "full_text",
		Bibliography: Bibliography{Authors: []BibliographicAuthor{{Name: "Ada Lovelace"}, {Name: "Alan Turing"}}, Year: 2024, Title: "Reproducible Science", ContainerTitle: "Journal of Evidence", Volume: "12", Issue: "3", Pages: "44-59", DOI: "10.1000/example"},
	}
	for _, fixture := range []struct {
		style CitationStyle
		wants []string
	}{
		{CitationGB7714, []string{"[1] Ada Lovelace, Alan Turing.", "Reproducible Science[J].", "Journal of Evidence, 2024, 12(3): 44-59.", "DOI: 10.1000/example", "证据等级：本地全文"}},
		{CitationAPA7, []string{"Ada Lovelace, & Alan Turing (2024).", "Reproducible Science.", "Journal of Evidence, 12(3), 44-59.", "https://doi.org/10.1000/example", "证据等级：本地全文"}},
	} {
		doc, err := BuildDocument("Report", []byte("Evidence [K-ABCDEF123456]."), SourceVersion{ID: "version", SHA256: strings.Repeat("e", 64), CreatedAt: time.Unix(0, 0).UTC(), Citations: []Citation{citation}}, fixture.style)
		if err != nil || len(doc.References) != 1 {
			t.Fatalf("document=%#v err=%v", doc, err)
		}
		for _, want := range fixture.wants {
			if !strings.Contains(doc.References[0].Entry, want) {
				t.Fatalf("%s entry missing %q: %q", fixture.style, want, doc.References[0].Entry)
			}
		}
	}
}
