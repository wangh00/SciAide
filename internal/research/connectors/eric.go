package connectors

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	research "github.com/wangh00/SciAide/internal/app/research"
)

const ericFields = "id,title,author,description,subject,publicationtype,publicationdateyear,language,publisher,peerreviewed,e_fulltextauth,educationlevel,source,sourceid,url"

var ericRecordID = regexp.MustCompile(`^(ED|EJ)[0-9]{1,12}$`)

type ericConnector struct {
	client     *Client
	base, host string
	spacing    time.Duration
}
type ericResponse struct {
	fetchRecordID string
	searchOffset  int
	Response      *struct {
		NumFound *int         `json:"numFound"`
		Docs     []ericRecord `json:"docs"`
	} `json:"response"`
}

func (r *ericResponse) validateSourceResponse() error {
	if r.Response == nil || r.Response.NumFound == nil || *r.Response.NumFound < 0 || r.Response.Docs == nil {
		return &research.SourceError{SourceID: "eric", Code: research.FailureInvalidData, Message: "ERIC response is missing a valid response.numFound/docs envelope"}
	}
	if r.fetchRecordID != "" {
		if len(r.Response.Docs) == 0 && *r.Response.NumFound == 0 {
			return nil // Cache legitimate empty lookups; Fetch reports not_found.
		}
		if len(r.Response.Docs) != 1 || *r.Response.NumFound != 1 {
			return &research.SourceError{SourceID: "eric", Code: research.FailureInvalidData, Message: "ERIC exact-id lookup did not return one record"}
		}
		raw := r.Response.Docs[0]
		return research.ValidateFetchedRecord("eric", r.fetchRecordID, research.Work{SourceRecordID: strings.ToUpper(strings.TrimSpace(raw.ID)), Title: plainText(raw.Title)})
	}
	if len(r.Response.Docs) == 0 && r.searchOffset < *r.Response.NumFound || len(r.Response.Docs) > 0 && *r.Response.NumFound == 0 {
		return &research.SourceError{SourceID: "eric", Code: research.FailureInvalidData, Message: "ERIC result count contradicts its returned page"}
	}
	if len(r.Response.Docs) == 0 {
		return nil
	}
	for _, record := range r.Response.Docs {
		if usableSearchRecord("eric", strings.ToUpper(strings.TrimSpace(record.ID)), plainText(record.Title)) {
			return nil
		}
	}
	return &research.SourceError{SourceID: "eric", Code: research.FailureInvalidData, Message: "ERIC returned no valid record identities or titles"}
}

type ericRecord struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Authors         []string `json:"author"`
	Abstract        string   `json:"description"`
	Subjects        []string `json:"subject"`
	Types           []string `json:"publicationtype"`
	Year            int      `json:"publicationdateyear"`
	Languages       []string `json:"language"`
	Publisher       string   `json:"publisher"`
	PeerReviewed    string   `json:"peerreviewed"`
	FullText        *int     `json:"e_fulltextauth"`
	EducationLevels []string `json:"educationlevel"`
	Source          string   `json:"source"`
	SourceCitation  string   `json:"sourceid"`
	URL             string   `json:"url"`
}

func newERIC(client *Client, base string, spacing time.Duration) *ericConnector {
	return &ericConnector{client: client, base: base, host: endpointHost(base), spacing: spacing}
}
func (c *ericConnector) Source() research.Source {
	return research.Source{ID: "eric", Name: "ERIC", Domain: "literature", Description: "Education research, ERIC subject descriptors and education levels. Explicit full-text-display flags identify requestable ERIC PDFs, not verified reading or study quality.", Homepage: "https://eric.ed.gov", Host: c.host, KeyFree: true, FullText: true}
}
func (c *ericConnector) Search(ctx context.Context, options research.SearchOptions) ([]research.Work, error) {
	q := url.Values{"search": {options.Query}, "format": {"json"}, "start": {strconv.Itoa(options.Offset)}, "rows": {strconv.Itoa(options.Limit)}, "fields": {ericFields}}
	applyPublicationYears("eric", q, options.Years)
	return c.query(ctx, q, "")
}
func (c *ericConnector) Fetch(ctx context.Context, recordID string) (research.Work, error) {
	id := strings.ToUpper(strings.TrimSpace(recordID))
	if !ericRecordID.MatchString(id) {
		return research.Work{}, &research.SourceError{SourceID: "eric", Code: research.FailureInvalidData, Message: "invalid ERIC record id"}
	}
	records, err := c.query(ctx, url.Values{"search": {"id:" + id}, "format": {"json"}, "start": {"0"}, "rows": {"1"}, "fields": {ericFields}}, id)
	if err != nil {
		return research.Work{}, err
	}
	if len(records) == 0 {
		return research.Work{}, &research.SourceError{SourceID: "eric", Code: research.FailureNotFound, Message: "ERIC record was not found"}
	}
	return records[0], nil
}
func (c *ericConnector) query(ctx context.Context, q url.Values, recordID string) ([]research.Work, error) {
	offset, _ := strconv.Atoi(q.Get("start"))
	response := ericResponse{fetchRecordID: recordID, searchOffset: offset}
	if err := c.client.getJSON(ctx, c.base+"?"+q.Encode(), requestOptions{SourceID: "eric", Host: c.host, MinSpacing: c.spacing, Cache: true}, &response); err != nil {
		return nil, err
	}
	result := []research.Work{}
	for _, r := range response.Response.Docs {
		id := strings.ToUpper(strings.TrimSpace(r.ID))
		if !ericRecordID.MatchString(id) || plainText(r.Title) == "" {
			continue
		}
		authors := []research.Author{}
		for _, a := range r.Authors {
			if name := plainText(a); name != "" {
				authors = append(authors, research.Author{Name: name})
			}
		}
		metadata := &research.SourceMetadata{Subjects: plainTerms(r.Subjects), EducationLevels: plainTerms(r.EducationLevels), PublicationTypes: plainTerms(r.Types), SourceCitation: plainText(r.SourceCitation)}
		if r.PeerReviewed == "T" || r.PeerReviewed == "F" {
			v := r.PeerReviewed == "T"
			metadata.PeerReviewed = &v
		}
		pdf := ""
		if r.FullText != nil && *r.FullText == 1 {
			pdf = "https://files.eric.ed.gov/fulltext/" + id + ".pdf"
		}
		result = append(result, research.Work{SourceID: "eric", SourceRecordID: id, Title: plainText(r.Title), Abstract: plainText(r.Abstract), Authors: authors, Year: r.Year, Venue: plainText(r.Source), Publisher: plainText(r.Publisher), WorkType: strings.Join(plainTerms(r.Types), "; "), Language: strings.Join(plainTerms(r.Languages), "; "), Identifiers: research.Identifiers{DOI: doiFromERICURL(r.URL)}, LandingURL: "https://eric.ed.gov/?id=" + id, PDFURL: pdf, OpenAccess: pdf != "", SourceMetadata: metadata, RawSnapshot: rawSnapshot(r)})
	}
	return result, nil
}
func doiFromERICURL(value string) string {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host != "doi.org" && host != "dx.doi.org" {
		return ""
	}
	return research.NormalizeDOI(strings.TrimPrefix(u.Path, "/"))
}
func plainTerms(values []string) []string {
	result := []string{}
	for _, v := range values {
		if v = plainText(v); v != "" {
			result = append(result, v)
		}
	}
	return result
}
