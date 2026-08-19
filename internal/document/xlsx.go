package document

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

type workbookSheet struct {
	Name  string
	RelID string
}

func parseXLSX(ctx context.Context, filePath string) (Parsed, error) {
	archive, err := openSafeArchive(filePath)
	if err != nil {
		return Parsed{}, err
	}
	defer archive.close()
	sheets, err := readWorkbookSheets(archive)
	if err != nil {
		return Parsed{}, err
	}
	relations, err := readWorkbookRelations(archive)
	if err != nil {
		return Parsed{}, err
	}
	shared, err := readSharedStrings(archive)
	if err != nil {
		return Parsed{}, err
	}
	collect := collector{}
	for _, sheet := range sheets {
		if err := ctx.Err(); err != nil {
			return Parsed{}, err
		}
		target, exists := relations[sheet.RelID]
		if !exists {
			return Parsed{}, fmt.Errorf("XLSX worksheet relation %q is missing", sheet.RelID)
		}
		if err := parseWorksheet(ctx, archive, target, sheet.Name, shared, &collect); err != nil {
			return Parsed{}, err
		}
		if collect.truncated {
			break
		}
	}
	metadata := map[string]string{"sheets": strconv.Itoa(len(sheets))}
	for key, value := range readOpenXMLCoreProperties(archive) {
		metadata[key] = value
	}
	return Parsed{Title: metadata["title"], Units: collect.units, Metadata: metadata, Truncated: collect.truncated, ExtractedRunes: collect.runes}, nil
}

func readWorkbookSheets(archive *safeArchive) ([]workbookSheet, error) {
	contents, err := archive.read("xl/workbook.xml", 8<<20)
	if err != nil {
		return nil, fmt.Errorf("XLSX workbook: %w", err)
	}
	decoder := xml.NewDecoder(bytes.NewReader(contents))
	result := make([]workbookSheet, 0)
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse XLSX workbook: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "sheet" {
			continue
		}
		var sheet workbookSheet
		for _, attribute := range start.Attr {
			switch attribute.Name.Local {
			case "name":
				sheet.Name = strings.TrimSpace(attribute.Value)
			case "id":
				sheet.RelID = strings.TrimSpace(attribute.Value)
			}
		}
		if sheet.Name != "" && sheet.RelID != "" {
			result = append(result, sheet)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("XLSX contains no worksheets")
	}
	return result, nil
}

func readWorkbookRelations(archive *safeArchive) (map[string]string, error) {
	contents, err := archive.read("xl/_rels/workbook.xml.rels", 8<<20)
	if err != nil {
		return nil, fmt.Errorf("XLSX workbook relations: %w", err)
	}
	decoder := xml.NewDecoder(bytes.NewReader(contents))
	result := map[string]string{}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse XLSX workbook relations: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "Relationship" {
			continue
		}
		id, target, relationType := "", "", ""
		for _, attribute := range start.Attr {
			switch attribute.Name.Local {
			case "Id":
				id = attribute.Value
			case "Target":
				target = strings.ReplaceAll(attribute.Value, "\\", "/")
			case "Type":
				relationType = attribute.Value
			}
		}
		if id == "" || target == "" || !strings.HasSuffix(relationType, "/worksheet") {
			continue
		}
		clean := path.Clean(path.Join("xl", target))
		if strings.HasPrefix(target, "/") {
			clean = path.Clean(strings.TrimPrefix(target, "/"))
		}
		if clean == ".." || strings.HasPrefix(clean, "../") || !strings.HasPrefix(clean, "xl/") {
			return nil, fmt.Errorf("XLSX worksheet relation escapes the archive")
		}
		result[id] = clean
	}
	return result, nil
}

func readSharedStrings(archive *safeArchive) ([]string, error) {
	if _, exists := archive.files["xl/sharedStrings.xml"]; !exists {
		return []string{}, nil
	}
	contents, err := archive.read("xl/sharedStrings.xml", maxArchiveEntryBytes)
	if err != nil {
		return nil, err
	}
	decoder := xml.NewDecoder(bytes.NewReader(contents))
	result := make([]string, 0)
	inItem, inText := false, false
	var item strings.Builder
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse XLSX shared strings: %w", err)
		}
		switch value := token.(type) {
		case xml.StartElement:
			if value.Name.Local == "si" {
				inItem = true
				item.Reset()
			} else if value.Name.Local == "t" && inItem {
				inText = true
			}
		case xml.CharData:
			if inText {
				item.Write([]byte(value))
			}
		case xml.EndElement:
			if value.Name.Local == "t" {
				inText = false
			} else if value.Name.Local == "si" {
				result = append(result, item.String())
				inItem = false
			}
		}
	}
	return result, nil
}

func parseWorksheet(ctx context.Context, archive *safeArchive, entry, sheetName string, shared []string, collect *collector) error {
	contents, err := archive.read(entry, maxArchiveEntryBytes)
	if err != nil {
		return fmt.Errorf("read XLSX sheet %q: %w", sheetName, err)
	}
	decoder := xml.NewDecoder(bytes.NewReader(contents))
	rowNumber := 0
	cellRef, cellType := "", ""
	inCell, inValue, inFormula, inInlineText := false, false, false, false
	var value, formula, inline strings.Builder
	rowCells := make([]string, 0)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("parse XLSX sheet %q: %w", sheetName, err)
		}
		switch tokenValue := token.(type) {
		case xml.StartElement:
			switch tokenValue.Name.Local {
			case "row":
				rowNumber++
				for _, attribute := range tokenValue.Attr {
					if attribute.Name.Local == "r" {
						if parsed, parseErr := strconv.Atoi(attribute.Value); parseErr == nil && parsed > 0 {
							rowNumber = parsed
						}
					}
				}
				rowCells = rowCells[:0]
			case "c":
				inCell = true
				cellRef, cellType = "", ""
				value.Reset()
				formula.Reset()
				inline.Reset()
				for _, attribute := range tokenValue.Attr {
					if attribute.Name.Local == "r" {
						cellRef = attribute.Value
					} else if attribute.Name.Local == "t" {
						cellType = attribute.Value
					}
				}
			case "v":
				inValue = inCell
			case "f":
				inFormula = inCell
			case "t":
				inInlineText = inCell && cellType == "inlineStr"
			}
		case xml.CharData:
			if inValue {
				value.Write([]byte(tokenValue))
			} else if inFormula {
				formula.Write([]byte(tokenValue))
			} else if inInlineText {
				inline.Write([]byte(tokenValue))
			}
		case xml.EndElement:
			switch tokenValue.Name.Local {
			case "v":
				inValue = false
			case "f":
				inFormula = false
			case "t":
				inInlineText = false
			case "c":
				rendered := renderCellValue(cellType, value.String(), inline.String(), shared)
				if formula.Len() > 0 {
					rendered = "=" + formula.String() + " => " + rendered
				}
				rendered = strings.ReplaceAll(strings.ReplaceAll(rendered, "\r", " "), "\n", " ")
				if cellRef == "" {
					cellRef = fmt.Sprintf("cell-%d", len(rowCells)+1)
				}
				if rendered != "" {
					rowCells = append(rowCells, cellRef+"="+rendered)
				}
				inCell = false
			case "row":
				if len(rowCells) > 0 {
					locator := fmt.Sprintf("sheet:%s/row:%d", sheetName, rowNumber)
					if !collect.add("sheet_row", locator, sheetName, strings.Join(rowCells, "\t")) {
						return nil
					}
				}
			}
		}
	}
	return nil
}

func renderCellValue(cellType, cached, inline string, shared []string) string {
	switch cellType {
	case "s":
		index, err := strconv.Atoi(strings.TrimSpace(cached))
		if err == nil && index >= 0 && index < len(shared) {
			return shared[index]
		}
	case "inlineStr":
		return inline
	case "b":
		if strings.TrimSpace(cached) == "1" {
			return "TRUE"
		}
		return "FALSE"
	}
	return cached
}
