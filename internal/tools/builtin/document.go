package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/document"
)

const (
	ListAttachmentsName = "builtin.attachment.list"
	InspectDocumentName = "builtin.document.inspect"
	ReadDocumentName    = "builtin.document.read"
	SearchDocumentName  = "builtin.document.search"
	defaultDocumentRead = 64_000
	maxDocumentRead     = 180_000
)

type DocumentLoader interface {
	List(ctx context.Context, projectID string) ([]attachment.Attachment, error)
	Parsed(ctx context.Context, projectID, attachmentID string) (attachment.Attachment, document.Parsed, error)
}

type ListAttachments struct{ documents DocumentLoader }
type InspectDocument struct{ documents DocumentLoader }
type ReadDocument struct{ documents DocumentLoader }
type SearchDocument struct{ documents DocumentLoader }

func NewListAttachments(documents DocumentLoader) *ListAttachments {
	return &ListAttachments{documents: documents}
}
func NewInspectDocument(documents DocumentLoader) *InspectDocument {
	return &InspectDocument{documents: documents}
}
func NewReadDocument(documents DocumentLoader) *ReadDocument {
	return &ReadDocument{documents: documents}
}
func NewSearchDocument(documents DocumentLoader) *SearchDocument {
	return &SearchDocument{documents: documents}
}

func documentReadDefinition(name, description string, input, output json.RawMessage) tool.Definition {
	return tool.Definition{
		QualifiedName: name, Description: description, InputSchema: input, OutputSchema: output,
		Risk: tool.RiskLow, Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "."}},
		Idempotent: true, Version: "1",
	}
}

func (*ListAttachments) Definition(context.Context) (tool.Definition, error) {
	return documentReadDefinition(ListAttachmentsName,
		"List documents attached to the current research project, including stable attachment IDs and local parse status.",
		json.RawMessage(`{"type":"object","additionalProperties":false}`),
		json.RawMessage(`{"type":"object","required":["attachments"],"properties":{"attachments":{"type":"array","items":{"type":"object"}}}}`)), nil
}

func (t *ListAttachments) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if t == nil || t.documents == nil {
		return tool.Result{}, fmt.Errorf("document loader is not configured")
	}
	values, err := t.documents.List(ctx, invocation.ProjectID)
	if err != nil {
		return tool.Result{}, err
	}
	type attachmentInfo struct {
		ID           string            `json:"id"`
		OriginalName string            `json:"originalName"`
		MIMEType     string            `json:"mimeType"`
		Format       document.Format   `json:"format"`
		SizeBytes    int64             `json:"sizeBytes"`
		Status       attachment.Status `json:"status"`
		UnitCount    int               `json:"unitCount"`
		Truncated    bool              `json:"truncated"`
	}
	items := make([]attachmentInfo, 0, len(values))
	for _, value := range values {
		items = append(items, attachmentInfo{ID: value.ID, OriginalName: value.OriginalName, MIMEType: value.MIMEType, Format: value.Format, SizeBytes: value.SizeBytes, Status: value.Status, UnitCount: value.UnitCount, Truncated: value.Truncated})
	}
	structured, err := json.Marshal(struct {
		Attachments []attachmentInfo `json:"attachments"`
	}{Attachments: items})
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Status: tool.ResultSuccess, Text: fmt.Sprintf("%d project attachments are available.", len(values)), Structured: structured}, nil
}

func (*InspectDocument) Definition(context.Context) (tool.Definition, error) {
	return documentReadDefinition(InspectDocumentName,
		"Inspect one locally parsed project document and list its page, paragraph, sheet, row, or line locators before reading it.",
		json.RawMessage(`{"type":"object","additionalProperties":false,"required":["attachmentId"],"properties":{"attachmentId":{"type":"string","minLength":1,"maxLength":128}}}`),
		json.RawMessage(`{"type":"object","required":["attachment","units","totalUnits","truncated"],"properties":{"attachment":{"type":"object"},"units":{"type":"array","items":{"type":"object"}},"totalUnits":{"type":"integer"},"truncated":{"type":"boolean"}}}`)), nil
}

func (t *InspectDocument) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	var args struct {
		AttachmentID string `json:"attachmentId"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	value, parsed, err := t.documents.Parsed(ctx, invocation.ProjectID, args.AttachmentID)
	if err != nil {
		return tool.Result{}, err
	}
	type unitInfo struct {
		Index      int    `json:"index"`
		Kind       string `json:"kind"`
		Locator    string `json:"locator"`
		Title      string `json:"title,omitempty"`
		Characters int    `json:"characters"`
	}
	limit := min(len(parsed.Units), 500)
	units := make([]unitInfo, 0, limit)
	for _, unit := range parsed.Units[:limit] {
		units = append(units, unitInfo{Index: unit.Index, Kind: unit.Kind, Locator: unit.Locator, Title: unit.Title, Characters: utf8.RuneCountInString(unit.Content)})
	}
	payload := struct {
		Attachment attachment.Attachment `json:"attachment"`
		Metadata   map[string]string     `json:"metadata"`
		Units      []unitInfo            `json:"units"`
		TotalUnits int                   `json:"totalUnits"`
		Truncated  bool                  `json:"truncated"`
	}{value, parsed.Metadata, units, len(parsed.Units), parsed.Truncated || len(units) < len(parsed.Units)}
	structured, err := json.Marshal(payload)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Status: tool.ResultSuccess, Text: fmt.Sprintf("%s contains %d readable units.", value.OriginalName, len(parsed.Units)), Structured: structured, Artifacts: []tool.ArtifactRef{{ID: value.ID, Name: value.OriginalName, MIMEType: value.MIMEType}}, Truncated: payload.Truncated}, nil
}

func (*ReadDocument) Definition(context.Context) (tool.Definition, error) {
	return documentReadDefinition(ReadDocumentName,
		"Read bounded locally extracted content from an attached PDF, DOCX, XLSX, TXT, Markdown, or CSV document. Use inspect first to discover precise locators.",
		json.RawMessage(`{"type":"object","additionalProperties":false,"required":["attachmentId"],"properties":{"attachmentId":{"type":"string","minLength":1,"maxLength":128},"locator":{"type":"string","maxLength":512},"offset":{"type":"integer","minimum":0,"maximum":8000000},"maxChars":{"type":"integer","minimum":1,"maximum":180000}}}`),
		json.RawMessage(`{"type":"object","required":["attachmentId","name","content","locators","offset","characters","truncated"],"properties":{"attachmentId":{"type":"string"},"name":{"type":"string"},"content":{"type":"string"},"locators":{"type":"array","items":{"type":"string"}},"offset":{"type":"integer"},"characters":{"type":"integer"},"truncated":{"type":"boolean"}}}`)), nil
}

func (t *ReadDocument) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	var args struct {
		AttachmentID string `json:"attachmentId"`
		Locator      string `json:"locator"`
		Offset       int    `json:"offset"`
		MaxChars     int    `json:"maxChars"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.MaxChars == 0 {
		args.MaxChars = defaultDocumentRead
	}
	if args.MaxChars < 1 || args.MaxChars > maxDocumentRead || args.Offset < 0 {
		return tool.Result{}, fmt.Errorf("document read bounds are invalid")
	}
	value, parsed, err := t.documents.Parsed(ctx, invocation.ProjectID, args.AttachmentID)
	if err != nil {
		return tool.Result{}, err
	}
	selected := make([]document.Unit, 0)
	for _, unit := range parsed.Units {
		if strings.TrimSpace(args.Locator) == "" || strings.EqualFold(unit.Locator, strings.TrimSpace(args.Locator)) {
			selected = append(selected, unit)
		}
	}
	if len(selected) == 0 {
		return tool.Result{}, fmt.Errorf("document locator was not found")
	}
	var content strings.Builder
	locators := make([]string, 0, len(selected))
	seenLocators := make(map[string]struct{}, len(selected))
	for _, unit := range selected {
		writeDocumentExcerpt(&content, value.OriginalName, unit.Locator, unit.Title, unit.Content)
		if _, duplicate := seenLocators[unit.Locator]; !duplicate {
			seenLocators[unit.Locator] = struct{}{}
			locators = append(locators, unit.Locator)
		}
	}
	all := []rune(strings.TrimSpace(content.String()))
	if args.Offset > len(all) {
		return tool.Result{}, fmt.Errorf("document offset exceeds selected content")
	}
	end := min(len(all), args.Offset+args.MaxChars)
	text := string(all[args.Offset:end])
	truncated := end < len(all) || parsed.Truncated
	citations := make([]tool.CitationRef, 0, len(locators))
	for _, locator := range locators {
		citations = append(citations, tool.CitationRef{ID: value.ID, Locator: locator})
	}
	payload := struct {
		AttachmentID string   `json:"attachmentId"`
		Name         string   `json:"name"`
		Content      string   `json:"content"`
		Locators     []string `json:"locators"`
		Offset       int      `json:"offset"`
		Characters   int      `json:"characters"`
		Truncated    bool     `json:"truncated"`
	}{value.ID, value.OriginalName, text, locators, args.Offset, len([]rune(text)), truncated}
	structured, err := json.Marshal(payload)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Status: tool.ResultSuccess, Text: text, Structured: structured, Artifacts: []tool.ArtifactRef{{ID: value.ID, Name: value.OriginalName, MIMEType: value.MIMEType}}, Citations: citations, Truncated: truncated}, nil
}

func (*SearchDocument) Definition(context.Context) (tool.Definition, error) {
	return documentReadDefinition(SearchDocumentName,
		"Search the local parsed content of one project attachment and return bounded snippets with exact source locators.",
		json.RawMessage(`{"type":"object","additionalProperties":false,"required":["attachmentId","query"],"properties":{"attachmentId":{"type":"string","minLength":1,"maxLength":128},"query":{"type":"string","minLength":1,"maxLength":200},"limit":{"type":"integer","minimum":1,"maximum":20}}}`),
		json.RawMessage(`{"type":"object","required":["attachmentId","query","matches"],"properties":{"attachmentId":{"type":"string"},"query":{"type":"string"},"matches":{"type":"array","items":{"type":"object"}}}}`)), nil
}

func (t *SearchDocument) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	var args struct {
		AttachmentID string `json:"attachmentId"`
		Query        string `json:"query"`
		Limit        int    `json:"limit"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	args.Query = strings.TrimSpace(args.Query)
	if args.Query == "" {
		return tool.Result{}, fmt.Errorf("search query is required")
	}
	if args.Limit == 0 {
		args.Limit = 8
	}
	if args.Limit < 1 || args.Limit > 20 {
		return tool.Result{}, fmt.Errorf("search result limit is invalid")
	}
	value, parsed, err := t.documents.Parsed(ctx, invocation.ProjectID, args.AttachmentID)
	if err != nil {
		return tool.Result{}, err
	}
	type match struct {
		Locator string `json:"locator"`
		Title   string `json:"title,omitempty"`
		Snippet string `json:"snippet"`
	}
	matches := make([]match, 0, args.Limit)
	needle := []rune(strings.ToLower(args.Query))
	for _, unit := range parsed.Units {
		if err := ctx.Err(); err != nil {
			return tool.Result{}, err
		}
		haystack := []rune(strings.ToLower(unit.Content))
		index := runeSliceIndex(haystack, needle)
		if index < 0 {
			continue
		}
		original := []rune(unit.Content)
		start, end := max(0, index-240), min(len(original), index+len(needle)+360)
		matches = append(matches, match{Locator: unit.Locator, Title: unit.Title, Snippet: strings.TrimSpace(string(original[start:end]))})
		if len(matches) == args.Limit {
			break
		}
	}
	payload := struct {
		AttachmentID string  `json:"attachmentId"`
		Query        string  `json:"query"`
		Matches      []match `json:"matches"`
	}{value.ID, args.Query, matches}
	structured, err := json.Marshal(payload)
	if err != nil {
		return tool.Result{}, err
	}
	var text strings.Builder
	citations := make([]tool.CitationRef, 0, len(matches))
	seenCitations := make(map[string]struct{}, len(matches))
	for _, item := range matches {
		writeDocumentExcerpt(&text, value.OriginalName, item.Locator, item.Title, item.Snippet)
		if _, duplicate := seenCitations[item.Locator]; !duplicate {
			seenCitations[item.Locator] = struct{}{}
			citations = append(citations, tool.CitationRef{ID: value.ID, Locator: item.Locator})
		}
	}
	return tool.Result{Status: tool.ResultSuccess, Text: strings.TrimSpace(text.String()), Structured: structured, Artifacts: []tool.ArtifactRef{{ID: value.ID, Name: value.OriginalName, MIMEType: value.MIMEType}}, Citations: citations}, nil
}

func writeDocumentExcerpt(output *strings.Builder, name, locator, title, content string) {
	fmt.Fprintf(output, "[%s | %s", name, locator)
	if title = strings.TrimSpace(title); title != "" {
		fmt.Fprintf(output, " | %s", title)
	}
	fmt.Fprintf(output, "]\n%s\n\n", content)
}

func runeSliceIndex(value, query []rune) int {
	if len(query) == 0 || len(query) > len(value) {
		return -1
	}
	for index := 0; index+len(query) <= len(value); index++ {
		matched := true
		for offset := range query {
			if value[index+offset] != query[offset] {
				matched = false
				break
			}
		}
		if matched {
			return index
		}
	}
	return -1
}
