package document

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

var docxHeadingNamePattern = regexp.MustCompile(`(?i)(?:heading|标题)\s*([1-9])`)

type docxStyle struct {
	name            string
	basedOn         string
	outlineLevel    int
	hasOutlineLevel bool
}

type docxParagraph struct {
	text            strings.Builder
	styleID         string
	outlineLevel    int
	hasOutlineLevel bool
	list            bool
	inProperties    bool
	inText          bool
}

func parseDOCX(ctx context.Context, path string) (Parsed, error) {
	archive, err := openSafeArchive(path)
	if err != nil {
		return Parsed{}, err
	}
	defer archive.close()
	contents, err := archive.read("word/document.xml", maxArchiveEntryBytes)
	if err != nil {
		return Parsed{}, fmt.Errorf("DOCX document body: %w", err)
	}
	styles, err := readDOCXStyles(archive)
	if err != nil {
		return Parsed{}, err
	}
	metadata := readOpenXMLCoreProperties(archive)
	parsed, err := parseDOCXBody(ctx, contents, styles)
	if err != nil {
		return Parsed{}, err
	}
	for key, value := range parsed.metadata {
		metadata[key] = value
	}
	if metadata["title"] == "" && parsed.detectedTitle != "" {
		metadata["title"] = parsed.detectedTitle
	}
	metadata["structureParser"] = "docx-v2"
	return Parsed{
		Title: metadata["title"], Units: parsed.collect.units, Metadata: metadata,
		Truncated: parsed.collect.truncated, ExtractedRunes: parsed.collect.runes,
	}, nil
}

type docxBodyResult struct {
	collect       collector
	metadata      map[string]string
	detectedTitle string
}

func parseDOCXBody(ctx context.Context, contents []byte, styles map[string]docxStyle) (docxBodyResult, error) {
	decoder := xml.NewDecoder(bytes.NewReader(contents))
	decoder.Strict = true
	result := docxBodyResult{metadata: map[string]string{}}
	var paragraph *docxParagraph
	var headings []string
	var rowCells []strings.Builder
	paragraphNumber, tableNumber, rowNumber, cellNumber := 0, 0, 0, 0
	tableDepth, deletedDepth := 0, 0
	headingCount, listCount, tableRowCount := 0, 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return docxBodyResult{}, err
		}
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return docxBodyResult{}, fmt.Errorf("parse DOCX XML: %w", err)
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "del":
				deletedDepth++
			case "tbl":
				if tableDepth == 0 {
					tableNumber++
					rowNumber = 0
				}
				tableDepth++
			case "tr":
				if tableDepth == 1 {
					rowNumber++
					cellNumber = 0
					rowCells = nil
				}
			case "tc":
				if tableDepth == 1 {
					cellNumber++
					rowCells = append(rowCells, strings.Builder{})
				}
			case "p":
				paragraphNumber++
				paragraph = &docxParagraph{outlineLevel: -1}
			case "pPr":
				if paragraph != nil {
					paragraph.inProperties = true
				}
			case "pStyle":
				if paragraph != nil && paragraph.inProperties {
					paragraph.styleID = xmlAttribute(value, "val")
				}
			case "outlineLvl":
				if paragraph != nil && paragraph.inProperties {
					if level, parseErr := strconv.Atoi(xmlAttribute(value, "val")); parseErr == nil && level >= 0 && level <= 8 {
						paragraph.outlineLevel, paragraph.hasOutlineLevel = level, true
					}
				}
			case "numPr":
				if paragraph != nil && paragraph.inProperties {
					paragraph.list = true
				}
			case "numId":
				if paragraph != nil && paragraph.inProperties && xmlAttribute(value, "val") == "0" {
					paragraph.list = false
				}
			case "t":
				if paragraph != nil && deletedDepth == 0 {
					paragraph.inText = true
				}
			case "tab":
				if paragraph != nil && deletedDepth == 0 {
					paragraph.text.WriteByte('\t')
				}
			case "br", "cr":
				if paragraph != nil && deletedDepth == 0 {
					paragraph.text.WriteByte('\n')
				}
			}
		case xml.CharData:
			if paragraph != nil && paragraph.inText && deletedDepth == 0 {
				paragraph.text.Write([]byte(value))
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "t":
				if paragraph != nil {
					paragraph.inText = false
				}
			case "pPr":
				if paragraph != nil {
					paragraph.inProperties = false
				}
			case "p":
				if paragraph == nil {
					continue
				}
				content := normalizeExplicitText(paragraph.text.String())
				if tableDepth > 0 && len(rowCells) > 0 {
					cell := &rowCells[len(rowCells)-1]
					if content != "" {
						if cell.Len() > 0 {
							cell.WriteString(" / ")
						}
						cell.WriteString(strings.ReplaceAll(content, "\n", " / "))
					}
				} else if content != "" {
					level := docxParagraphHeadingLevel(*paragraph, styles)
					kind, title := "paragraph", sectionPath(headings)
					if level > 0 {
						headings = updateSectionHeadings(headings, level, content)
						kind, title = "heading", sectionPath(headings)
						headingCount++
					} else if docxTitleStyle(paragraph.styleID, styles) {
						kind, title = "title", content
						if result.detectedTitle == "" {
							result.detectedTitle = content
						}
					} else if paragraph.list {
						kind = "list_item"
						listCount++
					} else if docxCaptionStyle(paragraph.styleID, styles) {
						kind = "caption"
					}
					if !result.collect.add(kind, "paragraph:"+strconv.Itoa(paragraphNumber), title, content) {
						return finalizeDOCXBodyResult(result, paragraphNumber, tableNumber, headingCount, listCount, tableRowCount), nil
					}
				}
				paragraph = nil
			case "tr":
				if tableDepth == 1 {
					if row := formatDOCXTableRow(rowCells); row != "" {
						tableRowCount++
						if !result.collect.add("table_row", fmt.Sprintf("table:%d/row:%d", tableNumber, rowNumber), sectionPath(headings), row) {
							return finalizeDOCXBodyResult(result, paragraphNumber, tableNumber, headingCount, listCount, tableRowCount), nil
						}
					}
					rowCells = nil
				}
			case "tbl":
				if tableDepth > 0 {
					tableDepth--
				}
			case "del":
				if deletedDepth > 0 {
					deletedDepth--
				}
			}
		}
	}
	return finalizeDOCXBodyResult(result, paragraphNumber, tableNumber, headingCount, listCount, tableRowCount), nil
}

func finalizeDOCXBodyResult(result docxBodyResult, paragraphs, tables, headings, listItems, tableRows int) docxBodyResult {
	result.metadata["paragraphs"] = strconv.Itoa(paragraphs)
	result.metadata["tables"] = strconv.Itoa(tables)
	result.metadata["headings"] = strconv.Itoa(headings)
	result.metadata["listItems"] = strconv.Itoa(listItems)
	result.metadata["tableRows"] = strconv.Itoa(tableRows)
	return result
}

func formatDOCXTableRow(cells []strings.Builder) string {
	if len(cells) == 0 {
		return ""
	}
	values := make([]string, len(cells))
	nonEmpty := false
	for index := range cells {
		values[index] = strings.ReplaceAll(normalizeExplicitText(cells[index].String()), "|", `\|`)
		if values[index] != "" {
			nonEmpty = true
		}
	}
	if !nonEmpty {
		return ""
	}
	return "| " + strings.Join(values, " | ") + " |"
}

func updateSectionHeadings(headings []string, level int, value string) []string {
	level = min(max(level, 1), 9)
	if len(headings) < level {
		headings = append(headings, make([]string, level-len(headings))...)
	} else {
		headings = headings[:level]
	}
	headings[level-1] = strings.TrimSpace(value)
	return headings
}

func docxParagraphHeadingLevel(paragraph docxParagraph, styles map[string]docxStyle) int {
	if paragraph.hasOutlineLevel {
		return paragraph.outlineLevel + 1
	}
	return docxStyleHeadingLevel(paragraph.styleID, styles, map[string]struct{}{})
}

func docxStyleHeadingLevel(styleID string, styles map[string]docxStyle, seen map[string]struct{}) int {
	key := strings.ToLower(strings.TrimSpace(styleID))
	if key == "" {
		return 0
	}
	if _, duplicate := seen[key]; duplicate {
		return 0
	}
	seen[key] = struct{}{}
	style, exists := styles[key]
	if exists && style.hasOutlineLevel {
		return style.outlineLevel + 1
	}
	for _, candidate := range []string{styleID, style.name} {
		match := docxHeadingNamePattern.FindStringSubmatch(candidate)
		if len(match) == 2 {
			if level, err := strconv.Atoi(match[1]); err == nil {
				return level
			}
		}
	}
	if exists {
		return docxStyleHeadingLevel(style.basedOn, styles, seen)
	}
	return 0
}

func docxTitleStyle(styleID string, styles map[string]docxStyle) bool {
	return docxStyleNamed(styleID, styles, "title", "标题")
}

func docxCaptionStyle(styleID string, styles map[string]docxStyle) bool {
	return docxStyleNamed(styleID, styles, "caption", "题注")
}

func docxStyleNamed(styleID string, styles map[string]docxStyle, names ...string) bool {
	style, exists := styles[strings.ToLower(strings.TrimSpace(styleID))]
	candidates := []string{strings.ToLower(strings.TrimSpace(styleID))}
	if exists {
		candidates = append(candidates, strings.ToLower(strings.TrimSpace(style.name)))
	}
	for _, candidate := range candidates {
		for _, name := range names {
			if candidate == strings.ToLower(name) {
				return true
			}
		}
	}
	return false
}

func readDOCXStyles(archive *safeArchive) (map[string]docxStyle, error) {
	if _, exists := archive.files["word/styles.xml"]; !exists {
		return map[string]docxStyle{}, nil
	}
	contents, err := archive.read("word/styles.xml", 8<<20)
	if err != nil {
		return nil, fmt.Errorf("read DOCX styles: %w", err)
	}
	decoder := xml.NewDecoder(bytes.NewReader(contents))
	decoder.Strict = true
	styles := map[string]docxStyle{}
	styleID := ""
	current := docxStyle{outlineLevel: -1}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse DOCX styles: %w", err)
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "style":
				styleID = xmlAttribute(value, "styleId")
				current = docxStyle{outlineLevel: -1}
			case "name":
				if styleID != "" {
					current.name = xmlAttribute(value, "val")
				}
			case "basedOn":
				if styleID != "" {
					current.basedOn = xmlAttribute(value, "val")
				}
			case "outlineLvl":
				if styleID != "" {
					if level, parseErr := strconv.Atoi(xmlAttribute(value, "val")); parseErr == nil && level >= 0 && level <= 8 {
						current.outlineLevel, current.hasOutlineLevel = level, true
					}
				}
			}
		case xml.EndElement:
			if value.Name.Local == "style" && styleID != "" {
				styles[strings.ToLower(styleID)] = current
				styleID = ""
			}
		}
	}
	return styles, nil
}

func readOpenXMLCoreProperties(archive *safeArchive) map[string]string {
	metadata := map[string]string{}
	if _, exists := archive.files["docProps/core.xml"]; !exists {
		return metadata
	}
	contents, err := archive.read("docProps/core.xml", 1<<20)
	if err != nil {
		return metadata
	}
	allowed := map[string]string{
		"title": "title", "subject": "subject", "creator": "creator", "keywords": "keywords",
		"description": "description", "lastModifiedBy": "lastModifiedBy", "created": "created", "modified": "modified",
	}
	decoder := xml.NewDecoder(bytes.NewReader(contents))
	current := ""
	var content strings.Builder
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch value := token.(type) {
		case xml.StartElement:
			if key, exists := allowed[value.Name.Local]; exists {
				current = key
				content.Reset()
			}
		case xml.CharData:
			if current != "" {
				content.Write([]byte(value))
			}
		case xml.EndElement:
			if key, exists := allowed[value.Name.Local]; exists && current == key {
				text := []rune(normalizeLine(content.String()))
				if len(text) > 4096 {
					text = text[:4096]
				}
				if len(text) > 0 {
					metadata[key] = string(text)
				}
				current = ""
			}
		}
	}
	return metadata
}

func xmlAttribute(element xml.StartElement, localName string) string {
	for _, attribute := range element.Attr {
		if attribute.Name.Local == localName {
			return strings.TrimSpace(attribute.Value)
		}
	}
	return ""
}
