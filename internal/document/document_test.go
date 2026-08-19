package document

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTextDOCXAndXLSX(t *testing.T) {
	ctx := context.Background()
	textPath := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(textPath, []byte("# Method\nmeasurement alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	text, err := Parse(ctx, textPath, FormatMarkdown)
	if err != nil || len(text.Units) != 1 || !strings.Contains(text.Units[0].Content, "measurement alpha") {
		t.Fatalf("text parse = %#v, %v", text, err)
	}

	docxPath := filepath.Join(t.TempDir(), "paper.docx")
	writeTestArchive(t, docxPath, map[string]string{
		"[Content_Types].xml": `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`,
		"word/document.xml":   `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Introduction</w:t></w:r></w:p><w:tbl><w:tr><w:tc><w:p><w:r><w:t>Result 42</w:t></w:r></w:p></w:tc></w:tr></w:tbl></w:body></w:document>`,
		"docProps/core.xml":   `<cp:coreProperties xmlns:cp="x" xmlns:dc="y"><dc:title>Study</dc:title></cp:coreProperties>`,
	})
	docx, err := Parse(ctx, docxPath, FormatDOCX)
	if err != nil || docx.Title != "Study" || len(docx.Units) != 2 || docx.Units[1].Kind != "table_row" || docx.Units[1].Locator != "table:1/row:1" || docx.Units[1].Content != "| Result 42 |" {
		t.Fatalf("DOCX parse = %#v, %v", docx, err)
	}

	xlsxPath := filepath.Join(t.TempDir(), "data.xlsx")
	writeTestArchive(t, xlsxPath, map[string]string{
		"[Content_Types].xml":        `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`,
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Results" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>sample</t></si></sst>`,
		"xl/worksheets/sheet1.xml":   `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1"><f>SUM(20,22)</f><v>42</v></c></row></sheetData></worksheet>`,
	})
	xlsx, err := Parse(ctx, xlsxPath, FormatXLSX)
	if err != nil || len(xlsx.Units) != 1 || xlsx.Units[0].Locator != "sheet:Results/row:1" || !strings.Contains(xlsx.Units[0].Content, "A1=sample") || !strings.Contains(xlsx.Units[0].Content, "=SUM(20,22) => 42") {
		t.Fatalf("XLSX parse = %#v, %v", xlsx, err)
	}
}

func TestParseDOCXPreservesResearchStructure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "structured.docx")
	writeTestArchive(t, path, map[string]string{
		"[Content_Types].xml": `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`,
		"word/styles.xml": `<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
			<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style>
			<w:style w:type="paragraph" w:styleId="CustomMethod"><w:name w:val="Method section"/><w:basedOn w:val="Heading2"/></w:style>
			<w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:pPr><w:outlineLvl w:val="1"/></w:pPr></w:style>
		</w:styles>`,
		"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
			<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Introduction</w:t></w:r></w:p>
			<w:p><w:r><w:t>Background evidence.</w:t></w:r></w:p>
			<w:p><w:pPr><w:pStyle w:val="CustomMethod"/></w:pPr><w:r><w:t>Method</w:t></w:r></w:p>
			<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="7"/></w:numPr></w:pPr><w:r><w:t>Collect samples</w:t></w:r></w:p>
			<w:tbl><w:tr><w:tc><w:p><w:r><w:t>Group</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Value</w:t></w:r><w:r><w:tab/></w:r><w:r><w:t>42</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
		</w:body></w:document>`,
		"docProps/core.xml": `<cp:coreProperties xmlns:cp="x" xmlns:dc="y"><dc:title>Structured Study</dc:title><dc:creator>Researcher</dc:creator><dc:subject>Methods</dc:subject></cp:coreProperties>`,
	})

	parsed, err := Parse(context.Background(), path, FormatDOCX)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.SchemaVersion != SchemaVersion || parsed.Title != "Structured Study" || parsed.Metadata["creator"] != "Researcher" || parsed.Metadata["headings"] != "2" || parsed.Metadata["listItems"] != "1" || parsed.Metadata["tableRows"] != "1" {
		t.Fatalf("DOCX metadata = %#v", parsed)
	}
	if len(parsed.Units) != 5 || parsed.Units[0].Kind != "heading" || parsed.Units[1].Title != "Introduction" || parsed.Units[2].Title != "Introduction > Method" || parsed.Units[3].Kind != "list_item" || parsed.Units[4].Content != "| Group | Value 42 |" || parsed.Units[4].Title != "Introduction > Method" {
		t.Fatalf("DOCX units = %#v", parsed.Units)
	}
}

func TestPDFStructureRemovesRepeatedEdgesAndJoinsWrappedText(t *testing.T) {
	pages := []pdfPage{
		{number: 1, lines: normalizedSourceLines("Journal of Tests\n- 1 -\n一\n、\n引言\n生成式人工智\n能正在发展。")},
		{number: 2, lines: normalizedSourceLines("Journal of Tests\n- 2 -\nMethods\ninter-\nnational cohort")},
		{number: 3, lines: normalizedSourceLines("Journal of Tests\n- 3 -\nResults\nResult was reproducible.")},
	}
	repeated := repeatedPDFEdgeLines(pages)
	if _, ok := repeated["journal of tests"]; !ok {
		t.Fatalf("repeated edges = %#v", repeated)
	}
	first, removed := filterPDFEdgeLines(pages[0].lines, repeated)
	if removed != 2 || len(first) == 0 || first[0] != "一、引言" {
		t.Fatalf("filtered first page = %#v, removed=%d", first, removed)
	}
	sections := splitPDFSections(first)
	if len(sections) != 1 || sections[0].title != "一、引言" || sections[0].content != "一、引言生成式人工智能正在发展。" {
		t.Fatalf("first page sections = %#v", sections)
	}
	second, _ := filterPDFEdgeLines(pages[1].lines, repeated)
	sections = splitPDFSections(second)
	if len(sections) != 1 || sections[0].title != "Methods" || !strings.Contains(sections[0].content, "international cohort") {
		t.Fatalf("second page sections = %#v", sections)
	}
}

func TestParsePDFPreservesPageLocator(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paper.pdf")
	writeMinimalPDF(t, path, "Hello scientific PDF")
	parsed, err := Parse(context.Background(), path, FormatPDF)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Units) != 1 || parsed.Units[0].Locator != "page:1" || !strings.Contains(parsed.Units[0].Content, "Hello scientific PDF") {
		t.Fatalf("PDF parse = %#v", parsed)
	}
}

func TestParsePDFAllowsPublisherMetadataAfterEOF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "publisher-paper.pdf")
	writeMinimalPDF(t, path, "Research result")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("WebFastLoad\xef\xbb\xbf<FileProperty><FileName>paper</FileName></FileProperty>"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	parsed, err := Parse(context.Background(), path, FormatPDF)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Units) != 1 || !strings.Contains(parsed.Units[0].Content, "Research result") {
		t.Fatalf("PDF parse = %#v", parsed)
	}
}

func TestArchiveRejectsTraversal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unsafe.docx")
	writeTestArchive(t, path, map[string]string{"../outside": "bad", "word/document.xml": "<document/>"})
	if _, err := Parse(context.Background(), path, FormatDOCX); err == nil {
		t.Fatal("archive traversal was accepted")
	}
}

func writeTestArchive(t *testing.T, path string, files map[string]string) {
	t.Helper()
	output, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(output)
	for name, contents := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeMinimalPDF(t *testing.T, path, text string) {
	t.Helper()
	objects := []string{
		`<< /Type /Catalog /Pages 2 0 R >>`,
		`<< /Type /Pages /Kids [3 0 R] /Count 1 >>`,
		`<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>`,
		`<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>`,
	}
	stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", strings.ReplaceAll(text, ")", `\)`))
	objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	var output bytes.Buffer
	output.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for index := 1; index < len(offsets); index++ {
		fmt.Fprintf(&output, "%010d 00000 n \n", offsets[index])
	}
	fmt.Fprintf(&output, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	if err := os.WriteFile(path, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
