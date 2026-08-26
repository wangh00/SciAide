package connectors

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	appresearch "github.com/wangh00/SciAide/internal/app/research"
)

const semanticFields = "paperId,title,abstract,url,year,publicationDate,venue,journal,publicationTypes,citationCount,externalIds,authors.name,authors.externalIds,openAccessPdf"

type semanticScholarConnector struct {
	client  *Client
	base    string
	host    string
	spacing time.Duration
}

type semanticSearchResponse struct {
	Data []semanticPaper `json:"data"`
}

type semanticPaper struct {
	PaperID          string            `json:"paperId"`
	Title            string            `json:"title"`
	Abstract         string            `json:"abstract"`
	URL              string            `json:"url"`
	Year             int               `json:"year"`
	PublicationDate  string            `json:"publicationDate"`
	Venue            string            `json:"venue"`
	CitationCount    int               `json:"citationCount"`
	PublicationTypes []string          `json:"publicationTypes"`
	ExternalIDs      map[string]string `json:"externalIds"`
	Journal          struct {
		Name   string `json:"name"`
		Volume string `json:"volume"`
		Pages  string `json:"pages"`
	} `json:"journal"`
	OpenAccessPDF struct {
		URL string `json:"url"`
	} `json:"openAccessPdf"`
	Authors []struct {
		Name        string            `json:"name"`
		ExternalIDs map[string]string `json:"externalIds"`
	} `json:"authors"`
}

func newSemanticScholar(client *Client, base string, spacing time.Duration) *semanticScholarConnector {
	return &semanticScholarConnector{client: client, base: strings.TrimRight(base, "/"), host: endpointHost(base), spacing: spacing}
}

func (c *semanticScholarConnector) Source() appresearch.Source {
	return appresearch.Source{ID: "semantic-scholar", Name: "Semantic Scholar", Domain: "literature", Description: "Academic graph with abstracts, citations and open-access links.", Homepage: "https://www.semanticscholar.org", Host: c.host, KeyFree: true, FullText: true}
}

func (c *semanticScholarConnector) Search(ctx context.Context, options appresearch.SearchOptions) ([]appresearch.Work, error) {
	query := url.Values{"query": {options.Query}, "limit": {fmt.Sprint(options.Limit)}, "fields": {semanticFields}}
	var response semanticSearchResponse
	if err := c.client.getJSON(ctx, c.base+"/search?"+query.Encode(), requestOptions{SourceID: "semantic-scholar", Host: c.host, MinSpacing: c.spacing, Cache: true}, &response); err != nil {
		return nil, err
	}
	return semanticWorks(response.Data), nil
}

func (c *semanticScholarConnector) Fetch(ctx context.Context, recordID string) (appresearch.Work, error) {
	id := strings.TrimSpace(recordID)
	if id == "" || strings.ContainsAny(id, "?#") {
		return appresearch.Work{}, &appresearch.SourceError{SourceID: "semantic-scholar", Code: appresearch.FailureInvalidData, Message: "Semantic Scholar record id is invalid"}
	}
	query := url.Values{"fields": {semanticFields}}
	var record semanticPaper
	if err := c.client.getJSON(ctx, c.base+"/"+url.PathEscape(id)+"?"+query.Encode(), requestOptions{SourceID: "semantic-scholar", Host: c.host, MinSpacing: c.spacing, Cache: true}, &record); err != nil {
		return appresearch.Work{}, err
	}
	values := semanticWorks([]semanticPaper{record})
	if len(values) == 0 {
		return appresearch.Work{}, &appresearch.SourceError{SourceID: "semantic-scholar", Code: appresearch.FailureNotFound, Message: "Semantic Scholar paper was not found"}
	}
	return values[0], nil
}

func semanticWorks(records []semanticPaper) []appresearch.Work {
	result := make([]appresearch.Work, 0, len(records))
	for _, record := range records {
		if strings.TrimSpace(record.PaperID) == "" {
			continue
		}
		authors := make([]appresearch.Author, 0, len(record.Authors))
		for _, author := range record.Authors {
			orcid := author.ExternalIDs["ORCID"]
			if strings.TrimSpace(author.Name) != "" {
				authors = append(authors, appresearch.Author{Name: author.Name, ORCID: orcid, Source: "semantic-scholar"})
			}
		}
		venue := record.Journal.Name
		if venue == "" {
			venue = record.Venue
		}
		workType := ""
		if len(record.PublicationTypes) > 0 {
			workType = strings.Join(record.PublicationTypes, ", ")
		}
		result = append(result, appresearch.Work{
			SourceRecordID: record.PaperID, Title: record.Title, Abstract: record.Abstract, Authors: authors,
			Year: record.Year, Published: record.PublicationDate, Venue: venue, Volume: record.Journal.Volume,
			Pages: record.Journal.Pages, WorkType: workType,
			Identifiers: appresearch.Identifiers{DOI: appresearch.NormalizeDOI(record.ExternalIDs["DOI"]), PMID: record.ExternalIDs["PubMed"],
				ArXiv: appresearch.NormalizeArXiv(record.ExternalIDs["ArXiv"]), SemanticID: record.PaperID},
			LandingURL: record.URL, PDFURL: record.OpenAccessPDF.URL, OpenAccess: record.OpenAccessPDF.URL != "",
			CitedByCount: record.CitationCount, RawSnapshot: rawSnapshot(record),
		})
	}
	return result
}
