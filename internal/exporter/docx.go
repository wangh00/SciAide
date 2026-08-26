package exporter

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type docxPart struct {
	name string
	body string
}

func RenderDOCX(document Document) ([]byte, error) {
	created := document.CreatedAt.UTC().Format(time.RFC3339)
	parts := []docxPart{
		{"[Content_Types].xml", docxContentTypes},
		{"_rels/.rels", docxRootRelationships},
		{"docProps/core.xml", docxCoreProperties(document.Title, created)},
		{"docProps/app.xml", docxAppProperties},
		{"word/document.xml", docxDocument(document)},
		{"word/styles.xml", docxStyles},
		{"word/numbering.xml", docxNumbering},
		{"word/_rels/document.xml.rels", docxDocumentRelationships},
	}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, part := range parts {
		header := &zip.FileHeader{Name: part.name, Method: zip.Deflate}
		header.SetModTime(document.CreatedAt.UTC())
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write([]byte(part.body)); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func docxDocument(document Document) string {
	var body strings.Builder
	writeParagraph := func(text, style string, indent, numberID int, shade string) {
		body.WriteString(`<w:p><w:pPr>`)
		if style != "" {
			body.WriteString(`<w:pStyle w:val="` + xmlEscape(style) + `"/>`)
		}
		if indent > 0 {
			body.WriteString(`<w:ind w:left="` + strconv.Itoa(indent*360) + `"/>`)
		}
		if numberID > 0 {
			body.WriteString(`<w:numPr><w:ilvl w:val="` + strconv.Itoa(max(0, indent-1)) + `"/><w:numId w:val="` + strconv.Itoa(numberID) + `"/></w:numPr>`)
		}
		if shade != "" {
			body.WriteString(`<w:shd w:val="clear" w:color="auto" w:fill="` + shade + `"/>`)
		}
		body.WriteString(`</w:pPr><w:r>` + docxText(text) + `</w:r></w:p>`)
	}
	writeParagraph(document.Title, "Title", 0, 0, "")
	for _, block := range document.Blocks {
		switch block.Kind {
		case BlockHeading:
			writeParagraph(block.Text, "Heading"+strconv.Itoa(min(3, max(1, block.Level))), 0, 0, "")
		case BlockParagraph:
			writeParagraph(block.Text, "Normal", 0, 0, "")
		case BlockQuote:
			writeParagraph(block.Text, "Quote", 0, 0, "F2F3F5")
		case BlockCode:
			writeParagraph(block.Text, "Code", 0, 0, "F4F5F7")
		case BlockListItem:
			numberID := 1
			if block.Ordered {
				numberID = 2
			}
			writeParagraph(block.Text, "Normal", max(1, block.Level), numberID, "")
		case BlockTable:
			body.WriteString(docxTables(block.Rows))
		case BlockRule:
			body.WriteString(`<w:p><w:pPr><w:pBdr><w:bottom w:val="single" w:sz="4" w:space="1" w:color="B9BDC5"/></w:pBdr></w:pPr></w:p>`)
		}
	}
	if len(document.References) > 0 {
		writeParagraph("参考文献", "Heading1", 0, 0, "")
		for _, reference := range document.References {
			writeParagraph(reference.Entry, "Reference", 0, 0, "")
		}
	}
	writeParagraph(fmt.Sprintf("导出溯源：ArtifactVersion %s · SHA256 %s · %s", document.SourceVersion, document.SourceSHA256, document.Generator), "Metadata", 0, 0, "")
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body.String() + `<w:sectPr><w:pgSz w:w="12240" w:h="15840"/><w:pgMar w:top="1080" w:right="1080" w:bottom="1080" w:left="1080" w:header="720" w:footer="720" w:gutter="0"/></w:sectPr></w:body></w:document>`
}

func docxText(value string) string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(value, "\n")
	var output strings.Builder
	for index, line := range lines {
		if index > 0 {
			output.WriteString(`<w:br/>`)
		}
		output.WriteString(`<w:t xml:space="preserve">` + xmlEscape(line) + `</w:t>`)
	}
	return output.String()
}

func docxTables(rows [][]string) string {
	segments := tableColumnSegments(rows, maxRenderedTableColumns)
	var output strings.Builder
	for index, segment := range segments {
		if index > 0 {
			output.WriteString(`<w:p><w:pPr><w:spacing w:after="80"/></w:pPr></w:p>`)
		}
		output.WriteString(docxTableSegment(segment))
	}
	return output.String()
}

func docxTableSegment(rows [][]string) string {
	columns := 1
	for _, row := range rows {
		columns = max(columns, len(row))
	}
	width := 10080 / columns
	var value strings.Builder
	value.WriteString(`<w:tbl><w:tblPr><w:tblW w:w="10080" w:type="dxa"/><w:tblLayout w:type="fixed"/><w:tblBorders><w:top w:val="single" w:sz="4" w:color="C8CBD1"/><w:left w:val="single" w:sz="4" w:color="C8CBD1"/><w:bottom w:val="single" w:sz="4" w:color="C8CBD1"/><w:right w:val="single" w:sz="4" w:color="C8CBD1"/><w:insideH w:val="single" w:sz="4" w:color="D9DBE0"/><w:insideV w:val="single" w:sz="4" w:color="D9DBE0"/></w:tblBorders></w:tblPr><w:tblGrid>`)
	for range columns {
		value.WriteString(`<w:gridCol w:w="` + strconv.Itoa(width) + `"/>`)
	}
	value.WriteString(`</w:tblGrid>`)
	for rowIndex, row := range rows {
		value.WriteString(`<w:tr>`)
		if rowIndex == 0 {
			value.WriteString(`<w:trPr><w:tblHeader/></w:trPr>`)
		}
		for column := range columns {
			text := ""
			if column < len(row) {
				text = row[column]
			}
			shade := ""
			bold := ""
			if rowIndex == 0 {
				shade = `<w:shd w:val="clear" w:color="auto" w:fill="ECEEF2"/>`
				bold = `<w:b/>`
			}
			value.WriteString(`<w:tc><w:tcPr><w:tcW w:w="` + strconv.Itoa(width) + `" w:type="dxa"/>` + shade + `</w:tcPr><w:p><w:r><w:rPr>` + bold + `</w:rPr>` + docxText(text) + `</w:r></w:p></w:tc>`)
		}
		value.WriteString(`</w:tr>`)
	}
	value.WriteString(`</w:tbl>`)
	return value.String()
}

func docxCoreProperties(title, created string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><dc:title>` + xmlEscape(title) + `</dc:title><dc:creator>SciAide</dc:creator><cp:lastModifiedBy>SciAide</cp:lastModifiedBy><dcterms:created xsi:type="dcterms:W3CDTF">` + created + `</dcterms:created><dcterms:modified xsi:type="dcterms:W3CDTF">` + created + `</dcterms:modified></cp:coreProperties>`
}

func xmlEscape(value string) string {
	var output bytes.Buffer
	_ = xml.EscapeText(&output, []byte(strings.ReplaceAll(value, "\x00", "")))
	return output.String()
}

const docxContentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/><Override PartName="/word/numbering.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.numbering+xml"/><Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/><Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/></Types>`
const docxRootRelationships = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/></Relationships>`
const docxDocumentRelationships = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering" Target="numbering.xml"/></Relationships>`
const docxAppProperties = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties"><Application>SciAide</Application><AppVersion>1.0</AppVersion></Properties>`
const docxStyles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Arial" w:hAnsi="Arial" w:eastAsia="Microsoft YaHei"/><w:sz w:val="22"/><w:lang w:val="zh-CN" w:eastAsia="zh-CN"/></w:rPr></w:rPrDefault><w:pPrDefault><w:pPr><w:spacing w:after="120" w:line="360" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults><w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style><w:style w:type="paragraph" w:styleId="Title"><w:name w:val="Title"/><w:basedOn w:val="Normal"/><w:pPr><w:spacing w:after="240"/><w:outlineLvl w:val="0"/></w:pPr><w:rPr><w:b/><w:color w:val="202124"/><w:sz w:val="40"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="Heading 1"/><w:basedOn w:val="Normal"/><w:pPr><w:keepNext/><w:spacing w:before="240" w:after="100"/><w:outlineLvl w:val="0"/></w:pPr><w:rPr><w:b/><w:color w:val="2F3A4A"/><w:sz w:val="30"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="Heading 2"/><w:basedOn w:val="Normal"/><w:pPr><w:keepNext/><w:spacing w:before="200" w:after="80"/><w:outlineLvl w:val="1"/></w:pPr><w:rPr><w:b/><w:color w:val="3D4653"/><w:sz w:val="26"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Heading3"><w:name w:val="Heading 3"/><w:basedOn w:val="Normal"/><w:pPr><w:keepNext/><w:spacing w:before="160" w:after="60"/><w:outlineLvl w:val="2"/></w:pPr><w:rPr><w:b/><w:sz w:val="23"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Quote"><w:name w:val="Quote"/><w:basedOn w:val="Normal"/><w:pPr><w:ind w:left="360" w:right="240"/><w:spacing w:before="80" w:after="120"/></w:pPr><w:rPr><w:color w:val="555B66"/><w:i/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Code"><w:name w:val="Code"/><w:basedOn w:val="Normal"/><w:pPr><w:ind w:left="180" w:right="180"/><w:spacing w:before="80" w:after="120" w:line="300"/></w:pPr><w:rPr><w:rFonts w:ascii="Consolas" w:hAnsi="Consolas" w:eastAsia="Microsoft YaHei"/><w:sz w:val="19"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Reference"><w:name w:val="Reference"/><w:basedOn w:val="Normal"/><w:pPr><w:ind w:left="360" w:hanging="360"/><w:spacing w:after="80"/></w:pPr><w:rPr><w:sz w:val="20"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Metadata"><w:name w:val="Metadata"/><w:basedOn w:val="Normal"/><w:pPr><w:spacing w:before="240"/><w:pBdr><w:top w:val="single" w:sz="4" w:space="6" w:color="D6D8DD"/></w:pBdr></w:pPr><w:rPr><w:color w:val="777C86"/><w:sz w:val="17"/></w:rPr></w:style></w:styles>`
const docxNumbering = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:abstractNum w:abstractNumId="1"><w:multiLevelType w:val="multilevel"/><w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="bullet"/><w:lvlText w:val="•"/><w:lvlJc w:val="left"/><w:pPr><w:tabs><w:tab w:val="num" w:pos="360"/></w:tabs><w:ind w:left="360" w:hanging="180"/></w:pPr></w:lvl><w:lvl w:ilvl="1"><w:start w:val="1"/><w:numFmt w:val="bullet"/><w:lvlText w:val="◦"/><w:pPr><w:ind w:left="720" w:hanging="180"/></w:pPr></w:lvl></w:abstractNum><w:abstractNum w:abstractNumId="2"><w:multiLevelType w:val="multilevel"/><w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="decimal"/><w:lvlText w:val="%1."/><w:pPr><w:ind w:left="420" w:hanging="240"/></w:pPr></w:lvl><w:lvl w:ilvl="1"><w:start w:val="1"/><w:numFmt w:val="lowerLetter"/><w:lvlText w:val="%2)"/><w:pPr><w:ind w:left="780" w:hanging="240"/></w:pPr></w:lvl></w:abstractNum><w:num w:numId="1"><w:abstractNumId w:val="1"/></w:num><w:num w:numId="2"><w:abstractNumId w:val="2"/></w:num></w:numbering>`
