package artifact

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/wangh00/SciAide/internal/document"
	"github.com/wangh00/SciAide/internal/exporter"
)

const (
	maxStructuredPreviewBlocks = 250
	maxPreviewTableRows        = 200
	maxPreviewBlockRunes       = 12_000
	maxPreviewCellRunes        = 2_000
)

func structuredPreviewSupported(version Version) bool {
	format, ok := artifactDocumentFormat(version)
	return ok && format != document.FormatText
}

func buildStructuredPreview(ctx context.Context, path string, version Version) (StructuredPreview, bool, error) {
	format, ok := artifactDocumentFormat(version)
	if !ok {
		return StructuredPreview{}, false, fmt.Errorf("unsupported structured Artifact preview")
	}
	if format == document.FormatMarkdown {
		contents, err := readBoundedUTF8(path, version.SizeBytes)
		if err != nil {
			return StructuredPreview{}, false, err
		}
		blocks, err := exporter.ParseMarkdown(contents)
		if err != nil {
			return StructuredPreview{}, false, err
		}
		previewBlocks, truncated := boundedPreviewBlocks(blocks)
		return StructuredPreview{Format: string(format), Blocks: previewBlocks, Metadata: map[string]string{"structureParser": "goldmark-gfm"}}, truncated, nil
	}
	if format == document.FormatCSV {
		blocks, truncated, err := previewDelimitedBlocks(path, strings.EqualFold(filepath.Ext(version.FileName), ".tsv"))
		if err != nil {
			return StructuredPreview{}, false, err
		}
		previewBlocks, bounded := boundedPreviewBlocks(blocks)
		return StructuredPreview{Format: string(format), Blocks: previewBlocks, Metadata: map[string]string{"structureParser": "encoding-csv"}}, truncated || bounded, nil
	}
	parsed, err := document.Parse(ctx, path, format)
	if err != nil {
		return StructuredPreview{}, false, fmt.Errorf("parse structured Artifact preview: %w", err)
	}
	blocks, truncated := boundedPreviewBlocks(parsedDocumentBlocks(parsed))
	return StructuredPreview{Title: parsed.Title, Format: string(format), Blocks: blocks, Metadata: parsed.Metadata}, truncated || parsed.Truncated, nil
}

func boundedPreviewBlocks(values []exporter.Block) ([]PreviewBlock, bool) {
	truncated := len(values) > maxStructuredPreviewBlocks
	if len(values) > maxStructuredPreviewBlocks {
		values = values[:maxStructuredPreviewBlocks]
	}
	blocks := make([]PreviewBlock, 0, len(values))
	for _, value := range values {
		block := PreviewBlock{Kind: string(value.Kind), Level: value.Level}
		block.Text, truncated = boundedRunes(value.Text, maxPreviewBlockRunes, truncated)
		if len(value.Rows) > 0 {
			rows := value.Rows
			if len(rows) > maxPreviewTableRows {
				rows = rows[:maxPreviewTableRows]
				truncated = true
			}
			block.Rows = make([][]string, len(rows))
			for rowIndex, row := range rows {
				columns := row
				if len(columns) > 64 {
					columns = columns[:64]
					truncated = true
				}
				block.Rows[rowIndex] = make([]string, len(columns))
				for columnIndex, cell := range columns {
					block.Rows[rowIndex][columnIndex], truncated = boundedRunes(cell, maxPreviewCellRunes, truncated)
				}
			}
		}
		blocks = append(blocks, block)
	}
	return blocks, truncated
}

func boundedRunes(value string, maximum int, truncated bool) (string, bool) {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value, truncated
	}
	return string(runes[:maximum]) + "…", true
}
