package bootstrap

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/research/connectors"
)

func TestEightSourceContractPreservesPartialEvidenceAndHistory(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Header.Get("X-Fixture-Source")
		key := host
		if host == "eutils.ncbi.nlm.nih.gov" {
			key += r.URL.Path
		}
		mu.Lock()
		counts[key]++
		n := counts[key]
		mu.Unlock()
		var body string
		switch host {
		case "api.crossref.org":
			body = `{"status":"ok","message":{"items":[{"DOI":"10.5555/crossref","title":["Original title"]}]}}`
			if n == 1 {
				body = strings.Replace(body, `"ok"`, `"error"`, 1)
			}
		case "api.openalex.org":
			body = `{"results":[{"id":"https://openalex.org/W123","title":"Original title"}]}`
			if n == 1 {
				body = strings.Replace(body, `{`, `{"error":"declared failure",`, 1)
			}
		case "api.semanticscholar.org":
			body = `{"data":[{"paperId":"KeepPaper","title":"Original title"}]}`
			if n == 1 {
				body = strings.Replace(body, "KeepPaper", "bad?paper", 1)
			}
		case "www.ebi.ac.uk":
			body = `{"resultList":{"result":[{"id":"999","source":"MED","title":"Original title"}]}}`
			if n == 1 {
				body = strings.Replace(body, "999", strings.Repeat("x", research.MaxRecordIDRunes+1), 1)
			}
		case "api.ies.ed.gov":
			body = `{"response":{"numFound":1,"docs":[{"id":"EJ1234567","title":"Retained ERIC evidence","description":"Original abstract.","publicationdateyear":2025}]}}`
		case "api.core.ac.uk":
			body = `{"totalHits":0,"results":[]}`
		case "export.arxiv.org":
			body = `<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>https://arxiv.org/abs/2401.01234v3</id><title>Original title</title></entry></feed>`
			if n == 1 {
				body = `<feed><entry><title>Original title</title></entry></feed>`
			}
		case "eutils.ncbi.nlm.nih.gov":
			if strings.Contains(r.URL.Path, "esearch") {
				body = `{"esearchresult":{"idlist":["1","2"]}}`
				break
			}
			body = `<PubmedArticleSet><PubmedArticle><MedlineCitation><PMID>1</PMID><Article><ArticleTitle>Original title</ArticleTitle></Article></MedlineCitation></PubmedArticle>`
			if n != 1 {
				body += `<PubmedArticle><MedlineCitation><PMID>2</PMID><Article><ArticleTitle>Original title</ArticleTitle></Article></MedlineCitation></PubmedArticle>`
			}
			body += `</PubmedArticleSet>`
		default:
			t.Error("unregistered source host", host)
			http.Error(w, "unexpected host", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	transport := &routingFixtureTransport{endpoint: endpoint}
	options := Options{RootDir: t.TempDir(), ResearchConnectors: connectors.DefaultWithTransport(transport)}
	app, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if app != nil {
			app.Close()
		}
	}()
	project, err := app.ProjectFacade.CreateProject(struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		WorkspacePath string `json:"workspacePath"`
	}{Name: "Unified response admission"})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"crossref", "semantic-scholar", "openalex", "europepmc", "eric", "core", "pubmed", "arxiv"}
	command := research.DiscoverySearchCommand{ProjectID: project.ID, Query: "writing feedback", SourceIDs: ids, Limit: 5}
	type snapshot struct {
		sources string
		partial bool
		items   [32]byte
		total   int
	}
	frozen := map[string]snapshot{}
	for attempt := 0; attempt < 3; attempt++ {
		result, err := app.ResearchFacade.Search(command)
		if err != nil || len(result.Query.Sources) != 8 {
			t.Fatal(result, err)
		}
		wantPartial, wantTotal, wantHTTP := attempt == 0, 8, int32(15)
		if attempt == 0 {
			wantTotal, wantHTTP = 1, 9
		}
		if result.Query.Partial != wantPartial || result.Page.Total != wantTotal || transport.calls.Load() != wantHTTP {
			t.Fatal("mixed source failure/recovery/cache changed", result.Query.Sources, result.Page.Total, transport.calls.Load())
		}
		for _, source := range result.Query.Sources {
			if source.SourceID == "core" && source.Status != research.SearchEmpty {
				t.Fatal("legitimate empty was rejected", source)
			}
			if source.SourceID == "eric" && source.Status != research.SearchOK {
				t.Fatal("unaffected evidence lost", source)
			}
			if wantPartial && source.SourceID != "eric" && source.SourceID != "core" && (source.ErrorCode != research.FailureInvalidData || source.Retryable) {
				t.Fatal("failure category/retry changed", source)
			}
		}
		encoded, err := json.Marshal(result.Query.Sources)
		if err != nil {
			t.Fatal(err)
		}
		items, err := json.Marshal(result.Page.Items)
		if err != nil {
			t.Fatal(err)
		}
		if _, duplicate := frozen[result.Query.ID]; duplicate {
			t.Fatal("explicit retries overwrote history")
		}
		frozen[result.Query.ID] = snapshot{string(encoded), result.Query.Partial, sha256.Sum256(items), result.Page.Total}
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app = nil
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	queries, err := app.ResearchFacade.ListQueries(project.ID)
	if err != nil || len(queries) != 3 {
		t.Fatal(queries, err)
	}
	for _, query := range queries {
		prior, exists := frozen[query.ID]
		encoded, err := json.Marshal(query.Sources)
		if err != nil || !exists || string(encoded) != prior.sources || query.Partial != prior.partial {
			t.Fatal("source history changed on reopen", query.ID, err)
		}
		page, err := app.ResearchFacade.ListCandidates(research.CandidateListCommand{ProjectID: project.ID, QueryID: query.ID, Limit: 20, Sort: "relevance"})
		if err != nil {
			t.Fatal(err)
		}
		items, err := json.Marshal(page.Items)
		if err != nil || page.Total != prior.total || sha256.Sum256(items) != prior.items {
			t.Fatal("frozen evidence was overwritten by later recovery", query.ID, err)
		}
	}
	if transport.calls.Load() != 15 {
		t.Fatal("reopening resumed source requests", transport.calls.Load())
	}
	t.Log("8 actual adapters: 9 initial local HTTP + 6 explicit recovery HTTP; third call cached; retained evidence/empty/failures and 3 frozen candidate/source snapshots unchanged on same-process SQLite reopen; 0 public/model/PDF calls")
}
