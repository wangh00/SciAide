package connectors

import (
	"context"
	"encoding/xml"
	"net/http"
	"testing"

	"github.com/wangh00/SciAide/internal/app/research"
)

func TestPubMedInlineMarkupPreservesScientificText(t *testing.T) {
	var article pubMedArticle
	if err := xml.Unmarshal([]byte(`<PubmedArticle><MedlineCitation><PMID>1</PMID><Article><ArticleTitle>Effect on <i>body weight</i></ArticleTitle><Abstract><AbstractText Label="RESULTS">Mean <b>weight</b> change &lt; 5 kg.</AbstractText></Abstract></Article></MedlineCitation></PubmedArticle>`), &article); err != nil {
		t.Fatal(err)
	}
	work := pubMedArticleWork(article)
	if work.Title != "Effect on body weight" || work.Abstract != "RESULTS: Mean weight change < 5 kg." {
		t.Fatal(work)
	}
}

func TestEuropePMCFollowsReturnedCursor(t *testing.T) {
	calls := 0
	server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Query().Get("cursorMark") {
		case "*":
			_, _ = w.Write([]byte(`{"nextCursorMark":"next-token","resultList":{"result":[{"id":"1","source":"MED","title":"First"}]}}`))
		case "next-token":
			_, _ = w.Write([]byte(`{"resultList":{"result":[{"id":"2","source":"MED","title":"Second"}]}}`))
		default:
			t.Error("unexpected cursor", r.URL.RawQuery)
		}
	})
	defer server.Close()
	works, err := newEuropePMC(client, server.URL).Search(context.Background(), research.SearchOptions{Query: "topic", Limit: 20, Offset: 20})
	if err != nil || calls != 2 || len(works) != 1 || works[0].Title != "Second" {
		t.Fatal(works, calls, err)
	}
}

func TestLiteratureConnectorPageParameters(t *testing.T) {
	server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		key, want := "offset", "20"
		switch r.URL.Path {
		case "/openalex":
			key, want = "page", "2"
		case "/europe/search":
			key, want = "cursorMark", "*"
		case "/pubmed/esearch.fcgi":
			key = "retstart"
		case "/arxiv":
			key = "start"
		}
		if r.URL.Query().Get(key) != want {
			t.Errorf("%s missing page: %s", r.URL.Path, r.URL.RawQuery)
		}
		body := map[string]string{"/openalex": `{"results":[]}`, "/crossref": `{"message":{"items":[]}}`, "/europe/search": `{"resultList":{"result":[]}}`, "/semantic/search": `{"data":[]}`, "/pubmed/esearch.fcgi": `{"esearchresult":{"idlist":[]}}`, "/arxiv": `<feed xmlns="http://www.w3.org/2005/Atom"></feed>`}[r.URL.Path]
		if body == "" {
			t.Error(r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	})
	defer server.Close()
	all := []research.Connector{newOpenAlex(client, server.URL+"/openalex"), newCrossref(client, server.URL+"/crossref"), newEuropePMC(client, server.URL+"/europe"), newSemanticScholar(client, server.URL+"/semantic", 0), newPubMed(client, server.URL+"/pubmed", 0), newArXiv(client, server.URL+"/arxiv", 0)}
	for _, c := range all {
		if _, err := c.Search(context.Background(), research.SearchOptions{Query: "topic", Limit: 20, Offset: 20}); err != nil {
			t.Fatal(c.Source().ID, err)
		}
	}
}
func TestPubMedSearchRejectsMissingPMID(t *testing.T) {
	server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/esearch.fcgi":
			_, _ = w.Write([]byte(`{"esearchresult":{"idlist":["1","2"]}}`))
		case "/efetch.fcgi":
			if r.URL.Query().Get("id") != "1,2" {
				t.Error("not batch fetched")
			}
			_, _ = w.Write([]byte(`<PubmedArticleSet><PubmedArticle><MedlineCitation><PMID>1</PMID><Article><ArticleTitle>Study</ArticleTitle><Abstract><AbstractText>Complete source abstract</AbstractText></Abstract></Article></MedlineCitation></PubmedArticle></PubmedArticleSet>`))
		default:
			t.Error("unexpected summary fallback")
		}
	})
	defer server.Close()
	if _, err := newPubMed(client, server.URL, 0).Search(context.Background(), research.SearchOptions{Query: "topic", Limit: 20}); err == nil {
		t.Fatal("missing PMID treated as complete")
	}
}
