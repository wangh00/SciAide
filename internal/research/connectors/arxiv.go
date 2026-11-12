package connectors

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"strings"
	"time"

	appresearch "github.com/wangh00/SciAide/internal/app/research"
)

type arxivConnector struct {
	client  *Client
	base    string
	host    string
	spacing time.Duration
}

type arxivFeed struct {
	XMLName xml.Name     `xml:"feed"`
	Entries []arxivEntry `xml:"entry"`
}

type arxivEntry struct {
	ID        string        `xml:"id" json:"id"`
	Updated   string        `xml:"updated" json:"updated"`
	Published string        `xml:"published" json:"published"`
	Title     string        `xml:"title" json:"title"`
	Summary   string        `xml:"summary" json:"summary"`
	DOI       string        `xml:"doi" json:"doi"`
	Journal   string        `xml:"journal_ref" json:"journalRef"`
	Authors   []arxivAuthor `xml:"author" json:"authors"`
	Links     []arxivLink   `xml:"link" json:"links"`
	Category  struct {
		Term string `xml:"term,attr" json:"term"`
	} `xml:"primary_category" json:"primaryCategory"`
}

type arxivAuthor struct {
	Name string `xml:"name" json:"name"`
}

type arxivLink struct {
	Href  string `xml:"href,attr" json:"href"`
	Title string `xml:"title,attr" json:"title"`
	Type  string `xml:"type,attr" json:"type"`
}

func newArXiv(client *Client, base string, spacing time.Duration) *arxivConnector {
	return &arxivConnector{client: client, base: strings.TrimRight(base, "/"), host: endpointHost(base), spacing: spacing}
}

func (c *arxivConnector) Source() appresearch.Source {
	return appresearch.Source{ID: "arxiv", Name: "arXiv", Domain: "literature", Description: "Open-access preprints in physics, mathematics, computer science and related fields.", Homepage: "https://arxiv.org", Host: c.host, KeyFree: true, FullText: true}
}

func (c *arxivConnector) Search(ctx context.Context, options appresearch.SearchOptions) ([]appresearch.Work, error) {
	expression, err := appresearch.ArXivQuery(options.Query)
	if err != nil {
		return nil, &appresearch.SourceError{SourceID: "arxiv", Code: appresearch.FailureInvalidData, Message: err.Error(), Cause: err}
	}
	query := url.Values{"search_query": {expression}, "start": {"0"}, "max_results": {fmt.Sprint(options.Limit)}, "sortBy": {"relevance"}}
	query.Set("start", fmt.Sprint(options.Offset))
	return c.feedRecord(ctx, c.base+"?"+query.Encode(), "", options.Limit)
}

func (c *arxivConnector) Fetch(ctx context.Context, recordID string) (appresearch.Work, error) {
	id := appresearch.CanonicalSourceRecordID("arxiv", recordID)
	if id == "" {
		return appresearch.Work{}, &appresearch.SourceError{SourceID: "arxiv", Code: appresearch.FailureInvalidData, Message: "arXiv record id is invalid"}
	}
	query := url.Values{"id_list": {id}, "max_results": {"1"}}
	works, err := c.feedRecord(ctx, c.base+"?"+query.Encode(), recordID, 1)
	if err != nil {
		return appresearch.Work{}, err
	}
	if len(works) == 0 {
		return appresearch.Work{}, &appresearch.SourceError{SourceID: "arxiv", Code: appresearch.FailureNotFound, Message: "arXiv preprint was not found"}
	}
	return works[0], nil
}

func (c *arxivConnector) feedRecord(ctx context.Context, target, recordID string, limit int) ([]appresearch.Work, error) {
	var result []appresearch.Work
	options := requestOptions{SourceID: "arxiv", Host: c.host, MinSpacing: c.spacing, Cache: true}
	options.validateBody = func(body []byte) error {
		var err error
		result, err = parseArXivFeed(body, recordID, limit)
		return err
	}
	_, _, err := c.client.get(ctx, target, options)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func parseArXivFeed(body []byte, recordID string, limit int) ([]appresearch.Work, error) {
	var feed arxivFeed
	if err := decodeSourceXML(body, &feed); err != nil || feed.XMLName.Local != "feed" {
		return nil, &appresearch.SourceError{SourceID: "arxiv", Code: appresearch.FailureInvalidData, Message: "arXiv returned malformed Atom XML", Cause: err}
	}
	result := make([]appresearch.Work, 0, len(feed.Entries))
	for _, entry := range feed.Entries {
		if strings.Contains(entry.ID, "/api/errors") || strings.EqualFold(strings.TrimSpace(entry.Title), "Error") {
			return nil, &appresearch.SourceError{SourceID: "arxiv", Code: appresearch.FailureInvalidData, Message: "arXiv rejected the query: " + plainText(entry.Summary)}
		}
		id := appresearch.CanonicalSourceRecordID("arxiv", entry.ID)
		if id == "" {
			continue
		}
		authors := make([]appresearch.Author, 0, len(entry.Authors))
		for _, author := range entry.Authors {
			if strings.TrimSpace(author.Name) != "" {
				authors = append(authors, appresearch.Author{Name: author.Name, Source: "arxiv"})
			}
		}
		pdf := ""
		for _, link := range entry.Links {
			if strings.EqualFold(link.Title, "pdf") || strings.Contains(strings.ToLower(link.Type), "pdf") {
				pdf = link.Href
				break
			}
		}
		result = append(result, appresearch.Work{
			SourceRecordID: id, Title: plainText(entry.Title), Abstract: plainText(entry.Summary), Authors: authors,
			Year: yearFrom(entry.Published), Published: dateString(entry.Published), Venue: entry.Journal,
			WorkType: "preprint", Identifiers: appresearch.Identifiers{DOI: appresearch.NormalizeDOI(entry.DOI), ArXiv: id},
			LandingURL: "https://arxiv.org/abs/" + id, PDFURL: pdf, OpenAccess: pdf != "",
			RawSnapshot: rawSnapshot(entry),
		})
	}
	if recordID != "" {
		if len(feed.Entries) == 0 {
			return result, nil // Cache legitimate empty lookups; Fetch reports not_found.
		}
		if len(feed.Entries) != 1 {
			return nil, &appresearch.SourceError{SourceID: "arxiv", Code: appresearch.FailureInvalidData, Message: "arXiv exact-id lookup did not return one record"}
		}
		// The transport URL/version is not the stored record ID. Canonicalize
		// without truncation before applying the same Fetch record contract.
		entry := feed.Entries[0]
		if err := appresearch.ValidateFetchedRecord("arxiv", recordID, appresearch.Work{SourceRecordID: appresearch.CanonicalSourceRecordID("arxiv", entry.ID), Title: plainText(entry.Title)}); err != nil {
			return nil, err
		}
	} else if err := validateSearchRecords("arxiv", len(feed.Entries), result, limit); err != nil {
		return nil, err
	}
	return result, nil
}
