package connectors

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	research "github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/researchsources"
)

type singleAttemptLiveDelegate struct {
	next interface {
		RoundTripSingleAttempt(*http.Request) (*http.Response, error)
	}
}

func (d singleAttemptLiveDelegate) RoundTrip(r *http.Request) (*http.Response, error) {
	return d.next.RoundTripSingleAttempt(r)
}

type profileLiveCapture struct {
	capture *literatureComparisonTransport
}

func (c profileLiveCapture) RoundTrip(r *http.Request) (*http.Response, error) {
	return c.RoundTripSingleAttempt(r)
}
func (c profileLiveCapture) RoundTripSingleAttempt(r *http.Request) (*http.Response, error) {
	scope, _ := r.Context().Value(literatureComparisonScopeKey{}).(literatureComparisonScope)
	for _, source := range researchsources.Definitions() {
		u, _ := url.Parse(source.Endpoint)
		if u.Hostname() == r.URL.Hostname() {
			scope.SourceID = source.ID
			break
		}
	}
	return c.capture.RoundTrip(r.WithContext(context.WithValue(r.Context(), literatureComparisonScopeKey{}, scope)))
}

// Public API integration only. The discipline labels are prespecified fixtures,
// not model predictions; no scientific stage, database or paid model is run.
func TestLiteratureProfileLiveBounded(t *testing.T) {
	if os.Getenv("SCIAIDE_LITERATURE_PROFILE_LIVE") != "1" {
		t.Skip("bounded profile/API integration is opt-in")
	}
	out := os.Getenv("SCIAIDE_LITERATURE_PROFILE_OUT")
	if !filepath.IsAbs(out) {
		t.Fatal("new absolute capture directory required")
	}
	if err := os.Mkdir(out, 0700); err != nil {
		t.Fatal("refuse to overwrite live evidence", err)
	}
	cases := literatureComparisonSpec().Cases
	spec := map[string]any{"version": "profile-api-live-v1", "strategyVersion": research.RetrievalPlanVersion, "cases": cases, "profiles": []string{"education", "biomedical"}, "publicationYears": research.PublicationYears{From: 2022, To: 2026}, "limit": 5, "maxHttp": 28, "maxSeconds": 300, "modelHttp": 0, "disciplineProvidedByFixture": true}
	bytes, _ := json.MarshalIndent(spec, "", "  ")
	bytes = append(bytes, '\n')
	sum := sha256.Sum256(bytes)
	if err := os.WriteFile(filepath.Join(out, "plan.json"), bytes, 0600); err != nil {
		t.Fatal(err)
	}
	client := NewClient()
	single, ok := client.http.Transport.(interface {
		RoundTripSingleAttempt(*http.Request) (*http.Response, error)
	})
	if !ok {
		t.Fatal("production transport lacks a non-replaying mode")
	}
	capture := &literatureComparisonTransport{next: singleAttemptLiveDelegate{next: single}, dir: out, max: 28, bySlot: map[string]int{}}
	client.http.Transport = profileLiveCapture{capture: capture}
	service, err := research.NewService(defaultWithClient(client))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	started := time.Now().UTC()
	type caseResult struct {
		CaseID  string                  `json:"caseId"`
		Plan    research.RetrievalPlan  `json:"allocation"`
		Queries []research.SearchResult `json:"queries"`
	}
	results := []caseResult{}
	for index, question := range cases {
		p, _ := research.BuildRetrievalPlan([]string{"education", "biomedical"}[index], nil, len(question.Queries))
		bounded, b, err := research.WithSourceHTTPBudget(ctx, p.MaxHTTP)
		if err != nil {
			t.Fatal(err)
		}
		result := caseResult{CaseID: question.ID, Queries: []research.SearchResult{}}
		for q, query := range question.Queries {
			callCtx, err := research.WithRetrievalSchedule(bounded, &p, q)
			if err != nil {
				t.Fatal(err)
			}
			scope := literatureComparisonScope{Key: fmt.Sprintf("%s:%d", question.ID, q), CaseID: question.ID, QueryIndex: q, MaxHTTP: 14}
			callCtx = context.WithValue(callCtx, literatureComparisonScopeKey{}, scope)
			found, err := service.Search(callCtx, research.SearchCommand{Query: query, SourceIDs: p.SourcesForQuery(q), Limit: 5, Years: research.PublicationYears{From: 2022, To: 2026}})
			result.Queries = append(result.Queries, found)
			if err != nil {
				t.Errorf("profile search incomplete: %v", err)
				break
			}
		}
		p.UsedHTTP, p.BudgetDenied = b.Usage()
		result.Plan = p
		results = append(results, result)
		t.Logf("case=%s profile=%s actual source HTTP=%d/%d denied=%d", question.ID, p.Profile, p.UsedHTTP, p.MaxHTTP, p.BudgetDenied)
		if ctx.Err() != nil {
			break
		}
	}
	complete := ctx.Err() == nil && len(results) == len(cases)
	used := 0
	for _, result := range results {
		used += result.Plan.UsedHTTP
		if result.Plan.UsedHTTP > 14 || len(result.Queries) != 2 {
			complete = false
		}
	}
	capture.mu.Lock()
	traces := append([]*literatureComparisonTrace(nil), capture.traces...)
	denied := capture.denied
	capture.mu.Unlock()
	if len(traces) != used || used > 28 || denied != 0 {
		complete = false
	}
	for _, trace := range traces {
		if trace.CaptureWriteFailed {
			complete = false
		}
	}
	data, _ := json.MarshalIndent(map[string]any{"version": "profile-api-live-v1", "specSha256": fmt.Sprintf("%x", sum), "startedAt": started, "endedAt": time.Now().UTC(), "complete": complete, "actualHttp": used, "modelHttp": 0, "cases": results, "http": traces}, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "capture.json"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("saved incomplete capture; do not expand bounds automatically")
	}
	t.Logf("sourceHttp=%d modelHttp=0; upstream failures remain observations, not successful empty searches", used)
}
