package connectors

import (
	"context"
	"encoding/xml"
	"github.com/wangh00/SciAide/internal/app/research"
	"net/http"
	"strings"
	"testing"
)

func TestPublicationDateVariantsSurviveNormalization(t *testing.T) {
	record := crossrefWork{DOI: "10.5555/dates", Title: []string{"Date variants"}, PublishedPrint: crossrefDate{DateParts: [][]int{{2026, 1}}}, PublishedOnline: crossrefDate{DateParts: [][]int{{2025, 12}}}}
	work := crossrefWorks([]crossrefWork{record})[0]
	if len(work.PublicationYears) != 2 || work.PublicationYears[0] != 2026 || work.PublicationYears[1] != 2025 {
		t.Fatal(work)
	}
	var article pubMedArticle
	if err := xml.Unmarshal([]byte(`<PubmedArticle><MedlineCitation><PMID>1</PMID><Article><ArticleTitle>Date variants</ArticleTitle><Journal><JournalIssue><PubDate><Year>2026</Year></PubDate></JournalIssue></Journal><ArticleDate DateType="Electronic"><Year>2025</Year></ArticleDate></Article></MedlineCitation></PubmedArticle>`), &article); err != nil {
		t.Fatal(err)
	}
	work = pubMedArticleWork(article)
	if work.Year != 2026 || len(work.PublicationYears) != 1 || work.PublicationYears[0] != 2025 {
		t.Fatal(work)
	}
}

func TestPublicationYearsReachNativeEndpoints(t *testing.T) {
	seen := map[string]bool{}
	server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path] = true
		q := r.URL.Query()
		response := ""
		switch r.URL.Path {
		case "/pubmed/esearch.fcgi":
			if q.Get("datetype") != "pdat" || q.Get("mindate") != "2020" || q.Get("maxdate") != "2025" {
				t.Error(q)
			}
			response = `{"esearchresult":{"idlist":[]}}`
		case "/crossref":
			if q.Get("filter") != "from-pub-date:2020-01-01,until-pub-date:2025-12-31" {
				t.Error(q)
			}
			response = `{"message":{"items":[]}}`
		case "/openalex":
			if q.Get("filter") != "from_publication_date:2020-01-01,to_publication_date:2025-12-31" {
				t.Error(q)
			}
			response = `{"results":[]}`
		case "/europe/search":
			if !strings.Contains(q.Get("query"), "FIRST_PDATE:[2020-01-01 TO 2025-12-31]") {
				t.Error(q)
			}
			response = `{"resultList":{"result":[]}}`
		case "/semantic/search":
			if q.Get("year") != "2020:2025" {
				t.Error(q)
			}
			response = `{"data":[]}`
		case "/arxiv":
			if strings.Contains(q.Get("search_query"), "submittedDate") {
				t.Error("publication range changed to submission range")
			}
			response = `<feed xmlns="http://www.w3.org/2005/Atom"></feed>`
		default:
			t.Error(r.URL.Path)
		}
		w.Write([]byte(response))
	})
	defer server.Close()
	connectors := []research.Connector{newPubMed(client, server.URL+"/pubmed", 0), newCrossref(client, server.URL+"/crossref"), newOpenAlex(client, server.URL+"/openalex"), newEuropePMC(client, server.URL+"/europe"), newSemanticScholar(client, server.URL+"/semantic", 0), newArXiv(client, server.URL+"/arxiv", 0)}
	for _, c := range connectors {
		if _, err := c.Search(context.Background(), research.SearchOptions{Query: "fasting", Limit: 20, Years: research.PublicationYears{From: 2020, To: 2025}}); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 6 {
		t.Fatal(seen)
	}
}
