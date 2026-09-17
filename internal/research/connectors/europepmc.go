package connectors

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	appresearch "github.com/wangh00/SciAide/internal/app/research"
)

type europePMCConnector struct {
	client *Client
	base   string
	host   string
}

type europePMCResponse struct {
	NextCursor string `json:"nextCursorMark"`
	Results    struct {
		Items []europePMCRecord `json:"result"`
	} `json:"resultList"`
}

type europePMCRecord struct {
	ID              string `json:"id"`
	Source          string `json:"source"`
	PMID            string `json:"pmid"`
	PMCID           string `json:"pmcid"`
	DOI             string `json:"doi"`
	Title           string `json:"title"`
	AuthorString    string `json:"authorString"`
	Abstract        string `json:"abstractText"`
	PublicationYear string `json:"pubYear"`
	FirstDate       string `json:"firstPublicationDate"`
	JournalTitle    string `json:"journalTitle"`
	JournalInfo     struct {
		Volume  string `json:"volume"`
		Issue   string `json:"issue"`
		Pages   string `json:"pageInfo"`
		Journal struct {
			Title string `json:"title"`
		} `json:"journal"`
	} `json:"journalInfo"`
	PublicationType string `json:"pubType"`
	Language        string `json:"language"`
	CitedByCount    int    `json:"citedByCount"`
	IsOpenAccess    string `json:"isOpenAccess"`
	InPMC           string `json:"inEPMC"`
}

func newEuropePMC(client *Client, base string) *europePMCConnector {
	return &europePMCConnector{client: client, base: strings.TrimRight(base, "/"), host: endpointHost(base)}
}

func (c *europePMCConnector) Source() appresearch.Source {
	return appresearch.Source{ID: "europepmc", Name: "Europe PMC", Domain: "literature", Description: "Life-science literature, abstracts and open full text via EMBL-EBI.", Homepage: "https://europepmc.org", Host: c.host, KeyFree: true, FullText: true}
}

func (c *europePMCConnector) Search(ctx context.Context, options appresearch.SearchOptions) ([]appresearch.Work, error) {
	query := url.Values{"query": {options.Query}, "format": {"json"}, "resultType": {"core"}, "pageSize": {fmt.Sprint(options.Limit)}}
	applyPublicationYears("europepmc", query, options.Years)
	if options.Limit <= 0 || options.Offset%options.Limit != 0 {
		return nil, fmt.Errorf("Europe PMC offset must align to page size")
	}
	cursor := "*"
	for offset := 0; offset <= options.Offset; offset += options.Limit {
		query.Set("cursorMark", cursor)
		var response europePMCResponse
		if err := c.client.getJSON(ctx, c.base+"/search?"+query.Encode(), requestOptions{SourceID: "europepmc", Host: c.host, Cache: true}, &response); err != nil {
			return nil, err
		}
		if offset == options.Offset {
			if response.Results.Items == nil {
				return nil, fmt.Errorf("Europe PMC response is missing resultList.result")
			}
			return europePMCWorks(response.Results.Items), nil
		}
		if len(response.Results.Items) == 0 {
			return []appresearch.Work{}, nil
		}
		if response.NextCursor == "" || response.NextCursor == cursor {
			return nil, &appresearch.SourceError{SourceID: "europepmc", Code: appresearch.FailureInvalidData, Message: "Europe PMC pagination did not advance"}
		}
		cursor = response.NextCursor
	}
	return []appresearch.Work{}, nil
}

func (c *europePMCConnector) Fetch(ctx context.Context, recordID string) (appresearch.Work, error) {
	id := strings.TrimSpace(recordID)
	queryText := id
	if source, value, found := strings.Cut(id, "/"); found && strings.TrimSpace(source) != "" && strings.TrimSpace(value) != "" {
		queryText = "EXT_ID:" + value + " AND SRC:" + source
	}
	query := url.Values{"query": {queryText}, "format": {"json"}, "resultType": {"core"}, "pageSize": {"1"}}
	var response europePMCResponse
	if err := c.client.getJSON(ctx, c.base+"/search?"+query.Encode(), requestOptions{SourceID: "europepmc", Host: c.host, Cache: true}, &response); err != nil {
		return appresearch.Work{}, err
	}
	values := europePMCWorks(response.Results.Items)
	if len(values) == 0 {
		return appresearch.Work{}, &appresearch.SourceError{SourceID: "europepmc", Code: appresearch.FailureNotFound, Message: "Europe PMC record was not found"}
	}
	return values[0], nil
}

func europePMCWorks(records []europePMCRecord) []appresearch.Work {
	result := make([]appresearch.Work, 0, len(records))
	for _, record := range records {
		id := strings.TrimSpace(record.ID)
		if record.Source != "" && id != "" {
			id = record.Source + "/" + id
		}
		if id == "" {
			continue
		}
		venue := firstNonEmpty(record.JournalInfo.Journal.Title, record.JournalTitle)
		landing := "https://europepmc.org/article/" + id
		pdf := ""
		if record.PMCID != "" && (strings.EqualFold(record.IsOpenAccess, "Y") || strings.EqualFold(record.InPMC, "Y")) {
			pdf = "https://europepmc.org/articles/" + record.PMCID + "?pdf=render"
		}
		year, _ := strconv.Atoi(record.PublicationYear)
		result = append(result, appresearch.Work{
			SourceRecordID: id, Title: plainText(record.Title), Abstract: plainText(record.Abstract), Authors: splitAuthorString(record.AuthorString),
			Year: year, Published: dateString(record.FirstDate), Venue: venue, Volume: record.JournalInfo.Volume,
			Issue: record.JournalInfo.Issue, Pages: record.JournalInfo.Pages, WorkType: record.PublicationType, Language: record.Language,
			Identifiers: appresearch.Identifiers{DOI: appresearch.NormalizeDOI(record.DOI), PMID: record.PMID, PMCID: record.PMCID},
			LandingURL:  landing, PDFURL: pdf, OpenAccess: pdf != "", CitedByCount: record.CitedByCount,
			RawSnapshot: rawSnapshot(record),
		})
	}
	return result
}
