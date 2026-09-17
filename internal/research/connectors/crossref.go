package connectors

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	appresearch "github.com/wangh00/SciAide/internal/app/research"
)

type crossrefConnector struct {
	client *Client
	base   string
	host   string
}

type crossrefResponse struct {
	Message struct {
		Items []crossrefWork `json:"items"`
	} `json:"message"`
}

type crossrefSingle struct {
	Message crossrefWork `json:"message"`
}

type crossrefWork struct {
	DOI               string           `json:"DOI"`
	Title             []string         `json:"title"`
	Subtitle          []string         `json:"subtitle"`
	Abstract          string           `json:"abstract"`
	Authors           []crossrefAuthor `json:"author"`
	ContainerTitle    []string         `json:"container-title"`
	Publisher         string           `json:"publisher"`
	Type              string           `json:"type"`
	URL               string           `json:"URL"`
	Language          string           `json:"language"`
	Volume            string           `json:"volume"`
	Issue             string           `json:"issue"`
	Page              string           `json:"page"`
	Score             float64          `json:"score"`
	ReferencedByCount int              `json:"is-referenced-by-count"`
	Issued            crossrefDate     `json:"issued"`
	PublishedPrint    crossrefDate     `json:"published-print"`
	PublishedOnline   crossrefDate     `json:"published-online"`
	Links             []struct {
		URL         string `json:"URL"`
		ContentType string `json:"content-type"`
	} `json:"link"`
}

type crossrefAuthor struct {
	Given  string `json:"given"`
	Family string `json:"family"`
	Name   string `json:"name"`
	ORCID  string `json:"ORCID"`
}

type crossrefDate struct {
	DateParts [][]int `json:"date-parts"`
}

func newCrossref(client *Client, base string) *crossrefConnector {
	return &crossrefConnector{client: client, base: strings.TrimRight(base, "/"), host: endpointHost(base)}
}

func (c *crossrefConnector) Source() appresearch.Source {
	return appresearch.Source{ID: "crossref", Name: "Crossref", Domain: "literature", Description: "Cross-publisher DOI metadata, authors, venues and references.", Homepage: "https://www.crossref.org", Host: c.host, KeyFree: true, FullText: false}
}

func (c *crossrefConnector) Search(ctx context.Context, options appresearch.SearchOptions) ([]appresearch.Work, error) {
	query := url.Values{"query.bibliographic": {options.Query}, "rows": {fmt.Sprint(options.Limit)}, "sort": {"relevance"}}
	applyPublicationYears("crossref", query, options.Years)
	if options.Offset > 0 {
		query.Set("offset", fmt.Sprint(options.Offset))
	}
	var response crossrefResponse
	if err := c.client.getJSON(ctx, c.base+"?"+query.Encode(), requestOptions{SourceID: "crossref", Host: c.host, Cache: true}, &response); err != nil {
		return nil, err
	}
	return crossrefWorks(response.Message.Items), nil
}

func (c *crossrefConnector) Fetch(ctx context.Context, recordID string) (appresearch.Work, error) {
	doi := appresearch.NormalizeDOI(recordID)
	if doi == "" {
		return appresearch.Work{}, &appresearch.SourceError{SourceID: "crossref", Code: appresearch.FailureInvalidData, Message: "Crossref fetch requires a DOI"}
	}
	var response crossrefSingle
	if err := c.client.getJSON(ctx, c.base+"/"+url.PathEscape(doi), requestOptions{SourceID: "crossref", Host: c.host, Cache: true}, &response); err != nil {
		return appresearch.Work{}, err
	}
	values := crossrefWorks([]crossrefWork{response.Message})
	if len(values) == 0 {
		return appresearch.Work{}, &appresearch.SourceError{SourceID: "crossref", Code: appresearch.FailureNotFound, Message: "Crossref work was not found"}
	}
	return values[0], nil
}

func crossrefWorks(records []crossrefWork) []appresearch.Work {
	result := make([]appresearch.Work, 0, len(records))
	for _, record := range records {
		doi := appresearch.NormalizeDOI(record.DOI)
		if doi == "" {
			continue
		}
		title := firstString(record.Title)
		if subtitle := firstString(record.Subtitle); title != "" && subtitle != "" {
			title += ": " + subtitle
		}
		authors := make([]appresearch.Author, 0, len(record.Authors))
		for _, author := range record.Authors {
			name := strings.TrimSpace(author.Name)
			if name == "" {
				name = strings.TrimSpace(strings.Join([]string{author.Given, author.Family}, " "))
			}
			if name != "" {
				authors = append(authors, appresearch.Author{Name: name, ORCID: author.ORCID, Source: "crossref"})
			}
		}
		published, year := crossrefPublished(record)
		years := []int{}
		for _, date := range []crossrefDate{record.PublishedPrint, record.PublishedOnline, record.Issued} {
			if len(date.DateParts) > 0 && len(date.DateParts[0]) > 0 {
				years = append(years, date.DateParts[0][0])
			}
		}
		pdf := ""
		for _, link := range record.Links {
			if strings.Contains(strings.ToLower(link.ContentType), "pdf") {
				pdf = link.URL
				break
			}
		}
		landing := record.URL
		if landing == "" {
			landing = "https://doi.org/" + doi
		}
		result = append(result, appresearch.Work{
			SourceRecordID: doi, Title: title, Abstract: plainText(record.Abstract), Authors: authors,
			Year: year, Published: published, PublicationYears: years, Venue: firstString(record.ContainerTitle), Volume: record.Volume,
			Issue: record.Issue, Pages: record.Page, Publisher: record.Publisher, WorkType: record.Type,
			Language: record.Language, Identifiers: appresearch.Identifiers{DOI: doi}, LandingURL: landing,
			PDFURL: pdf, OpenAccess: pdf != "", CitedByCount: record.ReferencedByCount, Score: record.Score,
			RawSnapshot: rawSnapshot(record),
		})
	}
	return result
}

func crossrefPublished(record crossrefWork) (string, int) {
	for _, value := range []crossrefDate{record.PublishedPrint, record.PublishedOnline, record.Issued} {
		if len(value.DateParts) == 0 || len(value.DateParts[0]) == 0 {
			continue
		}
		parts := value.DateParts[0]
		published := fmt.Sprintf("%04d", parts[0])
		if len(parts) >= 2 {
			published += fmt.Sprintf("-%02d", parts[1])
		}
		if len(parts) >= 3 {
			published += fmt.Sprintf("-%02d", parts[2])
		}
		return published, parts[0]
	}
	return "", 0
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}
