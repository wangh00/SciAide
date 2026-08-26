package connectors

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"strings"
	"time"

	appresearch "github.com/wangh00/SciAide/internal/app/research"
)

type pubMedConnector struct {
	client  *Client
	base    string
	host    string
	spacing time.Duration
}

type pubMedSearchResponse struct {
	Result struct {
		IDs []string `json:"idlist"`
	} `json:"esearchresult"`
}

type pubMedSummaryResponse struct {
	Result map[string]json.RawMessage `json:"result"`
}

type jsonRawSummary struct {
	UID             string `json:"uid"`
	Title           string `json:"title"`
	FullJournalName string `json:"fulljournalname"`
	Source          string `json:"source"`
	PubDate         string `json:"pubdate"`
	SortPubDate     string `json:"sortpubdate"`
	ELocationID     string `json:"elocationid"`
	Volume          string `json:"volume"`
	Issue           string `json:"issue"`
	Pages           string `json:"pages"`
	Authors         []struct {
		Name string `json:"name"`
	} `json:"authors"`
}

type pubMedArticleSet struct {
	Articles []pubMedArticle `xml:"PubmedArticle"`
}

type pubMedArticle struct {
	Citation struct {
		PMID    string `xml:"PMID"`
		Article struct {
			Title    string `xml:"ArticleTitle"`
			Abstract struct {
				Texts []struct {
					Label string `xml:"Label,attr"`
					Text  string `xml:",chardata"`
				} `xml:"AbstractText"`
			} `xml:"Abstract"`
			Journal struct {
				Title        string `xml:"Title"`
				ISO          string `xml:"ISOAbbreviation"`
				JournalIssue struct {
					Volume  string `xml:"Volume"`
					Issue   string `xml:"Issue"`
					PubDate struct {
						Year    string `xml:"Year"`
						Month   string `xml:"Month"`
						Day     string `xml:"Day"`
						Medline string `xml:"MedlineDate"`
					} `xml:"PubDate"`
				} `xml:"JournalIssue"`
			} `xml:"Journal"`
			Pagination struct {
				Pages string `xml:"MedlinePgn"`
			} `xml:"Pagination"`
			Authors []struct {
				LastName    string `xml:"LastName"`
				ForeName    string `xml:"ForeName"`
				Collective  string `xml:"CollectiveName"`
				Identifiers []struct {
					Source string `xml:"Source,attr"`
					Value  string `xml:",chardata"`
				} `xml:"Identifier"`
			} `xml:"AuthorList>Author"`
		} `xml:"Article"`
	} `xml:"MedlineCitation"`
	PubmedData struct {
		IDs []struct {
			Type  string `xml:"IdType,attr"`
			Value string `xml:",chardata"`
		} `xml:"ArticleIdList>ArticleId"`
	} `xml:"PubmedData"`
}

func newPubMed(client *Client, base string, spacing time.Duration) *pubMedConnector {
	return &pubMedConnector{client: client, base: strings.TrimRight(base, "/"), host: endpointHost(base), spacing: spacing}
}

func (c *pubMedConnector) Source() appresearch.Source {
	return appresearch.Source{ID: "pubmed", Name: "PubMed", Domain: "literature", Description: "Biomedical literature abstracts and citations from NCBI MEDLINE/PubMed.", Homepage: "https://pubmed.ncbi.nlm.nih.gov", Host: c.host, KeyFree: true, FullText: false}
}

func (c *pubMedConnector) Search(ctx context.Context, options appresearch.SearchOptions) ([]appresearch.Work, error) {
	query := url.Values{"db": {"pubmed"}, "retmode": {"json"}, "sort": {"relevance"}, "retmax": {fmt.Sprint(options.Limit)}, "term": {options.Query}}
	var search pubMedSearchResponse
	if err := c.client.getJSON(ctx, c.base+"/esearch.fcgi?"+query.Encode(), requestOptions{SourceID: "pubmed", Host: c.host, MinSpacing: c.spacing, Cache: true}, &search); err != nil {
		return nil, err
	}
	if len(search.Result.IDs) == 0 {
		return []appresearch.Work{}, nil
	}
	return c.summaries(ctx, search.Result.IDs)
}

func (c *pubMedConnector) Fetch(ctx context.Context, recordID string) (appresearch.Work, error) {
	id := strings.TrimSpace(recordID)
	for _, character := range id {
		if character < '0' || character > '9' {
			return appresearch.Work{}, &appresearch.SourceError{SourceID: "pubmed", Code: appresearch.FailureInvalidData, Message: "PubMed fetch requires a PMID"}
		}
	}
	if id == "" {
		return appresearch.Work{}, &appresearch.SourceError{SourceID: "pubmed", Code: appresearch.FailureInvalidData, Message: "PubMed fetch requires a PMID"}
	}
	query := url.Values{"db": {"pubmed"}, "retmode": {"xml"}, "id": {id}}
	body, _, err := c.client.get(ctx, c.base+"/efetch.fcgi?"+query.Encode(), requestOptions{SourceID: "pubmed", Host: c.host, MinSpacing: c.spacing, Cache: true})
	if err != nil {
		return appresearch.Work{}, err
	}
	var set pubMedArticleSet
	if err := xml.Unmarshal(body, &set); err != nil {
		return appresearch.Work{}, &appresearch.SourceError{SourceID: "pubmed", Code: appresearch.FailureInvalidData, Message: "PubMed returned malformed XML", Cause: err}
	}
	if len(set.Articles) == 0 {
		return appresearch.Work{}, &appresearch.SourceError{SourceID: "pubmed", Code: appresearch.FailureNotFound, Message: "PubMed article was not found"}
	}
	return pubMedArticleWork(set.Articles[0]), nil
}

func (c *pubMedConnector) summaries(ctx context.Context, ids []string) ([]appresearch.Work, error) {
	query := url.Values{"db": {"pubmed"}, "retmode": {"json"}, "id": {strings.Join(ids, ",")}}
	var response pubMedSummaryResponse
	if err := c.client.getJSON(ctx, c.base+"/esummary.fcgi?"+query.Encode(), requestOptions{SourceID: "pubmed", Host: c.host, MinSpacing: c.spacing, Cache: true}, &response); err != nil {
		return nil, err
	}
	result := make([]appresearch.Work, 0, len(ids))
	for _, id := range ids {
		raw, exists := response.Result[id]
		if !exists {
			continue
		}
		var record jsonRawSummary
		if err := json.Unmarshal(raw, &record); err != nil || strings.TrimSpace(record.UID) == "" {
			continue
		}
		authors := make([]appresearch.Author, 0, len(record.Authors))
		for _, author := range record.Authors {
			if strings.TrimSpace(author.Name) != "" {
				authors = append(authors, appresearch.Author{Name: author.Name, Source: "pubmed"})
			}
		}
		doi := strings.TrimSpace(record.ELocationID)
		if index := strings.Index(strings.ToLower(doi), "doi:"); index >= 0 {
			doi = strings.TrimSpace(doi[index+4:])
		}
		published := record.SortPubDate
		if published == "" {
			published = record.PubDate
		}
		result = append(result, appresearch.Work{
			SourceRecordID: record.UID, Title: plainText(record.Title), Authors: authors, Year: yearFrom(published),
			Published: dateString(published), Venue: firstNonEmpty(record.FullJournalName, record.Source), Volume: record.Volume,
			Issue: record.Issue, Pages: record.Pages, WorkType: "journal-article",
			Identifiers: appresearch.Identifiers{DOI: appresearch.NormalizeDOI(doi), PMID: record.UID},
			LandingURL:  "https://pubmed.ncbi.nlm.nih.gov/" + record.UID + "/", RawSnapshot: rawSnapshot(record),
		})
	}
	return result, nil
}

func pubMedArticleWork(record pubMedArticle) appresearch.Work {
	article := record.Citation.Article
	authors := make([]appresearch.Author, 0, len(article.Authors))
	for _, author := range article.Authors {
		name := strings.TrimSpace(strings.Join([]string{author.ForeName, author.LastName}, " "))
		if name == "" {
			name = strings.TrimSpace(author.Collective)
		}
		orcid := ""
		for _, identifier := range author.Identifiers {
			if strings.EqualFold(identifier.Source, "ORCID") {
				orcid = strings.TrimSpace(identifier.Value)
			}
		}
		if name != "" {
			authors = append(authors, appresearch.Author{Name: name, ORCID: orcid, Source: "pubmed"})
		}
	}
	abstracts := make([]string, 0, len(article.Abstract.Texts))
	for _, item := range article.Abstract.Texts {
		text := plainText(item.Text)
		if item.Label != "" && text != "" {
			text = item.Label + ": " + text
		}
		if text != "" {
			abstracts = append(abstracts, text)
		}
	}
	identifiers := appresearch.Identifiers{PMID: record.Citation.PMID}
	for _, item := range record.PubmedData.IDs {
		switch strings.ToLower(item.Type) {
		case "doi":
			identifiers.DOI = item.Value
		case "pmc":
			identifiers.PMCID = item.Value
		}
	}
	identifiers.DOI = appresearch.NormalizeDOI(identifiers.DOI)
	date := article.Journal.JournalIssue.PubDate
	published := strings.TrimSpace(strings.Join([]string{date.Year, date.Month, date.Day}, " "))
	if published == "" {
		published = date.Medline
	}
	return appresearch.Work{
		SourceRecordID: record.Citation.PMID, Title: plainText(article.Title), Abstract: strings.Join(abstracts, "\n\n"),
		Authors: authors, Year: yearFrom(published), Published: dateString(published), Venue: firstNonEmpty(article.Journal.Title, article.Journal.ISO),
		Volume: article.Journal.JournalIssue.Volume, Issue: article.Journal.JournalIssue.Issue, Pages: article.Pagination.Pages,
		WorkType: "journal-article", Identifiers: identifiers,
		LandingURL: "https://pubmed.ncbi.nlm.nih.gov/" + record.Citation.PMID + "/", RawSnapshot: rawSnapshot(record),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
