package connectors

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	research "github.com/wangh00/SciAide/internal/app/research"
)

// This is deliberately a metadata-only contract. Public API fullText notices,
// discovery redirects and arbitrary repository links never become local text
// evidence or a PDFURL. Registered/full-text access needs a separate contract.
type coreConnector struct {
	client     *Client
	base, host string
	spacing    time.Duration
}
type coreResponse struct {
	searchOffset int
	Total        *int         `json:"totalHits"`
	Results      []coreRecord `json:"results"`
}

func (r *coreResponse) validateSourceResponse() error {
	if r.Total == nil || *r.Total < 0 || r.Results == nil {
		return &research.SourceError{SourceID: "core", Code: research.FailureInvalidData, Message: "CORE response is missing a valid totalHits/results envelope"}
	}
	if len(r.Results) == 0 && r.searchOffset < *r.Total || len(r.Results) > 0 && *r.Total == 0 {
		return &research.SourceError{SourceID: "core", Code: research.FailureInvalidData, Message: "CORE result count contradicts its returned page"}
	}
	if len(r.Results) == 0 {
		return nil
	}
	for _, record := range r.Results {
		if usableSearchRecord("core", strconv.FormatInt(record.ID, 10), plainText(record.Title)) {
			return nil
		}
	}
	return &research.SourceError{SourceID: "core", Code: research.FailureInvalidData, Message: "CORE returned no valid work identities or titles"}
}

type coreRecord struct {
	fetchRecordID      string
	ID                 int64             `json:"id"`
	Title              string            `json:"title"`
	Abstract           string            `json:"abstract"`
	Authors            []json.RawMessage `json:"authors"`
	Year               int               `json:"yearPublished"`
	Published          string            `json:"publishedDate"`
	DOI                string            `json:"doi"`
	ArXiv              string            `json:"arxivId"`
	PMID               string            `json:"pubmedId"`
	Type               string            `json:"documentType"`
	Publisher          string            `json:"publisher"`
	Language           json.RawMessage   `json:"language"`
	License            string            `json:"license"`
	DownloadURL        string            `json:"downloadUrl"`
	SourceFullTextURLs []string          `json:"sourceFulltextUrls"`
	Journals           []struct {
		Title string `json:"title"`
	} `json:"journals"`
}

func newCORE(client *Client, base string, spacing time.Duration) *coreConnector {
	return &coreConnector{client: client, base: strings.TrimRight(base, "/"), host: endpointHost(base), spacing: spacing}
}
func (c *coreConnector) Source() research.Source {
	return research.Source{ID: "core", Name: "CORE (optional metadata)", Domain: "literature", Description: "Explicit-selection-only anonymous metadata search. Subject to CORE API quotas and applicable licence; raw fullText, discovery redirects and PDF downloads are not enabled. Never interpret an access notice as article text.", Homepage: "https://core.ac.uk", Host: c.host, KeyFree: true, FullText: false}
}
func (c *coreConnector) Search(ctx context.Context, options research.SearchOptions) ([]research.Work, error) {
	q := url.Values{"q": {options.Query}, "limit": {strconv.Itoa(options.Limit)}, "offset": {strconv.Itoa(options.Offset)}}
	applyPublicationYears("core", q, options.Years)
	response := coreResponse{searchOffset: options.Offset}
	if err := c.client.getJSON(ctx, c.base+"/search/works/?"+q.Encode(), requestOptions{SourceID: "core", Host: c.host, MinSpacing: c.spacing, Cache: true}, &response); err != nil {
		return nil, err
	}
	result := []research.Work{}
	for _, r := range response.Results {
		if r.ID > 0 && plainText(r.Title) != "" {
			result = append(result, coreWork(r))
		}
	}
	return result, nil
}
func (r *coreRecord) validateSourceResponse() error {
	return research.ValidateFetchedRecord("core", r.fetchRecordID, coreWork(*r))
}

func (c *coreConnector) Fetch(ctx context.Context, recordID string) (research.Work, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(recordID), 10, 64)
	if err != nil || id <= 0 {
		return research.Work{}, &research.SourceError{SourceID: "core", Code: research.FailureInvalidData, Message: "CORE work id must be a positive integer"}
	}
	record := coreRecord{fetchRecordID: strconv.FormatInt(id, 10)}
	if err := c.client.getJSON(ctx, c.base+"/works/"+strconv.FormatInt(id, 10), requestOptions{SourceID: "core", Host: c.host, MinSpacing: c.spacing, Cache: true}, &record); err != nil {
		return research.Work{}, err
	}
	return coreWork(record), nil
}
func coreWork(r coreRecord) research.Work {
	authors := []research.Author{}
	for _, raw := range r.Authors {
		var name string
		if json.Unmarshal(raw, &name) != nil {
			var value struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(raw, &value) != nil {
				continue
			}
			name = value.Name
		}
		if name = plainText(name); name != "" {
			authors = append(authors, research.Author{Name: name})
		}
	}
	language := ""
	if len(r.Language) > 0 && string(r.Language) != "null" {
		if json.Unmarshal(r.Language, &language) != nil {
			var value struct {
				Code string `json:"code"`
				Name string `json:"name"`
			}
			if json.Unmarshal(r.Language, &value) == nil {
				language = firstNonEmpty(value.Code, value.Name)
			}
		}
	}
	venue := ""
	for _, j := range r.Journals {
		if venue = plainText(j.Title); venue != "" {
			break
		}
	}
	id := strconv.FormatInt(r.ID, 10)
	return research.Work{SourceID: "core", SourceRecordID: id, Title: plainText(r.Title), Abstract: plainText(r.Abstract), Authors: authors, Year: r.Year, Published: dateString(r.Published), Venue: venue, Publisher: plainText(r.Publisher), WorkType: plainText(r.Type), Language: plainText(language), Identifiers: research.Identifiers{DOI: research.NormalizeDOI(r.DOI), ArXiv: research.NormalizeArXiv(r.ArXiv), PMID: r.PMID}, LandingURL: "https://core.ac.uk/works/" + id, SourceMetadata: &research.SourceMetadata{License: plainText(r.License), AccessNotice: "CORE metadata only; public fullText and PDF retrieval are not enabled by this connector."}, RawSnapshot: rawSnapshot(r)}
}
