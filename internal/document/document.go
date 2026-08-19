package document

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	SchemaVersion     = 2
	MaxExtractedRunes = 8_000_000
)

type Format string

const (
	FormatText     Format = "text"
	FormatMarkdown Format = "markdown"
	FormatCSV      Format = "csv"
	FormatPDF      Format = "pdf"
	FormatDOCX     Format = "docx"
	FormatXLSX     Format = "xlsx"
)

type Unit struct {
	Index   int    `json:"index"`
	Kind    string `json:"kind"`
	Locator string `json:"locator"`
	Title   string `json:"title,omitempty"`
	Content string `json:"content"`
}

type Parsed struct {
	SchemaVersion  int               `json:"schemaVersion"`
	Format         Format            `json:"format"`
	Title          string            `json:"title,omitempty"`
	Units          []Unit            `json:"units"`
	Metadata       map[string]string `json:"metadata"`
	Truncated      bool              `json:"truncated"`
	ExtractedRunes int               `json:"extractedRunes"`
}

func Parse(ctx context.Context, path string, format Format) (Parsed, error) {
	if err := ctx.Err(); err != nil {
		return Parsed{}, err
	}
	var (
		value Parsed
		err   error
	)
	switch format {
	case FormatText, FormatMarkdown, FormatCSV:
		value, err = parseText(ctx, path, format)
	case FormatPDF:
		value, err = parsePDF(ctx, path)
	case FormatDOCX:
		value, err = parseDOCX(ctx, path)
	case FormatXLSX:
		value, err = parseXLSX(ctx, path)
	default:
		return Parsed{}, fmt.Errorf("unsupported document format %q", format)
	}
	if err != nil {
		return Parsed{}, err
	}
	value.SchemaVersion = SchemaVersion
	value.Format = format
	if value.Metadata == nil {
		value.Metadata = map[string]string{}
	}
	if value.Units == nil {
		value.Units = []Unit{}
	}
	for index := range value.Units {
		value.Units[index].Index = index + 1
	}
	return value, nil
}

func FormatForName(name string) (Format, bool) {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".txt":
		return FormatText, true
	case ".md", ".markdown":
		return FormatMarkdown, true
	case ".csv", ".tsv":
		return FormatCSV, true
	case ".pdf":
		return FormatPDF, true
	case ".docx":
		return FormatDOCX, true
	case ".xlsx":
		return FormatXLSX, true
	default:
		return "", false
	}
}

func MIMEType(format Format) string {
	switch format {
	case FormatText:
		return "text/plain"
	case FormatMarkdown:
		return "text/markdown"
	case FormatCSV:
		return "text/csv"
	case FormatPDF:
		return "application/pdf"
	case FormatDOCX:
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case FormatXLSX:
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	default:
		return "application/octet-stream"
	}
}

type collector struct {
	units     []Unit
	runes     int
	truncated bool
}

func (c *collector) add(kind, locator, title, content string) bool {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\x00", ""))
	if content == "" || c.truncated {
		return !c.truncated
	}
	remaining := MaxExtractedRunes - c.runes
	if remaining <= 0 {
		c.truncated = true
		return false
	}
	runes := []rune(content)
	if len(runes) > remaining {
		content = string(runes[:remaining])
		c.truncated = true
	}
	c.units = append(c.units, Unit{Kind: kind, Locator: locator, Title: strings.TrimSpace(title), Content: content})
	c.runes += len([]rune(content))
	return !c.truncated
}
