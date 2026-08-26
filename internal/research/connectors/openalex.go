package connectors

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	appresearch "github.com/wangh00/SciAide/internal/app/research"
)

type openAlexConnector struct {
	client *Client
	base   string
	host   string
}

type openAlexResponse struct {
	Results []openAlexWork `json:"results"`
}

type openAlexWork struct {
	ID                    string               `json:"id"`
	DOI                   string               `json:"doi"`
	Title                 string               `json:"title"`
	DisplayName           string               `json:"display_name"`
	PublicationYear       int                  `json:"publication_year"`
	PublicationDate       string               `json:"publication_date"`
	Type                  string               `json:"type"`
	Language              string               `json:"language"`
	CitedByCount          int                  `json:"cited_by_count"`
	RelevanceScore        float64              `json:"relevance_score"`
	AbstractInvertedIndex map[string][]int     `json:"abstract_inverted_index"`
	Authorships           []openAlexAuthorship `json:"authorships"`
	PrimaryLocation       openAlexLocation     `json:"primary_location"`
	BestOpenAccess        openAlexLocation     `json:"best_oa_location"`
	OpenAccess            struct {
		IsOA bool `json:"is_oa"`
	} `json:"open_access"`
}

type openAlexAuthorship struct {
	Author struct {
		DisplayName string `json:"display_name"`
		ORCID       string `json:"orcid"`
	} `json:"author"`
}

type openAlexLocation struct {
	LandingPageURL string `json:"landing_page_url"`
	PDFURL         string `json:"pdf_url"`
	Source         struct {
		DisplayName string `json:"display_name"`
	} `json:"source"`
}

func newOpenAlex(client *Client, base string) *openAlexConnector {
	return &openAlexConnector{client: client, base: strings.TrimRight(base, "/"), host: endpointHost(base)}
}

func (c *openAlexConnector) Source() appresearch.Source {
	return appresearch.Source{ID: "openalex", Name: "OpenAlex", Domain: "literature", Description: "Open scholarly graph of works, authors, venues and concepts.", Homepage: "https://openalex.org", Host: c.host, KeyFree: true, FullText: true}
}

func (c *openAlexConnector) Search(ctx context.Context, options appresearch.SearchOptions) ([]appresearch.Work, error) {
	query := url.Values{"search": {options.Query}, "per-page": {fmt.Sprint(options.Limit)}}
	var response openAlexResponse
	if err := c.client.getJSON(ctx, c.base+"?"+query.Encode(), requestOptions{SourceID: "openalex", Host: c.host, Cache: true}, &response); err != nil {
		return nil, err
	}
	return openAlexWorks(response.Results), nil
}

func (c *openAlexConnector) Fetch(ctx context.Context, recordID string) (appresearch.Work, error) {
	id := strings.TrimSpace(recordID)
	if strings.HasPrefix(strings.ToLower(id), "https://openalex.org/") {
		id = id[len("https://openalex.org/"):]
	}
	if !strings.HasPrefix(strings.ToUpper(id), "W") {
		return appresearch.Work{}, &appresearch.SourceError{SourceID: "openalex", Code: appresearch.FailureInvalidData, Message: "OpenAlex fetch requires a work id returned by search"}
	}
	var record openAlexWork
	if err := c.client.getJSON(ctx, c.base+"/"+url.PathEscape(id), requestOptions{SourceID: "openalex", Host: c.host, Cache: true}, &record); err != nil {
		return appresearch.Work{}, err
	}
	values := openAlexWorks([]openAlexWork{record})
	if len(values) == 0 {
		return appresearch.Work{}, &appresearch.SourceError{SourceID: "openalex", Code: appresearch.FailureNotFound, Message: "OpenAlex work was not found"}
	}
	return values[0], nil
}

func openAlexWorks(records []openAlexWork) []appresearch.Work {
	result := make([]appresearch.Work, 0, len(records))
	for _, record := range records {
		id := strings.TrimPrefix(strings.TrimSpace(record.ID), "https://openalex.org/")
		if id == "" {
			continue
		}
		title := record.DisplayName
		if title == "" {
			title = record.Title
		}
		authors := make([]appresearch.Author, 0, len(record.Authorships))
		for _, authorship := range record.Authorships {
			if strings.TrimSpace(authorship.Author.DisplayName) != "" {
				authors = append(authors, appresearch.Author{Name: authorship.Author.DisplayName, ORCID: authorship.Author.ORCID, Source: "openalex"})
			}
		}
		landing := record.PrimaryLocation.LandingPageURL
		pdf := record.BestOpenAccess.PDFURL
		if pdf == "" {
			pdf = record.PrimaryLocation.PDFURL
		}
		venue := record.PrimaryLocation.Source.DisplayName
		if venue == "" {
			venue = record.BestOpenAccess.Source.DisplayName
		}
		result = append(result, appresearch.Work{
			SourceRecordID: id, Title: title, Abstract: invertedAbstract(record.AbstractInvertedIndex), Authors: authors,
			Year: record.PublicationYear, Published: record.PublicationDate, Venue: venue, WorkType: record.Type,
			Language: record.Language, Identifiers: appresearch.Identifiers{DOI: appresearch.NormalizeDOI(record.DOI), OpenAlex: id},
			LandingURL: landing, PDFURL: pdf, OpenAccess: record.OpenAccess.IsOA || pdf != "",
			CitedByCount: record.CitedByCount, Score: record.RelevanceScore, RawSnapshot: rawSnapshot(record),
		})
	}
	return result
}
