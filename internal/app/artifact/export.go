package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/document"
	"github.com/wangh00/SciAide/internal/exporter"
	"github.com/wangh00/SciAide/internal/platform/filepublish"
)

const (
	maxDirectTextExportBytes = int64(64 << 20)
	maxExportTableColumns    = 64
	maxExportTableRows       = 50_000
	maxExportCellRunes       = 32_000
)

func (s *Service) CreateExport(ctx context.Context, cmd ExportCommand) (ExportResult, error) {
	cmd.ProjectID, cmd.VersionID = strings.TrimSpace(cmd.ProjectID), strings.TrimSpace(cmd.VersionID)
	if cmd.ProjectID == "" || cmd.VersionID == "" {
		return ExportResult{}, fmt.Errorf("project and Artifact version are required")
	}
	if cmd.Format != ExportDOCX && cmd.Format != ExportPDF {
		return ExportResult{}, fmt.Errorf("unsupported Artifact export format %q", cmd.Format)
	}
	if cmd.CitationStyle != CitationGB7714 && cmd.CitationStyle != CitationAPA7 {
		return ExportResult{}, fmt.Errorf("unsupported citation style %q", cmd.CitationStyle)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	version, _, selected, source, err := s.openVersion(ctx, cmd.ProjectID, cmd.VersionID)
	if err != nil {
		return ExportResult{}, err
	}
	snapshotPath, cleanup, err := s.verifiedSnapshot(ctx, selected, version, source, "export-source-")
	if err != nil {
		return ExportResult{}, err
	}
	defer cleanup()

	blocks, err := exportBlocks(ctx, snapshotPath, version)
	if err != nil {
		return ExportResult{}, err
	}
	if err := validateExportBlocks(blocks, false); err != nil {
		return ExportResult{}, err
	}
	sourceVersion := exporter.SourceVersion{ID: version.ID, SHA256: version.SHA256, CreatedAt: version.CreatedAt, Citations: exportCitations(version.Citations)}
	title := exportTitle(version)
	doc, err := exporter.BuildStructuredDocument(title, blocks, sourceVersion, exporter.CitationStyle(cmd.CitationStyle))
	if err != nil {
		return ExportResult{}, err
	}
	var contents []byte
	switch cmd.Format {
	case ExportDOCX:
		contents, err = exporter.RenderDOCX(doc)
	case ExportPDF:
		contents, err = exporter.RenderPDF(doc)
	}
	if err != nil {
		return ExportResult{}, fmt.Errorf("render Artifact export: %w", err)
	}
	if len(contents) == 0 || int64(len(contents)) > maxArtifactBytes {
		return ExportResult{}, fmt.Errorf("generated Artifact export has an invalid size")
	}
	if err := s.validateGeneratedExport(ctx, selected, cmd.Format, contents); err != nil {
		return ExportResult{}, err
	}

	digestBytes := sha256.Sum256(contents)
	digest := hex.EncodeToString(digestBytes[:])
	blobID, err := s.newID()
	if err != nil {
		return ExportResult{}, err
	}
	exportID, err := s.newID()
	if err != nil {
		return ExportResult{}, err
	}
	objectRelative, err := s.publishObject(selected, contents, digest)
	if err != nil {
		return ExportResult{}, err
	}
	now := s.now()
	mimeType := "application/pdf"
	if cmd.Format == ExportDOCX {
		mimeType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	}
	fileName := exportFileName(title, version.VersionNumber, cmd.Format, cmd.CitationStyle)
	value := Export{
		ID: exportID, ProjectID: selected.ID, ArtifactVersionID: version.ID, BlobID: blobID,
		Format: cmd.Format, CitationStyle: cmd.CitationStyle, GeneratorVersion: exporter.GeneratorVersion,
		FileName: fileName, MIMEType: mimeType, SizeBytes: int64(len(contents)), SHA256: digest,
		SourceSHA256: version.SHA256, CreatedAt: now,
	}
	return s.repository.CreateExport(ctx, CreateExportRecord{
		Export: value,
		Blob: BlobRecord{
			ID: blobID, ProjectID: selected.ID, SHA256: digest, SizeBytes: int64(len(contents)), MIMEType: mimeType,
			StorageRelativePath: filepath.ToSlash(objectRelative), CreatedAt: now,
		},
	})
}

func (s *Service) DownloadExport(ctx context.Context, projectID, exportID, destination string) error {
	projectID, exportID, destination = strings.TrimSpace(projectID), strings.TrimSpace(exportID), strings.TrimSpace(destination)
	if projectID == "" || exportID == "" || destination == "" {
		return fmt.Errorf("project, Artifact export, and destination are required")
	}
	value, blob, err := s.repository.GetExport(ctx, projectID, exportID)
	if err != nil {
		return err
	}
	selected, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return err
	}
	if err := project.VerifyPrivateDataLayout(selected); err != nil {
		return err
	}
	root, err := os.OpenRoot(project.PrivateDataPath(selected))
	if err != nil {
		return err
	}
	input, err := root.Open(filepath.FromSlash(blob.StorageRelativePath))
	_ = root.Close()
	if err != nil {
		return fmt.Errorf("open Artifact export: %w", err)
	}
	defer input.Close()
	return copyVerifiedDownload(input, destination, value.SizeBytes, value.SHA256, s.newID)
}

func (s *Service) verifiedSnapshot(ctx context.Context, selected project.Project, version Version, source *os.File, prefix string) (string, func(), error) {
	defer source.Close()
	if err := ctx.Err(); err != nil {
		return "", func() {}, err
	}
	rootPath := project.PrivateDataPath(selected)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return "", func() {}, err
	}
	defer root.Close()
	if err := root.MkdirAll("tmp", 0o700); err != nil {
		return "", func() {}, err
	}
	temporaryID, err := s.newID()
	if err != nil {
		return "", func() {}, err
	}
	relative := filepath.Join("tmp", prefix+temporaryID+filepath.Ext(version.FileName))
	output, err := root.OpenFile(relative, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", func() {}, err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(source, maxArtifactBytes+1))
	syncErr, closeErr := output.Sync(), output.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || written != version.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != version.SHA256 {
		_ = root.Remove(relative)
		if copyErr != nil {
			return "", func() {}, fmt.Errorf("snapshot Artifact source: %w", copyErr)
		}
		return "", func() {}, fmt.Errorf("Artifact integrity check failed before processing")
	}
	absolute := filepath.Join(rootPath, relative)
	cleanup := func() { _ = os.Remove(absolute) }
	return absolute, cleanup, nil
}

func (s *Service) validateGeneratedExport(ctx context.Context, selected project.Project, format ExportFormat, contents []byte) error {
	rootPath := project.PrivateDataPath(selected)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	temporaryID, err := s.newID()
	if err != nil {
		return err
	}
	extension := "." + string(format)
	relative := filepath.Join("tmp", "export-validate-"+temporaryID+extension)
	file, err := root.OpenFile(relative, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(contents)
	syncErr, closeErr := file.Sync(), file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = root.Remove(relative)
		return fmt.Errorf("write Artifact export validation copy")
	}
	absolute := filepath.Join(rootPath, relative)
	defer root.Remove(relative)
	documentFormat := document.FormatPDF
	if format == ExportDOCX {
		documentFormat = document.FormatDOCX
	}
	parsed, err := document.Parse(ctx, absolute, documentFormat)
	if err != nil {
		return fmt.Errorf("generated Artifact export could not be reopened: %w", err)
	}
	if parsed.ExtractedRunes == 0 || len(parsed.Units) == 0 {
		return fmt.Errorf("generated Artifact export contains no readable content")
	}
	return nil
}

func (s *Service) publishObject(selected project.Project, contents []byte, digest string) (string, error) {
	if len(digest) != 64 || int64(len(contents)) > maxArtifactBytes {
		return "", fmt.Errorf("invalid Artifact object")
	}
	root, err := os.OpenRoot(project.PrivateDataPath(selected))
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := root.MkdirAll("tmp", 0o700); err != nil {
		return "", err
	}
	temporaryID, err := s.newID()
	if err != nil {
		return "", err
	}
	temporary := filepath.Join("tmp", "artifact-export-"+temporaryID)
	output, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	written, writeErr := output.Write(contents)
	syncErr, closeErr := output.Sync(), output.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || written != len(contents) {
		_ = root.Remove(temporary)
		return "", fmt.Errorf("write Artifact export object")
	}
	objectRelative := filepath.Join("artifacts", "objects", digest[:2], digest)
	if err := root.MkdirAll(filepath.Dir(objectRelative), 0o700); err != nil {
		_ = root.Remove(temporary)
		return "", err
	}
	if info, err := root.Stat(objectRelative); errors.Is(err, os.ErrNotExist) {
		if err := root.Rename(temporary, objectRelative); err != nil {
			_ = root.Remove(temporary)
			return "", fmt.Errorf("publish Artifact export object: %w", err)
		}
	} else if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(contents)) {
		_ = root.Remove(temporary)
		return "", fmt.Errorf("existing Artifact export object conflicts with its content address")
	} else {
		if err := verifyObject(root, objectRelative, int64(len(contents)), digest); err != nil {
			_ = root.Remove(temporary)
			return "", err
		}
		_ = root.Remove(temporary)
	}
	return objectRelative, nil
}

func exportBlocks(ctx context.Context, path string, version Version) ([]exporter.Block, error) {
	format, ok := artifactDocumentFormat(version)
	if !ok {
		return nil, fmt.Errorf("this Artifact format cannot be exported as a research document")
	}
	if format == document.FormatMarkdown {
		contents, err := readBoundedUTF8(path, version.SizeBytes)
		if err != nil {
			return nil, err
		}
		blocks, err := exporter.ParseMarkdown(contents)
		if err != nil {
			return nil, err
		}
		if err := validateExportBlocks(blocks, true); err != nil {
			return nil, err
		}
		return blocks, nil
	}
	if format == document.FormatCSV {
		return delimitedBlocks(path, strings.EqualFold(filepath.Ext(version.FileName), ".tsv"))
	}
	if format == document.FormatText {
		contents, err := readBoundedUTF8(path, version.SizeBytes)
		if err != nil {
			return nil, err
		}
		if isCodeExtension(filepath.Ext(version.FileName)) || strings.Contains(strings.ToLower(version.MIMEType), "json") {
			return []exporter.Block{{Kind: exporter.BlockCode, Text: string(contents)}}, nil
		}
		return plainTextBlocks(string(contents)), nil
	}
	parsed, err := document.Parse(ctx, path, format)
	if err != nil {
		return nil, fmt.Errorf("parse Artifact for export: %w", err)
	}
	if err := validateParsedDocumentExport(parsed); err != nil {
		return nil, err
	}
	return parsedDocumentBlocks(parsed), nil
}

func validateExportBlocks(blocks []exporter.Block, enforceTableRowLimit bool) error {
	readable := false
	tableRows := 0
	totalRunes := 0
	for _, block := range blocks {
		blockRunes := len([]rune(block.Text))
		totalRunes += blockRunes
		if blockRunes > 0 && strings.TrimSpace(block.Text) != "" {
			readable = true
		}
		if block.Kind != exporter.BlockTable {
			continue
		}
		tableRows += len(block.Rows)
		if enforceTableRowLimit && tableRows > maxExportTableRows {
			return fmt.Errorf("tabular Artifact has more than %d rows", maxExportTableRows)
		}
		for _, row := range block.Rows {
			if len(row) > maxExportTableColumns {
				return fmt.Errorf("tabular Artifact has more than %d columns", maxExportTableColumns)
			}
			for _, cell := range row {
				cellRunes := len([]rune(cell))
				if cellRunes > maxExportCellRunes {
					return fmt.Errorf("tabular Artifact cell exceeds %d characters", maxExportCellRunes)
				}
				totalRunes += cellRunes
				if strings.TrimSpace(cell) != "" {
					readable = true
				}
			}
		}
	}
	if totalRunes > document.MaxExtractedRunes {
		return fmt.Errorf("Artifact exceeds the export text limit")
	}
	if !readable {
		return fmt.Errorf("Artifact contains no readable content to export")
	}
	return nil
}

func validateParsedDocumentExport(parsed document.Parsed) error {
	if parsed.Truncated {
		return fmt.Errorf("Artifact exceeds the complete document export limit")
	}
	tableRows := 0
	for _, unit := range parsed.Units {
		var row []string
		switch unit.Kind {
		case "table_row":
			row = splitMarkdownTableRow(unit.Content)
		case "sheet_row":
			row = splitSheetRow(unit.Content)
		default:
			continue
		}
		tableRows++
		if tableRows > maxExportTableRows {
			return fmt.Errorf("tabular Artifact has more than %d rows", maxExportTableRows)
		}
		if len(row) > maxExportTableColumns {
			return fmt.Errorf("tabular Artifact has more than %d columns", maxExportTableColumns)
		}
		for _, cell := range row {
			if len([]rune(cell)) > maxExportCellRunes {
				return fmt.Errorf("tabular Artifact cell exceeds %d characters", maxExportCellRunes)
			}
		}
	}
	return nil
}

func artifactDocumentFormat(version Version) (document.Format, bool) {
	mediaType, _, _ := mime.ParseMediaType(version.MIMEType)
	switch {
	case mediaType == "application/pdf":
		return document.FormatPDF, true
	case strings.Contains(mediaType, "wordprocessingml"):
		return document.FormatDOCX, true
	case strings.Contains(mediaType, "spreadsheetml"):
		return document.FormatXLSX, true
	case mediaType == "text/markdown":
		return document.FormatMarkdown, true
	case mediaType == "text/csv" || mediaType == "text/tab-separated-values":
		return document.FormatCSV, true
	case isTextMIME(version.MIMEType):
		return document.FormatText, true
	default:
		return "", false
	}
}

func readBoundedUTF8(path string, size int64) ([]byte, error) {
	if size < 0 || size > maxDirectTextExportBytes {
		return nil, fmt.Errorf("text Artifact is too large for document export")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) != size || !utf8.Valid(contents) || strings.IndexByte(string(contents), 0) >= 0 {
		return nil, fmt.Errorf("text Artifact is not valid UTF-8")
	}
	return contents, nil
}

func plainTextBlocks(value string) []exporter.Block {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	parts := strings.Split(value, "\n\n")
	blocks := make([]exporter.Block, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			blocks = append(blocks, exporter.Block{Kind: exporter.BlockParagraph, Text: part})
		}
	}
	return blocks
}

func delimitedBlocks(path string, tabSeparated bool) ([]exporter.Block, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxDirectTextExportBytes {
		return nil, fmt.Errorf("tabular Artifact is too large for document export")
	}
	blocks, truncated, err := readDelimitedBlocks(path, tabSeparated, maxExportTableRows)
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, fmt.Errorf("tabular Artifact has more than %d rows", maxExportTableRows)
	}
	return blocks, nil
}

func previewDelimitedBlocks(path string, tabSeparated bool) ([]exporter.Block, bool, error) {
	return readDelimitedBlocks(path, tabSeparated, maxPreviewTableRows)
}

func readDelimitedBlocks(path string, tabSeparated bool, maximumRows int) ([]exporter.Block, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	reader := csv.NewReader(io.LimitReader(file, maxDirectTextExportBytes+1))
	reader.FieldsPerRecord = -1
	reader.ReuseRecord = false
	if tabSeparated {
		reader.Comma = '\t'
	}
	blocks := make([]exporter.Block, 0)
	rows := make([][]string, 0, 51)
	var header []string
	characters := 0
	flush := func() {
		if len(rows) > 0 {
			blocks = append(blocks, exporter.Block{Kind: exporter.BlockTable, Rows: rows})
			rows = nil
		}
	}
	for rowNumber := 0; ; rowNumber++ {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, false, fmt.Errorf("parse tabular Artifact row %d: %w", rowNumber+1, err)
		}
		if len(row) > maxExportTableColumns {
			return nil, false, fmt.Errorf("tabular Artifact has more than %d columns", maxExportTableColumns)
		}
		if rowNumber >= maximumRows {
			flush()
			return blocks, true, nil
		}
		for index := range row {
			row[index] = strings.TrimSpace(strings.ReplaceAll(row[index], "\x00", ""))
			cellRunes := len([]rune(row[index]))
			if cellRunes > maxExportCellRunes {
				return nil, false, fmt.Errorf("tabular Artifact cell exceeds %d characters", maxExportCellRunes)
			}
			characters += cellRunes
		}
		if characters > document.MaxExtractedRunes {
			return nil, false, fmt.Errorf("tabular Artifact exceeds the export text limit")
		}
		if rowNumber == 0 {
			header = append([]string(nil), row...)
		}
		rows = append(rows, row)
		if len(rows) == 51 {
			flush()
			rows = [][]string{append([]string(nil), header...)}
		}
	}
	flush()
	return blocks, false, nil
}

func parsedDocumentBlocks(parsed document.Parsed) []exporter.Block {
	blocks := make([]exporter.Block, 0, len(parsed.Units))
	var tableRows [][]string
	flushTable := func() {
		if len(tableRows) > 0 {
			blocks = append(blocks, exporter.Block{Kind: exporter.BlockTable, Rows: tableRows})
			tableRows = nil
		}
	}
	lastTable := ""
	for _, unit := range parsed.Units {
		switch unit.Kind {
		case "table_row":
			tableID := unit.Locator
			if prefix, _, ok := strings.Cut(unit.Locator, "/row:"); ok {
				tableID = prefix
			}
			if lastTable != "" && tableID != lastTable {
				flushTable()
			}
			lastTable = tableID
			row := splitMarkdownTableRow(unit.Content)
			if len(row) > maxExportTableColumns {
				row = row[:maxExportTableColumns]
			}
			tableRows = append(tableRows, row)
			continue
		case "sheet_row":
			if lastTable != "" && unit.Title != lastTable {
				flushTable()
			}
			lastTable = unit.Title
			row := splitSheetRow(unit.Content)
			if len(row) > maxExportTableColumns {
				row = row[:maxExportTableColumns]
			}
			tableRows = append(tableRows, row)
			continue
		}
		flushTable()
		lastTable = ""
		switch unit.Kind {
		case "title":
			blocks = append(blocks, exporter.Block{Kind: exporter.BlockHeading, Level: 1, Text: unit.Content})
		case "heading":
			if unit.Title != "" && unit.Title != unit.Content {
				blocks = append(blocks, exporter.Block{Kind: exporter.BlockHeading, Level: 2, Text: unit.Title})
			}
			if unit.Content != "" {
				blocks = append(blocks, exporter.Block{Kind: exporter.BlockHeading, Level: 2, Text: unit.Content})
			}
		case "section":
			if unit.Title != "" && unit.Title != unit.Content {
				blocks = append(blocks, exporter.Block{Kind: exporter.BlockHeading, Level: 2, Text: unit.Title})
			}
			content := strings.TrimSpace(unit.Content)
			if unit.Title != "" {
				content = strings.TrimSpace(strings.TrimPrefix(content, unit.Title))
			}
			if content != "" {
				blocks = append(blocks, exporter.Block{Kind: exporter.BlockParagraph, Text: content})
			}
		case "list_item":
			blocks = append(blocks, exporter.Block{Kind: exporter.BlockListItem, Level: 1, Text: unit.Content})
		case "caption":
			blocks = append(blocks, exporter.Block{Kind: exporter.BlockQuote, Text: unit.Content})
		default:
			blocks = append(blocks, exporter.Block{Kind: exporter.BlockParagraph, Text: unit.Content})
		}
	}
	flushTable()
	return blocks
}

func splitMarkdownTableRow(value string) []string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(strings.TrimSuffix(value, "|"), "|")
	result := make([]string, 0)
	var cell strings.Builder
	escaped := false
	for _, character := range value {
		switch {
		case escaped:
			cell.WriteRune(character)
			escaped = false
		case character == '\\':
			escaped = true
		case character == '|':
			result = append(result, strings.TrimSpace(cell.String()))
			cell.Reset()
		default:
			cell.WriteRune(character)
		}
	}
	if escaped {
		cell.WriteRune('\\')
	}
	result = append(result, strings.TrimSpace(cell.String()))
	return result
}

func splitSheetRow(value string) []string {
	parts := strings.Split(value, "\t")
	for index, part := range parts {
		if _, cell, ok := strings.Cut(part, "="); ok {
			parts[index] = cell
		}
	}
	return parts
}

func exportCitations(values []Citation) []exporter.Citation {
	result := make([]exporter.Citation, len(values))
	for index, value := range values {
		result[index] = exporter.Citation{
			Ordinal: value.Ordinal, Reference: value.Reference, SourceName: value.SourceName, Title: value.Title,
			Locator: value.Locator, Quote: value.Quote, QuoteSHA256: value.QuoteSHA256, EvidenceLevel: value.EvidenceLevel,
		}
		if len(value.BibliographySnapshot) > 0 {
			var snapshot struct {
				Data struct {
					Authors []struct {
						Name  string `json:"name"`
						ORCID string `json:"orcid"`
					} `json:"authors"`
					Year           int    `json:"year"`
					Title          string `json:"title"`
					Published      string `json:"published"`
					ContainerTitle string `json:"containerTitle"`
					Volume         string `json:"volume"`
					Issue          string `json:"issue"`
					Pages          string `json:"pages"`
					Publisher      string `json:"publisher"`
					DOI            string `json:"doi"`
					PMID           string `json:"pmid"`
					PMCID          string `json:"pmcid"`
					ArXiv          string `json:"arxiv"`
					OpenAlex       string `json:"openAlex"`
					URL            string `json:"url"`
					WorkType       string `json:"workType"`
					Language       string `json:"language"`
				} `json:"data"`
			}
			if json.Unmarshal(value.BibliographySnapshot, &snapshot) == nil {
				bibliography := exporter.Bibliography{Year: snapshot.Data.Year, Title: snapshot.Data.Title, Published: snapshot.Data.Published, ContainerTitle: snapshot.Data.ContainerTitle, Volume: snapshot.Data.Volume, Issue: snapshot.Data.Issue, Pages: snapshot.Data.Pages, Publisher: snapshot.Data.Publisher, DOI: snapshot.Data.DOI, PMID: snapshot.Data.PMID, PMCID: snapshot.Data.PMCID, ArXiv: snapshot.Data.ArXiv, OpenAlex: snapshot.Data.OpenAlex, URL: snapshot.Data.URL, WorkType: snapshot.Data.WorkType, Language: snapshot.Data.Language}
				for _, author := range snapshot.Data.Authors {
					bibliography.Authors = append(bibliography.Authors, exporter.BibliographicAuthor{Name: author.Name, ORCID: author.ORCID})
				}
				result[index].Bibliography = bibliography
			}
		}
	}
	return result
}

func exportFileName(name string, version int, format ExportFormat, style CitationStyle) string {
	styleName := "gbt7714"
	if style == CitationAPA7 {
		styleName = "apa7"
	}
	baseRunes := []rune(safeName(name))
	if len(baseRunes) > 200 {
		baseRunes = baseRunes[:200]
	}
	base := string(baseRunes)
	return fmt.Sprintf("%s-v%d-%s.%s", base, version, styleName, format)
}

func exportTitle(version Version) string {
	if name := strings.TrimSpace(version.Provenance.Extra["artifactNameSnapshot"]); name != "" {
		return safeName(name)
	}
	name := safeName(version.FileName)
	name = strings.TrimSuffix(name, filepath.Ext(name))
	if name == "" {
		return "artifact"
	}
	return name
}

func copyVerifiedDownload(input *os.File, destination string, expectedSize int64, expectedSHA256 string, newID func() (string, error)) error {
	abs, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(abs); err == nil {
		return fmt.Errorf("destination already exists: %s", info.Name())
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return err
	}
	temporaryID, err := newID()
	if err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(abs), ".sciaide-download-"+temporaryID)
	output, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(input, maxArtifactBytes+1))
	syncErr, closeErr := output.Sync(), output.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("copy Artifact download")
	}
	if written != expectedSize || hex.EncodeToString(hash.Sum(nil)) != expectedSHA256 {
		_ = os.Remove(temporary)
		return fmt.Errorf("Artifact integrity check failed before download")
	}
	if err := filepublish.NoReplace(temporary, abs); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("publish Artifact download: %w", err)
	}
	return nil
}
