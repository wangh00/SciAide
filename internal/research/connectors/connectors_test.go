package connectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appresearch "github.com/wangh00/SciAide/internal/app/research"
)

func fixtureServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	server := httptest.NewServer(handler)
	client := newTestClient(server.Client())
	client.attempts = 1
	return server, client
}

func TestOpenAlexCrossrefAndSemanticScholarFixtures(t *testing.T) {
	server, client := fixtureServer(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/openalex":
			_, _ = writer.Write([]byte(`{"results":[{"id":"https://openalex.org/W1","doi":"https://doi.org/10.1000/ABC","display_name":"Open work","publication_year":2025,"publication_date":"2025-02-03","cited_by_count":7,"abstract_inverted_index":{"hello":[0],"world":[1]},"authorships":[{"author":{"display_name":"Ada Lovelace","orcid":"https://orcid.org/0000"}}],"primary_location":{"landing_page_url":"https://example.test/work","source":{"display_name":"Journal"}},"best_oa_location":{"pdf_url":"https://example.test/work.pdf"},"open_access":{"is_oa":true}}]}`))
		case "/crossref":
			_, _ = writer.Write([]byte(`{"message":{"items":[{"DOI":"10.1000/ABC","title":["Cross work"],"abstract":"<jats:p>Evidence</jats:p>","author":[{"given":"Ada","family":"Lovelace"}],"container-title":["Journal"],"issued":{"date-parts":[[2024,3,2]]},"URL":"https://doi.org/10.1000/ABC"}]}}`))
		case "/semantic/search":
			_, _ = writer.Write([]byte(`{"data":[{"paperId":"S1","title":"Semantic work","abstract":"Abstract","year":2023,"externalIds":{"DOI":"10.1000/ABC","PubMed":"123"},"authors":[{"name":"Ada"}],"openAccessPdf":{"url":"https://example.test/open.pdf"}}]}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()
	ctx := context.Background()
	openWorks, err := newOpenAlex(client, server.URL+"/openalex").Search(ctx, searchOptions("q"))
	if err != nil || len(openWorks) != 1 || openWorks[0].Abstract != "hello world" || openWorks[0].Identifiers.DOI != "10.1000/abc" || !openWorks[0].OpenAccess {
		t.Fatalf("OpenAlex works=%#v err=%v", openWorks, err)
	}
	crossWorks, err := newCrossref(client, server.URL+"/crossref").Search(ctx, searchOptions("q"))
	if err != nil || len(crossWorks) != 1 || crossWorks[0].Abstract != "Evidence" || crossWorks[0].Year != 2024 {
		t.Fatalf("Crossref works=%#v err=%v", crossWorks, err)
	}
	semanticWorks, err := newSemanticScholar(client, server.URL+"/semantic", 0).Search(ctx, searchOptions("q"))
	if err != nil || len(semanticWorks) != 1 || semanticWorks[0].Identifiers.PMID != "123" || semanticWorks[0].PDFURL == "" {
		t.Fatalf("Semantic works=%#v err=%v", semanticWorks, err)
	}
}

func TestArXivPubMedAndEuropePMCFixtures(t *testing.T) {
	server, client := fixtureServer(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/arxiv":
			writer.Header().Set("Content-Type", "application/atom+xml")
			_, _ = writer.Write([]byte(`<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom" xmlns:arxiv="http://arxiv.org/schemas/atom"><entry><id>https://arxiv.org/abs/2401.01234v2</id><updated>2024-01-03T00:00:00Z</updated><published>2024-01-02T00:00:00Z</published><title> An arXiv work </title><summary> Evidence text </summary><author><name>Ada Lovelace</name></author><arxiv:doi>10.1000/ABC</arxiv:doi><arxiv:primary_category term="cs.AI"/><link title="pdf" href="https://arxiv.org/pdf/2401.01234" type="application/pdf"/></entry></feed>`))
		case "/pubmed/esearch.fcgi":
			_, _ = writer.Write([]byte(`{"esearchresult":{"idlist":["123"]}}`))
		case "/pubmed/esummary.fcgi":
			_, _ = writer.Write([]byte(`{"result":{"uids":["123"],"123":{"uid":"123","title":"PubMed work","fulljournalname":"Journal","pubdate":"2022 Jan","authors":[{"name":"Lovelace A"}],"elocationid":"doi: 10.1000/ABC"}}}`))
		case "/pubmed/efetch.fcgi":
			writer.Header().Set("Content-Type", "application/xml")
			_, _ = writer.Write([]byte(`<?xml version="1.0"?><PubmedArticleSet><PubmedArticle><MedlineCitation><PMID>123</PMID><Article><ArticleTitle>PubMed full</ArticleTitle><Abstract><AbstractText Label="BACKGROUND">Evidence</AbstractText></Abstract><AuthorList><Author><LastName>Lovelace</LastName><ForeName>Ada</ForeName><Identifier Source="ORCID">0000</Identifier></Author></AuthorList><Journal><Title>Journal</Title><JournalIssue><PubDate><Year>2022</Year><Month>01</Month><Day>02</Day></PubDate></JournalIssue></Journal></Article></MedlineCitation><PubmedData><ArticleIdList><ArticleId IdType="doi">10.1000/ABC</ArticleId><ArticleId IdType="pmc">PMC123</ArticleId></ArticleIdList></PubmedData></PubmedArticle></PubmedArticleSet>`))
		case "/europe/search":
			_, _ = writer.Write([]byte(`{"resultList":{"result":[{"id":"123","source":"MED","pmid":"123","pmcid":"PMC123","doi":"10.1000/ABC","title":"Europe work","authorString":"Ada Lovelace; Alan Turing","abstractText":"Evidence","pubYear":"2021","firstPublicationDate":"2021-02-03","journalInfo":{"journal":{"title":"Journal"}},"isOpenAccess":"Y","inEPMC":"Y"}]}}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()
	ctx := context.Background()
	arxivWorks, err := newArXiv(client, server.URL+"/arxiv", 0).Search(ctx, searchOptions("q"))
	if err != nil || len(arxivWorks) != 1 || arxivWorks[0].Identifiers.ArXiv != "2401.01234" || arxivWorks[0].PDFURL == "" || arxivWorks[0].Authors[0].Name != "Ada Lovelace" {
		t.Fatalf("arXiv works=%#v err=%v", arxivWorks, err)
	}
	pubmed := newPubMed(client, server.URL+"/pubmed", 0)
	pubmedWorks, err := pubmed.Search(ctx, searchOptions("q"))
	if err != nil || len(pubmedWorks) != 1 || pubmedWorks[0].Identifiers.DOI != "10.1000/abc" {
		t.Fatalf("PubMed search=%#v err=%v", pubmedWorks, err)
	}
	full, err := pubmed.Fetch(ctx, "123")
	if err != nil || !strings.Contains(full.Abstract, "BACKGROUND: Evidence") || full.Identifiers.PMCID != "PMC123" || full.Authors[0].ORCID != "0000" {
		t.Fatalf("PubMed fetch=%#v err=%v", full, err)
	}
	europeWorks, err := newEuropePMC(client, server.URL+"/europe").Search(ctx, searchOptions("q"))
	if err != nil || len(europeWorks) != 1 || len(europeWorks[0].Authors) != 2 || europeWorks[0].PDFURL == "" {
		t.Fatalf("Europe PMC works=%#v err=%v", europeWorks, err)
	}
}

func searchOptions(query string) appresearch.SearchOptions {
	return appresearch.SearchOptions{Query: query, Limit: 10}
}
