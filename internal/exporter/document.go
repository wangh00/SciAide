// Package exporter creates deterministic, derived research documents.
package exporter

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extensionast "github.com/yuin/goldmark/extension/ast"
	goldmarktext "github.com/yuin/goldmark/text"
)

const GeneratorVersion = "p6.5-v1"

const maxRenderedTableColumns = 8

type CitationStyle string

const (
	CitationGB7714 CitationStyle = "gb_t_7714_2015"
	CitationAPA7   CitationStyle = "apa_7"
)

type Citation struct {
	Ordinal       int
	Reference     string
	SourceName    string
	Title         string
	Locator       string
	Quote         string
	QuoteSHA256   string
	Bibliography  Bibliography
	EvidenceLevel string
}

type BibliographicAuthor struct {
	Name  string
	ORCID string
}

type Bibliography struct {
	Authors        []BibliographicAuthor
	Year           int
	Title          string
	Published      string
	ContainerTitle string
	Volume         string
	Issue          string
	Pages          string
	Publisher      string
	DOI            string
	PMID           string
	PMCID          string
	ArXiv          string
	OpenAlex       string
	URL            string
	WorkType       string
	Language       string
}

type SourceVersion struct {
	ID        string
	SHA256    string
	CreatedAt time.Time
	Citations []Citation
}

type BlockKind string

const (
	BlockHeading   BlockKind = "heading"
	BlockParagraph BlockKind = "paragraph"
	BlockListItem  BlockKind = "list_item"
	BlockQuote     BlockKind = "quote"
	BlockCode      BlockKind = "code"
	BlockTable     BlockKind = "table"
	BlockRule      BlockKind = "rule"
)

type Block struct {
	Kind    BlockKind
	Level   int
	Text    string
	Ordered bool
	Number  int
	Rows    [][]string
}

type Document struct {
	Title         string
	Blocks        []Block
	References    []Reference
	CitationStyle CitationStyle
	SourceVersion string
	SourceSHA256  string
	CreatedAt     time.Time
	Generator     string
}

type Reference struct {
	Number  int
	Marker  string
	Entry   string
	Source  string
	Title   string
	Locator string
	Quote   string
	SHA256  string
}

var citationMarkerPattern = regexp.MustCompile(`\[K-[0-9A-F]{12}\]`)

func BuildDocument(title string, markdown []byte, version SourceVersion, style CitationStyle) (Document, error) {
	if style != CitationGB7714 && style != CitationAPA7 {
		return Document{}, fmt.Errorf("unsupported citation style %q", style)
	}
	blocks, err := ParseMarkdown(markdown)
	if err != nil {
		return Document{}, err
	}
	return BuildStructuredDocument(title, blocks, version, style)
}

func BuildStructuredDocument(title string, blocks []Block, version SourceVersion, style CitationStyle) (Document, error) {
	if style != CitationGB7714 && style != CitationAPA7 {
		return Document{}, fmt.Errorf("unsupported citation style %q", style)
	}
	resolved, references := renderReferences(version.Citations, style)
	normalized := make([]Block, 0, len(blocks))
	for _, block := range blocks {
		block.Text = string(replaceCitationMarkers([]byte(block.Text), resolved))
		if len(block.Rows) > 0 {
			rows := make([][]string, len(block.Rows))
			for rowIndex := range block.Rows {
				rows[rowIndex] = make([]string, len(block.Rows[rowIndex]))
				for columnIndex := range block.Rows[rowIndex] {
					rows[rowIndex][columnIndex] = string(replaceCitationMarkers([]byte(block.Rows[rowIndex][columnIndex]), resolved))
				}
			}
			block.Rows = rows
		}
		normalized = append(normalized, block)
	}
	return Document{
		Title: strings.TrimSpace(title), Blocks: normalized, References: references, CitationStyle: style,
		SourceVersion: version.ID, SourceSHA256: version.SHA256, CreatedAt: version.CreatedAt.UTC(), Generator: GeneratorVersion,
	}, nil
}

func replaceCitationMarkers(value []byte, resolved map[string]string) []byte {
	return citationMarkerPattern.ReplaceAllFunc(value, func(marker []byte) []byte {
		if replacement, ok := resolved[string(marker)]; ok {
			return []byte(replacement)
		}
		return []byte("[未验证引用 " + string(marker) + "]")
	})
}

func ParseMarkdown(source []byte) ([]Block, error) {
	parser := goldmark.New(goldmark.WithExtensions(extension.GFM))
	root := parser.Parser().Parse(goldmarktext.NewReader(source))
	blocks := make([]Block, 0)
	var visit func(ast.Node, int, bool, int)
	visit = func(node ast.Node, listDepth int, ordered bool, number int) {
		switch value := node.(type) {
		case *ast.Heading:
			blocks = append(blocks, Block{Kind: BlockHeading, Level: value.Level, Text: inlineText(value, source)})
			return
		case *ast.Paragraph:
			if _, inItem := value.Parent().(*ast.ListItem); inItem {
				blocks = append(blocks, Block{Kind: BlockListItem, Level: max(1, listDepth), Text: inlineText(value, source), Ordered: ordered, Number: number})
			} else if _, inQuote := value.Parent().(*ast.Blockquote); inQuote {
				blocks = append(blocks, Block{Kind: BlockQuote, Text: inlineText(value, source)})
			} else if _, inCell := value.Parent().(*extensionast.TableCell); !inCell {
				blocks = append(blocks, Block{Kind: BlockParagraph, Text: inlineText(value, source)})
			}
			return
		case *ast.FencedCodeBlock:
			blocks = append(blocks, Block{Kind: BlockCode, Text: linesText(value, source)})
			return
		case *ast.CodeBlock:
			blocks = append(blocks, Block{Kind: BlockCode, Text: linesText(value, source)})
			return
		case *ast.ThematicBreak:
			blocks = append(blocks, Block{Kind: BlockRule})
			return
		case *extensionast.Table:
			rows := make([][]string, 0)
			for row := value.FirstChild(); row != nil; row = row.NextSibling() {
				cells := make([]string, 0)
				for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
					cells = append(cells, inlineText(cell, source))
				}
				if len(cells) > 0 {
					rows = append(rows, cells)
				}
			}
			if len(rows) > 0 {
				blocks = append(blocks, Block{Kind: BlockTable, Rows: rows})
			}
			return
		case *ast.List:
			itemNumber := value.Start
			if itemNumber <= 0 {
				itemNumber = 1
			}
			for child := value.FirstChild(); child != nil; child = child.NextSibling() {
				visit(child, listDepth+1, value.IsOrdered(), itemNumber)
				itemNumber++
			}
			return
		}
		for child := node.FirstChild(); child != nil; child = child.NextSibling() {
			visit(child, listDepth, ordered, number)
		}
	}
	visit(root, 0, false, 0)
	return blocks, nil
}

func inlineText(node ast.Node, source []byte) string {
	var value strings.Builder
	var walk func(ast.Node)
	walk = func(current ast.Node) {
		switch part := current.(type) {
		case *ast.Text:
			value.Write(part.Segment.Value(source))
			if part.SoftLineBreak() || part.HardLineBreak() {
				value.WriteByte('\n')
			}
			return
		case *ast.String:
			value.Write(part.Value)
			return
		case *ast.CodeSpan:
			value.WriteString(string(part.Text(source)))
			return
		case *ast.AutoLink:
			value.Write(part.Label(source))
			return
		case *ast.Link:
			for child := part.FirstChild(); child != nil; child = child.NextSibling() {
				walk(child)
			}
			if len(part.Destination) > 0 {
				value.WriteString(" (")
				value.Write(part.Destination)
				value.WriteByte(')')
			}
			return
		case *ast.Image:
			value.WriteString("[图片: ")
			for child := part.FirstChild(); child != nil; child = child.NextSibling() {
				walk(child)
			}
			value.WriteByte(']')
			return
		}
		for child := current.FirstChild(); child != nil; child = child.NextSibling() {
			walk(child)
		}
	}
	walk(node)
	return strings.TrimSpace(value.String())
}

func linesText(node ast.Node, source []byte) string {
	var value strings.Builder
	for index := range node.Lines().Len() {
		segment := node.Lines().At(index)
		value.Write(segment.Value(source))
	}
	return strings.TrimRight(value.String(), "\r\n")
}

// tableColumnSegments keeps tables legible on portrait pages. The first
// column is repeated in later segments so row identities remain visible.
func tableColumnSegments(rows [][]string, maximum int) [][][]string {
	if len(rows) == 0 || maximum < 2 {
		return nil
	}
	columns := 0
	for _, row := range rows {
		columns = max(columns, len(row))
	}
	if columns == 0 {
		return nil
	}
	segments := make([][][]string, 0, 1+(columns-1)/(maximum-1))
	appendSegment := func(start, end int, repeatFirst bool) {
		segment := make([][]string, len(rows))
		segmentColumns := end - start
		if repeatFirst {
			segmentColumns++
		}
		for rowIndex, row := range rows {
			segment[rowIndex] = make([]string, segmentColumns)
			offset := 0
			if repeatFirst {
				if len(row) > 0 {
					segment[rowIndex][0] = row[0]
				}
				offset = 1
			}
			for column := start; column < end; column++ {
				if column < len(row) {
					segment[rowIndex][offset+column-start] = row[column]
				}
			}
		}
		segments = append(segments, segment)
	}
	firstEnd := min(columns, maximum)
	appendSegment(0, firstEnd, false)
	for start := firstEnd; start < columns; start += maximum - 1 {
		appendSegment(start, min(columns, start+maximum-1), true)
	}
	return segments
}

func renderReferences(citations []Citation, style CitationStyle) (map[string]string, []Reference) {
	values := append([]Citation(nil), citations...)
	sort.SliceStable(values, func(left, right int) bool { return values[left].Ordinal < values[right].Ordinal })
	markers := make(map[string]string, len(values))
	references := make([]Reference, 0, len(values))
	for index, value := range values {
		number := index + 1
		markers[value.Reference] = inTextCitation(value, number, style)
		references = append(references, Reference{
			Number: number, Marker: value.Reference, Entry: formatReference(value, number, style), Source: strings.TrimSpace(value.SourceName),
			Title: strings.TrimSpace(value.Title), Locator: strings.TrimSpace(value.Locator), Quote: strings.TrimSpace(value.Quote), SHA256: value.QuoteSHA256,
		})
	}
	return markers, references
}

func inTextCitation(value Citation, number int, style CitationStyle) string {
	if style == CitationGB7714 {
		return "[" + strconv.Itoa(number) + "]"
	}
	label := firstAuthorLabel(value.Bibliography.Authors)
	year := "n.d."
	if value.Bibliography.Year > 0 {
		year = strconv.Itoa(value.Bibliography.Year)
	}
	if label != "" {
		if len(value.Bibliography.Authors) > 2 {
			label += " et al."
		} else if len(value.Bibliography.Authors) == 2 {
			label += " & " + strings.TrimSpace(value.Bibliography.Authors[1].Name)
		}
		return "(" + label + ", " + year + ")"
	}
	label = strings.TrimSpace(value.Bibliography.Title)
	if label == "" {
		label = strings.TrimSpace(value.Title)
	}
	if label == "" {
		label = strings.TrimSpace(value.SourceName)
	}
	if label == "" {
		label = "未命名来源"
	}
	return "(" + label + ", " + year + ")"
}

func formatReference(value Citation, number int, style CitationStyle) string {
	if hasBibliography(value.Bibliography) {
		if style == CitationAPA7 {
			return formatAPAReference(value.Bibliography) + evidenceDisclosure(value.EvidenceLevel)
		}
		return "[" + strconv.Itoa(number) + "] " + formatGBReference(value.Bibliography) + evidenceDisclosure(value.EvidenceLevel)
	}
	source := strings.TrimSpace(value.SourceName)
	if source == "" {
		source = "未命名来源"
	}
	title := strings.TrimSpace(value.Title)
	locator := strings.TrimSpace(value.Locator)
	if style == CitationAPA7 {
		workTitle := title
		if workTitle == "" {
			workTitle = source
		}
		parts := []string{workTitle + ". (n.d.)."}
		if title != "" && source != title {
			parts = append(parts, source+".")
		}
		if locator != "" {
			parts = append(parts, locator+".")
		}
		return strings.Join(parts, " ") + " [元数据来自可信引用快照；作者与年份未提供]"
	}
	parts := []string{"[" + strconv.Itoa(number) + "]", "未知作者."}
	if title != "" {
		parts = append(parts, title+"[EB/OL].")
	} else {
		parts = append(parts, source+"[EB/OL].")
	}
	if title != "" {
		parts = append(parts, source+".")
	}
	if locator != "" {
		parts = append(parts, locator+".")
	}
	return strings.Join(parts, " ") + " [作者、年份等书目信息未包含在引用快照中]"
}

func hasBibliography(value Bibliography) bool {
	return len(value.Authors) > 0 || value.Year > 0 || strings.TrimSpace(value.Title) != "" || strings.TrimSpace(value.ContainerTitle) != "" || strings.TrimSpace(value.DOI) != "" || strings.TrimSpace(value.URL) != ""
}

func formatGBReference(value Bibliography) string {
	authors := bibliographyAuthorNames(value.Authors)
	if authors == "" {
		authors = "未知作者"
	}
	title := strings.TrimSpace(value.Title)
	if title == "" {
		title = "题名缺失"
	}
	typeCode := gbTypeCode(value.WorkType, value.ContainerTitle)
	parts := []string{authors + ".", title + typeCode + "."}
	container := strings.TrimSpace(value.ContainerTitle)
	if container != "" {
		parts = append(parts, container+gbPublicationTail(value)+".")
	} else {
		publication := strings.TrimSpace(value.Publisher)
		if publication != "" {
			if value.Year > 0 {
				publication += ", " + strconv.Itoa(value.Year)
			}
			parts = append(parts, publication+".")
		} else if value.Year > 0 {
			parts = append(parts, strconv.Itoa(value.Year)+".")
		}
	}
	if doi := strings.TrimSpace(value.DOI); doi != "" {
		parts = append(parts, "DOI: "+doi+".")
	} else if location := preferredBibliographyURL(value); location != "" {
		parts = append(parts, location+".")
	}
	return strings.Join(parts, " ")
}

func gbPublicationTail(value Bibliography) string {
	var tail strings.Builder
	if value.Year > 0 {
		tail.WriteString(", ")
		tail.WriteString(strconv.Itoa(value.Year))
	}
	if volume := strings.TrimSpace(value.Volume); volume != "" {
		tail.WriteString(", ")
		tail.WriteString(volume)
	}
	if issue := strings.TrimSpace(value.Issue); issue != "" {
		tail.WriteString("(")
		tail.WriteString(issue)
		tail.WriteString(")")
	}
	if pages := strings.TrimSpace(value.Pages); pages != "" {
		tail.WriteString(": ")
		tail.WriteString(pages)
	}
	return tail.String()
}

func gbTypeCode(workType, container string) string {
	kind := strings.ToLower(strings.TrimSpace(workType))
	switch {
	case strings.Contains(kind, "proceeding") || strings.Contains(kind, "conference"):
		return "[C]"
	case strings.Contains(kind, "book"):
		return "[M]"
	case strings.Contains(kind, "thesis") || strings.Contains(kind, "dissertation"):
		return "[D]"
	case strings.TrimSpace(container) != "":
		return "[J]"
	default:
		return "[EB/OL]"
	}
}

func formatAPAReference(value Bibliography) string {
	authors := apaAuthorNames(value.Authors)
	if authors == "" {
		authors = "Unknown author"
	}
	year := "n.d."
	if value.Year > 0 {
		year = strconv.Itoa(value.Year)
	}
	title := strings.TrimSpace(value.Title)
	if title == "" {
		title = "[Title unavailable]"
	}
	parts := []string{authors + " (" + year + ").", title + "."}
	container := strings.TrimSpace(value.ContainerTitle)
	if container != "" {
		publication := container
		if volume := strings.TrimSpace(value.Volume); volume != "" {
			publication += ", " + volume
		}
		if issue := strings.TrimSpace(value.Issue); issue != "" {
			publication += "(" + issue + ")"
		}
		if pages := strings.TrimSpace(value.Pages); pages != "" {
			publication += ", " + pages
		}
		parts = append(parts, publication+".")
	} else if publisher := strings.TrimSpace(value.Publisher); publisher != "" {
		parts = append(parts, publisher+".")
	}
	if location := preferredBibliographyURL(value); location != "" {
		parts = append(parts, location)
	}
	return strings.Join(parts, " ")
}

func bibliographyAuthorNames(values []BibliographicAuthor) string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		if name := strings.TrimSpace(value.Name); name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, ", ")
}

func apaAuthorNames(values []BibliographicAuthor) string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		if name := strings.TrimSpace(value.Name); name != "" {
			names = append(names, name)
		}
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + ", & " + names[len(names)-1]
}

func firstAuthorLabel(values []BibliographicAuthor) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0].Name)
}

func preferredBibliographyURL(value Bibliography) string {
	if doi := strings.TrimSpace(value.DOI); doi != "" {
		return "https://doi.org/" + doi
	}
	if location := strings.TrimSpace(value.URL); location != "" {
		return location
	}
	return ""
}

func evidenceDisclosure(level string) string {
	switch strings.TrimSpace(level) {
	case "metadata_abstract":
		return " [证据等级：元数据/摘要，非全文]"
	case "full_text":
		return " [证据等级：本地全文]"
	default:
		return ""
	}
}
