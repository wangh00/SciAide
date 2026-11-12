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

func (r *pubMedSearchResponse) validateSourceResponse() error {
	if r.Result.IDs == nil {
		return &appresearch.SourceError{SourceID: "pubmed", Code: appresearch.FailureInvalidData, Message: "PubMed search response is missing esearchresult.idlist"}
	}
	seen := map[string]bool{}
	for _, id := range r.Result.IDs {
		key := appresearch.CanonicalSourceRecordID("pubmed", id)
		if key == "" || seen[key] {
			return &appresearch.SourceError{SourceID: "pubmed", Code: appresearch.FailureInvalidData, Message: "PubMed search returned invalid or duplicate PMIDs"}
		}
		seen[key] = true
	}
	return nil
}

type pubMedArticleSet struct {
	XMLName  xml.Name
	Articles []pubMedArticle `xml:"PubmedArticle"`
}

type pubMedText string

func (value *pubMedText) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	var content struct {
		Inner string `xml:",innerxml"`
	}
	if err := decoder.DecodeElement(&content, &start); err != nil {
		return err
	}
	*value = pubMedText(plainText(content.Inner))
	return nil
}

type pubMedArticle struct {
	Citation struct {
		PMID    string `xml:"PMID"`
		Article struct {
			Dates []struct {
				Year int `xml:"Year"`
			} `xml:"ArticleDate"`
			Title    pubMedText `xml:"ArticleTitle"`
			Abstract struct {
				Texts []struct {
					Label string `xml:"Label,attr"`
					Text  string `xml:",innerxml"`
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
	applyPublicationYears("pubmed", query, options.Years)
	if options.Offset > 0 {
		query.Set("retstart", fmt.Sprint(options.Offset))
	}
	var search pubMedSearchResponse
	if err := c.client.getJSON(ctx, c.base+"/esearch.fcgi?"+query.Encode(), requestOptions{SourceID: "pubmed", Host: c.host, MinSpacing: c.spacing, Cache: true}, &search); err != nil {
		return nil, err
	}
	if len(search.Result.IDs) == 0 {
		return []appresearch.Work{}, nil
	}
	ids := append([]string(nil), search.Result.IDs...)
	query = url.Values{"db": {"pubmed"}, "retmode": {"xml"}, "id": {strings.Join(ids, ",")}}
	var works []appresearch.Work
	request := requestOptions{SourceID: "pubmed", Host: c.host, MinSpacing: c.spacing, Cache: true}
	request.validateBody = func(body []byte) error {
		var err error
		works, err = validatePubMedBatch(body, ids, false)
		return err
	}
	_, _, err := c.client.get(ctx, c.base+"/efetch.fcgi?"+query.Encode(), request)
	return works, err
}

func (c *pubMedConnector) Fetch(ctx context.Context, recordID string) (appresearch.Work, error) {
	id := strings.TrimSpace(recordID)
	if appresearch.CanonicalSourceRecordID("pubmed", id) == "" {
		return appresearch.Work{}, &appresearch.SourceError{SourceID: "pubmed", Code: appresearch.FailureInvalidData, Message: "PubMed fetch requires a PMID"}
	}
	query := url.Values{"db": {"pubmed"}, "retmode": {"xml"}, "id": {id}}
	var works []appresearch.Work
	options := requestOptions{SourceID: "pubmed", Host: c.host, MinSpacing: c.spacing, Cache: true}
	options.validateBody = func(body []byte) error {
		var err error
		works, err = validatePubMedBatch(body, []string{id}, true)
		return err
	}
	_, _, err := c.client.get(ctx, c.base+"/efetch.fcgi?"+query.Encode(), options)
	if err != nil {
		return appresearch.Work{}, err
	}
	if len(works) == 0 {
		return appresearch.Work{}, &appresearch.SourceError{SourceID: "pubmed", Code: appresearch.FailureNotFound, Message: "PubMed article was not found"}
	}
	return works[0], nil
}

func validatePubMedBatch(body []byte, ids []string, allowNotFound bool) ([]appresearch.Work, error) {
	var set pubMedArticleSet
	if err := decodeSourceXML(body, &set); err != nil || set.XMLName.Local != "PubmedArticleSet" {
		return nil, &appresearch.SourceError{SourceID: "pubmed", Code: appresearch.FailureInvalidData, Message: "PubMed returned malformed abstract XML", Cause: err}
	}
	if len(set.Articles) == 0 && allowNotFound {
		return []appresearch.Work{}, nil
	}
	if len(set.Articles) != len(ids) {
		return nil, &appresearch.SourceError{SourceID: "pubmed", Code: appresearch.FailureInvalidData, Message: "PubMed abstract response did not match its requested batch"}
	}
	expected := map[string]bool{}
	for _, id := range ids {
		expected[appresearch.CanonicalSourceRecordID("pubmed", id)] = true
	}
	byID := map[string]appresearch.Work{}
	for _, article := range set.Articles {
		work := pubMedArticleWork(article)
		if len(ids) == 1 {
			if err := appresearch.ValidateFetchedRecord("pubmed", ids[0], work); err != nil {
				return nil, err
			}
		}
		key := appresearch.CanonicalSourceRecordID("pubmed", work.SourceRecordID)
		if !appresearch.IsUsableSourceRecord("pubmed", work) || !expected[key] {
			return nil, &appresearch.SourceError{SourceID: "pubmed", Code: appresearch.FailureInvalidData, Message: "PubMed abstract response returned an invalid or unexpected record"}
		}
		if _, duplicate := byID[key]; duplicate {
			return nil, &appresearch.SourceError{SourceID: "pubmed", Code: appresearch.FailureInvalidData, Message: "PubMed abstract response repeated a requested PMID"}
		}
		byID[key] = work
	}
	works := make([]appresearch.Work, 0, len(ids))
	for _, id := range ids {
		work, ok := byID[appresearch.CanonicalSourceRecordID("pubmed", id)]
		if !ok {
			return nil, &appresearch.SourceError{SourceID: "pubmed", Code: appresearch.FailureInvalidData, Message: "PubMed abstract response omitted a requested PMID"}
		}
		works = append(works, work)
	}
	return works, nil
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
	years := []int{}
	for _, date := range article.Dates {
		years = append(years, date.Year)
	}
	published := strings.TrimSpace(strings.Join([]string{date.Year, date.Month, date.Day}, " "))
	if published == "" {
		published = date.Medline
	}
	return appresearch.Work{
		SourceRecordID: record.Citation.PMID, Title: string(article.Title), Abstract: strings.Join(abstracts, "\n\n"),
		Authors: authors, Year: yearFrom(published), PublicationYears: years, Published: dateString(published), Venue: firstNonEmpty(article.Journal.Title, article.Journal.ISO),
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
