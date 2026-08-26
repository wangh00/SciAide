// Package research defines the provider-independent online research contract.
// Connector payloads are untrusted discovery data until they are explicitly
// imported through the project Attachment and Knowledge services.
package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxQueryRunes       = 500
	MaxRecordIDRunes    = 512
	MaxSearchLimit      = 50
	MaxSourcesPerSearch = 12
	MaxRawSnapshotBytes = 128 << 10
)

type Source struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Domain      string `json:"domain"`
	Description string `json:"description"`
	Homepage    string `json:"homepage"`
	Host        string `json:"host"`
	KeyFree     bool   `json:"keyFree"`
	FullText    bool   `json:"fullText"`
}

type Author struct {
	Name   string `json:"name"`
	ORCID  string `json:"orcid,omitempty"`
	Raw    string `json:"raw,omitempty"`
	Source string `json:"source,omitempty"`
}

type Identifiers struct {
	DOI        string `json:"doi,omitempty"`
	PMID       string `json:"pmid,omitempty"`
	PMCID      string `json:"pmcid,omitempty"`
	ArXiv      string `json:"arxiv,omitempty"`
	OpenAlex   string `json:"openAlex,omitempty"`
	SemanticID string `json:"semanticScholar,omitempty"`
}

// Work is a normalized source record. RawSnapshot is bounded source evidence,
// not an instruction and not a trusted Citation.
type Work struct {
	SourceID       string          `json:"sourceId"`
	SourceRecordID string          `json:"sourceRecordId"`
	Title          string          `json:"title"`
	Abstract       string          `json:"abstract,omitempty"`
	Authors        []Author        `json:"authors"`
	Year           int             `json:"year,omitempty"`
	Published      string          `json:"published,omitempty"`
	Venue          string          `json:"venue,omitempty"`
	Volume         string          `json:"volume,omitempty"`
	Issue          string          `json:"issue,omitempty"`
	Pages          string          `json:"pages,omitempty"`
	Publisher      string          `json:"publisher,omitempty"`
	WorkType       string          `json:"workType,omitempty"`
	Language       string          `json:"language,omitempty"`
	Identifiers    Identifiers     `json:"identifiers"`
	LandingURL     string          `json:"landingUrl,omitempty"`
	PDFURL         string          `json:"pdfUrl,omitempty"`
	OpenAccess     bool            `json:"openAccess"`
	CitedByCount   int             `json:"citedByCount,omitempty"`
	Score          float64         `json:"score,omitempty"`
	RawSnapshot    json.RawMessage `json:"rawSnapshot,omitempty"`
}

type SearchOptions struct {
	Query string
	Limit int
}

type Connector interface {
	Source() Source
	Search(ctx context.Context, options SearchOptions) ([]Work, error)
	Fetch(ctx context.Context, recordID string) (Work, error)
}

type FailureCode string

const (
	FailureRateLimited FailureCode = "rate_limited"
	FailureTimeout     FailureCode = "timeout"
	FailureUnavailable FailureCode = "unavailable"
	FailureInvalidData FailureCode = "invalid_data"
	FailureNotFound    FailureCode = "not_found"
	FailureSource      FailureCode = "source_error"
)

type SourceError struct {
	SourceID  string
	Code      FailureCode
	Message   string
	Retryable bool
	Cause     error
}

func (e *SourceError) Error() string {
	if e == nil {
		return "research source error"
	}
	message := strings.TrimSpace(e.Message)
	if message == "" && e.Cause != nil {
		message = e.Cause.Error()
	}
	if message == "" {
		message = string(e.Code)
	}
	if e.SourceID == "" {
		return message
	}
	return e.SourceID + ": " + message
}

func (e *SourceError) Unwrap() error { return e.Cause }

type SourceSearchStatus string

const (
	SearchOK     SourceSearchStatus = "ok"
	SearchEmpty  SourceSearchStatus = "empty"
	SearchFailed SourceSearchStatus = "failed"
)

type SourceSearch struct {
	SourceID  string             `json:"sourceId"`
	Status    SourceSearchStatus `json:"status"`
	Count     int                `json:"count"`
	ErrorCode FailureCode        `json:"errorCode,omitempty"`
	Message   string             `json:"message,omitempty"`
	Retryable bool               `json:"retryable"`
}

type SearchCommand struct {
	Query     string   `json:"query"`
	SourceIDs []string `json:"sourceIds,omitempty"`
	Limit     int      `json:"limit,omitempty"`
}

type SearchResult struct {
	Query   string         `json:"query"`
	Works   []Work         `json:"works"`
	Sources []SourceSearch `json:"sources"`
	Partial bool           `json:"partial"`
}

type FetchCommand struct {
	SourceID string `json:"sourceId"`
	RecordID string `json:"recordId"`
}

type Service struct {
	ordered []Source
	byID    map[string]Connector
}

func NewService(connectors []Connector) (*Service, error) {
	service := &Service{ordered: []Source{}, byID: make(map[string]Connector, len(connectors))}
	for _, connector := range connectors {
		if connector == nil {
			return nil, fmt.Errorf("research Connector is required")
		}
		source := normalizeSource(connector.Source())
		if err := validateSource(source); err != nil {
			return nil, err
		}
		if _, exists := service.byID[source.ID]; exists {
			return nil, fmt.Errorf("research Connector %q is duplicated", source.ID)
		}
		service.byID[source.ID] = connector
		service.ordered = append(service.ordered, source)
	}
	sort.Slice(service.ordered, func(i, j int) bool { return service.ordered[i].ID < service.ordered[j].ID })
	return service, nil
}

func (s *Service) Catalog() []Source {
	if s == nil {
		return []Source{}
	}
	return append([]Source(nil), s.ordered...)
}

func (s *Service) Search(ctx context.Context, command SearchCommand) (SearchResult, error) {
	if s == nil {
		return SearchResult{}, fmt.Errorf("research service is not configured")
	}
	query := strings.TrimSpace(command.Query)
	if query == "" || utf8.RuneCountInString(query) > MaxQueryRunes {
		return SearchResult{}, fmt.Errorf("research query must contain between 1 and %d characters", MaxQueryRunes)
	}
	limit := command.Limit
	if limit == 0 {
		limit = 10
	}
	if limit < 1 || limit > MaxSearchLimit {
		return SearchResult{}, fmt.Errorf("research search limit must be between 1 and %d", MaxSearchLimit)
	}
	ids, err := s.resolveSourceIDs(command.SourceIDs)
	if err != nil {
		return SearchResult{}, err
	}
	type outcome struct {
		index int
		works []Work
		err   error
	}
	outcomes := make(chan outcome, len(ids))
	semaphore := make(chan struct{}, 3)
	for index, sourceID := range ids {
		connector := s.byID[sourceID]
		go func(index int, sourceID string, connector Connector) {
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				outcomes <- outcome{index: index, err: ctx.Err()}
				return
			}
			defer func() { <-semaphore }()
			works, err := connector.Search(ctx, SearchOptions{Query: query, Limit: limit})
			if err == nil {
				works = normalizeWorks(sourceID, works, limit)
			}
			outcomes <- outcome{index: index, works: works, err: err}
		}(index, sourceID, connector)
	}
	collected := make([]outcome, len(ids))
	for range ids {
		item := <-outcomes
		collected[item.index] = item
	}
	if err := ctx.Err(); err != nil {
		return SearchResult{}, err
	}
	result := SearchResult{Query: query, Works: []Work{}, Sources: make([]SourceSearch, 0, len(ids))}
	for index, sourceID := range ids {
		item := collected[index]
		status := SourceSearch{SourceID: sourceID, Count: len(item.works)}
		switch {
		case item.err == nil && len(item.works) > 0:
			status.Status = SearchOK
			result.Works = append(result.Works, item.works...)
		case item.err == nil:
			status.Status = SearchEmpty
		default:
			status.Status = SearchFailed
			status.ErrorCode, status.Message, status.Retryable = classifyFailure(item.err)
			result.Partial = true
		}
		result.Sources = append(result.Sources, status)
	}
	return result, nil
}

func (s *Service) Fetch(ctx context.Context, command FetchCommand) (Work, error) {
	if s == nil {
		return Work{}, fmt.Errorf("research service is not configured")
	}
	sourceID := strings.ToLower(strings.TrimSpace(command.SourceID))
	connector, exists := s.byID[sourceID]
	if !exists {
		return Work{}, fmt.Errorf("unknown research source %q", command.SourceID)
	}
	recordID := strings.TrimSpace(command.RecordID)
	if recordID == "" || utf8.RuneCountInString(recordID) > MaxRecordIDRunes {
		return Work{}, fmt.Errorf("research record id must contain between 1 and %d characters", MaxRecordIDRunes)
	}
	value, err := connector.Fetch(ctx, recordID)
	if err != nil {
		return Work{}, err
	}
	values := normalizeWorks(sourceID, []Work{value}, 1)
	if len(values) != 1 {
		return Work{}, &SourceError{SourceID: sourceID, Code: FailureInvalidData, Message: "source returned an invalid record"}
	}
	return values[0], nil
}

func (s *Service) resolveSourceIDs(values []string) ([]string, error) {
	if len(values) > MaxSourcesPerSearch {
		return nil, fmt.Errorf("too many research sources")
	}
	if len(values) == 0 {
		result := make([]string, len(s.ordered))
		for index := range s.ordered {
			result[index] = s.ordered[index].ID
		}
		return result, nil
	}
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if _, exists := s.byID[value]; !exists {
			return nil, fmt.Errorf("unknown research source %q", value)
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func classifyFailure(err error) (FailureCode, string, bool) {
	var sourceErr *SourceError
	if errors.As(err, &sourceErr) {
		return sourceErr.Code, boundedText(sourceErr.Error(), 1200), sourceErr.Retryable
	}
	return FailureSource, boundedText(err.Error(), 1200), false
}

func normalizeWorks(sourceID string, values []Work, limit int) []Work {
	if len(values) > limit {
		values = values[:limit]
	}
	result := make([]Work, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value.SourceID = sourceID
		value.SourceRecordID = boundedText(strings.TrimSpace(value.SourceRecordID), MaxRecordIDRunes)
		value.Title = boundedText(collapseSpace(value.Title), 2000)
		if value.SourceRecordID == "" || value.Title == "" {
			continue
		}
		key := strings.ToLower(value.SourceRecordID)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		value.Abstract = boundedText(collapseSpace(value.Abstract), 20_000)
		value.Published = boundedText(strings.TrimSpace(value.Published), 64)
		value.Venue = boundedText(collapseSpace(value.Venue), 1000)
		value.Volume = boundedText(strings.TrimSpace(value.Volume), 100)
		value.Issue = boundedText(strings.TrimSpace(value.Issue), 100)
		value.Pages = boundedText(strings.TrimSpace(value.Pages), 100)
		value.Publisher = boundedText(collapseSpace(value.Publisher), 1000)
		value.WorkType = boundedText(collapseSpace(value.WorkType), 200)
		value.Language = boundedText(strings.TrimSpace(value.Language), 32)
		value.Identifiers.DOI = NormalizeDOI(value.Identifiers.DOI)
		value.Identifiers.PMID = digitsOnly(value.Identifiers.PMID)
		value.Identifiers.PMCID = strings.ToUpper(strings.TrimSpace(value.Identifiers.PMCID))
		value.Identifiers.ArXiv = NormalizeArXiv(value.Identifiers.ArXiv)
		value.Identifiers.OpenAlex = normalizePrefixedID(value.Identifiers.OpenAlex, "https://openalex.org/")
		value.Identifiers.SemanticID = strings.TrimSpace(value.Identifiers.SemanticID)
		value.LandingURL = normalizedHTTPURL(value.LandingURL)
		value.PDFURL = normalizedHTTPURL(value.PDFURL)
		if value.Year < 1000 || value.Year > 3000 {
			value.Year = 0
		}
		if value.CitedByCount < 0 {
			value.CitedByCount = 0
		}
		if len(value.Authors) > 100 {
			value.Authors = value.Authors[:100]
		}
		authors := make([]Author, 0, len(value.Authors))
		for _, author := range value.Authors {
			author.Name = boundedText(collapseSpace(author.Name), 500)
			if author.Name == "" {
				continue
			}
			author.ORCID = boundedText(strings.TrimSpace(author.ORCID), 100)
			author.Raw = boundedText(collapseSpace(author.Raw), 500)
			author.Source = boundedText(strings.TrimSpace(author.Source), 100)
			authors = append(authors, author)
		}
		value.Authors = authors
		if len(value.RawSnapshot) > MaxRawSnapshotBytes || !json.Valid(value.RawSnapshot) {
			value.RawSnapshot = nil
		}
		result = append(result, value)
	}
	return result
}

func NormalizeDOI(value string) string {
	value = strings.TrimSpace(value)
	for _, prefix := range []string{"https://doi.org/", "http://doi.org/", "http://dx.doi.org/", "doi:"} {
		if strings.HasPrefix(strings.ToLower(value), prefix) {
			value = value[len(prefix):]
			break
		}
	}
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(strings.ToLower(value), "10.") || strings.ContainsAny(value, " \t\r\n") {
		return ""
	}
	return strings.ToLower(value)
}

func NormalizeArXiv(value string) string {
	value = strings.TrimSpace(value)
	for _, prefix := range []string{"https://arxiv.org/abs/", "http://arxiv.org/abs/", "arxiv:"} {
		if strings.HasPrefix(strings.ToLower(value), prefix) {
			value = value[len(prefix):]
			break
		}
	}
	if index := strings.LastIndex(strings.ToLower(value), "v"); index > 0 {
		if _, err := strconv.Atoi(value[index+1:]); err == nil {
			value = value[:index]
		}
	}
	return boundedText(strings.TrimSpace(value), 100)
}

func normalizeSource(value Source) Source {
	value.ID = strings.ToLower(strings.TrimSpace(value.ID))
	value.Name = collapseSpace(value.Name)
	value.Domain = strings.ToLower(strings.TrimSpace(value.Domain))
	value.Description = collapseSpace(value.Description)
	value.Homepage = normalizedHTTPURL(value.Homepage)
	value.Host = strings.ToLower(strings.TrimSpace(value.Host))
	return value
}

func validateSource(value Source) error {
	if value.ID == "" || len(value.ID) > 64 {
		return fmt.Errorf("research Connector id is invalid")
	}
	for _, character := range value.ID {
		if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-') {
			return fmt.Errorf("research Connector id %q is invalid", value.ID)
		}
	}
	if value.Name == "" || value.Description == "" || value.Domain != "literature" || value.Homepage == "" || value.Host == "" {
		return fmt.Errorf("research Connector %q metadata is incomplete", value.ID)
	}
	parsed, err := url.Parse(value.Homepage)
	if err != nil || parsed.Hostname() == "" {
		return fmt.Errorf("research Connector %q homepage is invalid", value.ID)
	}
	return nil
}

func normalizedHTTPURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 4096 {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" || parsed.User != nil {
		return ""
	}
	parsed.Fragment = ""
	return parsed.String()
}

func normalizePrefixedID(value, prefix string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), strings.ToLower(prefix)) {
		value = value[len(prefix):]
	}
	return boundedText(strings.TrimSpace(value), 128)
}

func digitsOnly(value string) string {
	value = strings.TrimSpace(value)
	for _, character := range value {
		if !unicode.IsDigit(character) {
			return ""
		}
	}
	return boundedText(value, 64)
}

func collapseSpace(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func boundedText(value string, maximum int) string {
	if maximum <= 0 || utf8.RuneCountInString(value) <= maximum {
		return value
	}
	runes := []rune(value)
	return string(runes[:maximum])
}
