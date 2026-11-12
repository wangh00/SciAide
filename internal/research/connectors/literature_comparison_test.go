package connectors

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	research "github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/researchsources"
)

// This is a bounded public-source experiment, not another research executor or
// a scientific-relevance gate. Frozen responses are shared by predeclared
// equal-ceiling ablations; cache reuse is never counted as a free API request.
type literatureComparisonCase struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Queries  []string `json:"queries"`
}
type literatureComparisonSlot struct {
	SourceID     string `json:"sourceId"`
	QueryIndex   int    `json:"queryIndex"`
	ReservedHTTP int    `json:"reservedHttp"`
}
type literatureComparisonStrategy struct {
	ID        string                     `json:"id"`
	SourceIDs []string                   `json:"sourceIds"`
	Slots     []literatureComparisonSlot `json:"slots"`
}
type literatureComparisonPlan struct {
	Version      string                         `json:"version"`
	Cases        []literatureComparisonCase     `json:"cases"`
	Years        research.PublicationYears      `json:"publicationYears"`
	Limit        int                            `json:"limitPerSourceQuery"`
	MaxHTTP      int                            `json:"maxHttp"`
	MaxSeconds   int                            `json:"maxSeconds"`
	StrategyHTTP int                            `json:"strategyHttpCeiling"`
	Strategies   []literatureComparisonStrategy `json:"strategies"`
	Pool         []literatureComparisonSlot     `json:"sharedPool"`
	Notes        []string                       `json:"notes"`
}

func literatureComparisonSpec() literatureComparisonPlan {
	sources := researchsources.IDs()
	// Versioned baseline: changing catalog order/count requires an explicit new
	// experiment specification rather than silently expanding network scope.
	expected := []string{"openalex", "crossref", "arxiv", "pubmed", "europepmc", "semantic-scholar", "eric", "core"}
	if strings.Join(sources, ",") != strings.Join(expected, ",") {
		panic("literature comparison v2 catalog changed")
	}
	p := literatureComparisonPlan{Version: "literature-equal-http-v2", Years: research.PublicationYears{From: 2022, To: 2026}, Limit: 5, MaxHTTP: 32, MaxSeconds: 480, StrategyHTTP: 14,
		Cases: []literatureComparisonCase{
			{ID: "education_writing", Question: "AI 辅助反馈对大学生写作是否有帮助（不限写作语言）", Queries: []string{
				`("artificial intelligence" OR ChatGPT OR "automated writing evaluation") AND feedback AND writing`,
				`("automated feedback" OR Grammarly OR "generative AI") AND (writing OR essay) AND (university OR undergraduate OR college)`,
			}},
			{ID: "biomedical_insomnia", Question: "认知行为疗法对成人慢性失眠是否有效", Queries: []string{
				`("cognitive behavioral therapy" OR "cognitive behavioural therapy") AND insomnia AND adult`,
				`"chronic insomnia" AND ("cognitive behavioral" OR "cognitive behavioural") AND (randomized OR trial OR "meta analysis")`,
			}},
		}, Notes: []string{
			"Fixed catalog-order round robin, at most two prespecified queries per source; not a deployed discipline router.",
			"PubMed reserves two HTTP calls (esearch plus batch efetch); others reserve one. Empty results/cooldown may spend less; unused credit is not reallocated.",
			"One transport attempt, no per-record enrichment, first page only. Production retry policy is unchanged.",
			"v2 capture uses standard TLS / HTTP1 fresh connections so transport-internal replay cannot bypass the HTTP ceiling. Legacy v1 records count outer RoundTrip attempts only.",
			"One frozen pool is reused for all three configurations. This is offline ablation of live captures, not three independent timing or cost trials.",
			"arXiv lacks native publication-year filtering; its submitted year is not treated as verified publication year.",
			"PDF URLs are unverified discovery leads. No PDF download, model call, scientific approval or formal task/data access.",
			"No gold relevance set: counts/keyword screening are not recall, independent-study counts or expert eligibility judgments.",
		}}
	seen := map[string]bool{}
	for i, n := range []int{6, 7, 8} {
		strategy := literatureComparisonStrategy{ID: []string{"baseline_six", "plus_eric", "plus_eric_core"}[i], SourceIDs: append([]string(nil), sources[:n]...)}
		used := 0
		for q := 0; q < 2; q++ {
			for _, id := range strategy.SourceIDs {
				cost := 1
				if id == "pubmed" {
					cost = 2
				}
				if used+cost > p.StrategyHTTP {
					continue
				}
				if used == p.StrategyHTTP {
					break
				}
				slot := literatureComparisonSlot{SourceID: id, QueryIndex: q, ReservedHTTP: cost}
				strategy.Slots = append(strategy.Slots, slot)
				used += cost
				key := fmt.Sprintf("%s:%d", id, q)
				if !seen[key] {
					seen[key] = true
					p.Pool = append(p.Pool, slot)
				}
			}
		}
		p.Strategies = append(p.Strategies, strategy)
	}
	// Keep the capture order stable: complete query 1, then needed query-2 slots.
	p.Pool = nil
	for q := 0; q < 2; q++ {
		for _, id := range sources {
			if seen[fmt.Sprintf("%s:%d", id, q)] {
				cost := 1
				if id == "pubmed" {
					cost = 2
				}
				p.Pool = append(p.Pool, literatureComparisonSlot{SourceID: id, QueryIndex: q, ReservedHTTP: cost})
			}
		}
	}
	return p
}

type literatureComparisonScope struct {
	Key, CaseID, SourceID string
	QueryIndex, MaxHTTP   int
}
type literatureComparisonScopeKey struct{}
type literatureComparisonTrace struct {
	Ordinal            int    `json:"ordinal"`
	CaseID             string `json:"caseId"`
	SourceID           string `json:"sourceId"`
	QueryIndex         int    `json:"queryIndex"`
	Endpoint           string `json:"endpoint"`
	Status             int    `json:"status,omitempty"`
	ContentType        string `json:"contentType,omitempty"`
	RetryAfter         string `json:"retryAfter,omitempty"`
	Remaining          string `json:"remaining,omitempty"`
	HeaderMillis       int64  `json:"headerMillis"`
	TransportFailed    bool   `json:"transportFailed,omitempty"`
	BodyFile           string `json:"bodyFile,omitempty"`
	CapturedBytes      int    `json:"capturedBytes"`
	CapturedSHA256     string `json:"capturedSha256,omitempty"`
	CapturedPrefixOnly bool   `json:"capturedPrefixOnly,omitempty"`
	BodyReadFailed     bool   `json:"bodyReadFailed,omitempty"`
	BodyCloseFailed    bool   `json:"bodyCloseFailed,omitempty"`
	CaptureWriteFailed bool   `json:"captureWriteFailed,omitempty"`
}
type literatureComparisonTransport struct {
	next   http.RoundTripper
	dir    string
	max    int
	mu     sync.Mutex
	traces []*literatureComparisonTrace
	bySlot map[string]int
	denied int
}

func (c *literatureComparisonTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	scope, ok := r.Context().Value(literatureComparisonScopeKey{}).(literatureComparisonScope)
	c.mu.Lock()
	if !ok || len(c.traces) >= c.max || c.bySlot[scope.Key] >= scope.MaxHTTP {
		c.denied++
		c.mu.Unlock()
		return nil, errors.New("literature comparison HTTP ceiling reached")
	}
	c.bySlot[scope.Key]++
	endpoint := r.URL.Scheme + "://" + r.URL.Host + r.URL.EscapedPath()
	trace := &literatureComparisonTrace{Ordinal: len(c.traces) + 1, CaseID: scope.CaseID, SourceID: scope.SourceID, QueryIndex: scope.QueryIndex, Endpoint: endpoint}
	c.traces = append(c.traces, trace)
	c.mu.Unlock()
	start := time.Now()
	response, err := c.next.RoundTrip(r)
	c.mu.Lock()
	trace.HeaderMillis = time.Since(start).Milliseconds()
	trace.TransportFailed = err != nil
	if response != nil {
		trace.Status = response.StatusCode
		trace.ContentType = response.Header.Get("Content-Type")
		trace.RetryAfter = response.Header.Get("Retry-After")
		trace.Remaining = response.Header.Get("X-RateLimit-Remaining")
	}
	c.mu.Unlock()
	if response != nil && response.Body != nil {
		response.Body = &literatureComparisonBody{ReadCloser: response.Body, owner: c, trace: trace}
	}
	return response, err
}

type literatureComparisonBody struct {
	io.ReadCloser
	owner      *literatureComparisonTransport
	trace      *literatureComparisonTrace
	data       bytes.Buffer
	prefixOnly bool
	readFailed bool
	once       sync.Once
}

func (b *literatureComparisonBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	remaining := (8 << 20) - b.data.Len()
	keep := min(n, max(0, remaining))
	b.data.Write(p[:keep])
	b.prefixOnly = b.prefixOnly || keep < n
	b.readFailed = b.readFailed || (err != nil && !errors.Is(err, io.EOF))
	return n, err
}
func (b *literatureComparisonBody) Close() error {
	closeErr := b.ReadCloser.Close()
	b.once.Do(func() {
		name := fmt.Sprintf("http-%03d.raw", b.trace.Ordinal)
		sum := sha256.Sum256(b.data.Bytes())
		writeErr := os.WriteFile(filepath.Join(b.owner.dir, name), b.data.Bytes(), 0600)
		b.owner.mu.Lock()
		defer b.owner.mu.Unlock()
		b.trace.BodyFile = name
		b.trace.CapturedBytes = b.data.Len()
		b.trace.CapturedSHA256 = fmt.Sprintf("%x", sum)
		b.trace.CapturedPrefixOnly = b.prefixOnly
		b.trace.BodyReadFailed = b.readFailed
		b.trace.BodyCloseFailed = closeErr != nil
		b.trace.CaptureWriteFailed = writeErr != nil
	})
	return closeErr
}

type literatureComparisonRow struct {
	CaseID        string                   `json:"caseId"`
	Slot          literatureComparisonSlot `json:"slot"`
	ActualHTTP    int                      `json:"actualHttp"`
	ElapsedMillis int64                    `json:"elapsedMillis"`
	Search        research.SearchResult    `json:"search"`
	Aborted       bool                     `json:"aborted,omitempty"`
}
type literatureComparisonResult struct {
	SpecSHA256   string                       `json:"specSha256"`
	StartedAt    time.Time                    `json:"startedAt"`
	EndedAt      time.Time                    `json:"endedAt"`
	Complete     bool                         `json:"complete"`
	ModelHTTP    int                          `json:"modelHttp"`
	ActualHTTP   int                          `json:"actualHttp"`
	BudgetDenied int                          `json:"budgetDenied"`
	Rows         []literatureComparisonRow    `json:"rows"`
	HTTP         []*literatureComparisonTrace `json:"http"`
}

func TestLiteratureEqualBudgetLive(t *testing.T) {
	if os.Getenv("SCIAIDE_LITERATURE_COMPARISON_LIVE") != "1" {
		t.Skip("bounded public-source comparison is opt-in")
	}
	out := os.Getenv("SCIAIDE_LITERATURE_COMPARISON_OUT")
	if out == "" || !filepath.IsAbs(out) {
		t.Fatal("comparison requires a new absolute output directory")
	}
	if err := os.Mkdir(out, 0700); err != nil {
		t.Fatal("refuse to overwrite a comparison directory", err)
	}
	spec := literatureComparisonSpec()
	encoded, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	sum := sha256.Sum256(encoded)
	if err := os.WriteFile(filepath.Join(out, "plan.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	client := NewClient()
	client.attempts = 1
	client.timeout = 12 * time.Second
	single, ok := client.http.Transport.(interface {
		RoundTripSingleAttempt(*http.Request) (*http.Response, error)
	})
	if !ok {
		t.Fatal("comparison requires an explicit non-replaying source transport")
	}
	capture := &literatureComparisonTransport{next: singleAttemptLiveDelegate{next: single}, dir: out, max: spec.MaxHTTP, bySlot: map[string]int{}}
	client.http.Transport = capture
	service, err := research.NewService(defaultWithClient(client))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(spec.MaxSeconds)*time.Second)
	defer cancel()
	result := literatureComparisonResult{SpecSHA256: fmt.Sprintf("%x", sum), StartedAt: time.Now().UTC(), Rows: []literatureComparisonRow{}}
	t.Logf("frozen comparison=%s maxHttp=%d maxSeconds=%d modelHttp=0", result.SpecSHA256, spec.MaxHTTP, spec.MaxSeconds)
	for _, question := range spec.Cases {
		for _, slot := range spec.Pool {
			if ctx.Err() != nil {
				break
			}
			key := fmt.Sprintf("%s:%s:%d", question.ID, slot.SourceID, slot.QueryIndex)
			callCtx := context.WithValue(ctx, literatureComparisonScopeKey{}, literatureComparisonScope{Key: key, CaseID: question.ID, SourceID: slot.SourceID, QueryIndex: slot.QueryIndex, MaxHTTP: slot.ReservedHTTP})
			start := time.Now()
			found, searchErr := service.Search(callCtx, research.SearchCommand{Query: question.Queries[slot.QueryIndex], SourceIDs: []string{slot.SourceID}, Limit: spec.Limit, Years: spec.Years, EnrichMetadata: false})
			capture.mu.Lock()
			calls := capture.bySlot[key]
			capture.mu.Unlock()
			result.Rows = append(result.Rows, literatureComparisonRow{CaseID: question.ID, Slot: slot, ActualHTTP: calls, ElapsedMillis: time.Since(start).Milliseconds(), Search: found, Aborted: searchErr != nil})
			status := "aborted"
			if len(found.Sources) == 1 {
				status = string(found.Sources[0].Status) + ":" + string(found.Sources[0].ErrorCode)
			}
			t.Logf("case=%s source=%s query=%d http=%d status=%s records=%d", question.ID, slot.SourceID, slot.QueryIndex+1, calls, status, len(found.Works))
		}
	}
	result.EndedAt = time.Now().UTC()
	result.Complete = ctx.Err() == nil && len(result.Rows) == len(spec.Cases)*len(spec.Pool)
	capture.mu.Lock()
	result.ActualHTTP = len(capture.traces)
	result.BudgetDenied = capture.denied
	result.HTTP = append([]*literatureComparisonTrace(nil), capture.traces...)
	capture.mu.Unlock()
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "capture.json"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.BudgetDenied != 0 {
		t.Error("comparison stopped at a declared bound; saved partial evidence; do not expand automatically")
	}
	for _, trace := range result.HTTP {
		if trace.CaptureWriteFailed {
			t.Error("HTTP capture incomplete", trace.Ordinal)
		}
	}
	t.Logf("complete=%v sourceHttp=%d modelHttp=0; source failures are observations, not empty successes", result.Complete, result.ActualHTTP)
}

func TestLiteratureComparisonPlanHasEqualPhysicalHTTPCeilings(t *testing.T) {
	p := literatureComparisonSpec()
	poolCost := 0
	if len(p.Pool) != 14 || len(p.Strategies) != 3 || len(p.Cases) != 2 {
		t.Fatal("comparison changed scope")
	}
	for _, slot := range p.Pool {
		poolCost += slot.ReservedHTTP
	}
	if poolCost*len(p.Cases) != p.MaxHTTP {
		t.Fatal("pool would exceed network budget", poolCost)
	}
	for _, strategy := range p.Strategies {
		cost := 0
		seen := map[string]bool{}
		for _, slot := range strategy.Slots {
			cost += slot.ReservedHTTP
			key := fmt.Sprintf("%s:%d", slot.SourceID, slot.QueryIndex)
			if seen[key] {
				t.Fatal("duplicate paid slot", key)
			}
			seen[key] = true
		}
		if cost != p.StrategyHTTP || len(strategy.Slots) != 12 {
			t.Fatal(strategy.ID, cost, len(strategy.Slots))
		}
	}
	for _, question := range p.Cases {
		for _, q := range question.Queries {
			if _, err := research.ArXivQuery(q); err != nil {
				t.Fatal(err)
			}
			if projection, err := research.RankedBibliographicQuery(q); err != nil || projection.Query == "" {
				t.Fatal("invalid or empty ranked projection", err)
			}
		}
	}
}

type literatureComparisonTestTransport struct{ calls int }

func (t *literatureComparisonTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.calls++
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"public":"fixture"}`)), Request: r}, nil
}
func TestLiteratureComparisonTransportBoundsCallsWithoutSavingCredentials(t *testing.T) {
	next := &literatureComparisonTestTransport{}
	capture := &literatureComparisonTransport{next: next, dir: t.TempDir(), max: 2, bySlot: map[string]int{}}
	scope := literatureComparisonScope{Key: "one", CaseID: "fixture", SourceID: "eric", MaxHTTP: 1}
	ctx := context.WithValue(context.Background(), literatureComparisonScopeKey{}, scope)
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ies.ed.gov/eric/?search=public", nil)
	request.Header.Set("Authorization", "never-save-this-test-header")
	response, err := capture.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := capture.RoundTrip(request); err == nil || next.calls != 1 || capture.denied != 1 {
		t.Fatal("per-slot budget escaped")
	}
	scope.Key = "two"
	request = request.WithContext(context.WithValue(context.Background(), literatureComparisonScopeKey{}, scope))
	response, err = capture.RoundTrip(request)
	if err != nil {
		t.Fatal("second slot should still have global credit", err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	scope.Key = "three"
	request = request.WithContext(context.WithValue(context.Background(), literatureComparisonScopeKey{}, scope))
	if _, err := capture.RoundTrip(request); err == nil || next.calls != 2 || capture.denied != 2 {
		t.Fatal("global budget escaped")
	}
	trace, _ := json.Marshal(capture.traces)
	if bytes.Contains(trace, []byte("never-save-this-test-header")) || capture.traces[0].CapturedBytes != len(body) || capture.traces[0].CapturedSHA256 != fmt.Sprintf("%x", sha256.Sum256(body)) {
		t.Fatal("capture leaked credentials or lost bytes")
	}
}

type literatureComparisonFailingBody struct {
	io.Reader
}

func (b literatureComparisonFailingBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}
func (b literatureComparisonFailingBody) Close() error { return io.ErrClosedPipe }

func TestLiteratureComparisonCapturePreservesBodyFailures(t *testing.T) {
	capture := &literatureComparisonTransport{dir: t.TempDir()}
	trace := &literatureComparisonTrace{Ordinal: 1}
	body := &literatureComparisonBody{ReadCloser: literatureComparisonFailingBody{Reader: strings.NewReader("prefix")}, owner: capture, trace: trace}
	data, err := io.ReadAll(body)
	if !errors.Is(err, io.ErrUnexpectedEOF) || string(data) != "prefix" {
		t.Fatal("capture changed the application's body-read outcome", string(data), err)
	}
	if err := body.Close(); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("capture changed the application's close outcome", err)
	}
	saved, err := os.ReadFile(filepath.Join(capture.dir, trace.BodyFile))
	if err != nil || !bytes.Equal(saved, data) || !trace.BodyReadFailed || !trace.BodyCloseFailed || trace.CaptureWriteFailed {
		t.Fatal("failed response was not preserved accurately", trace, err)
	}
}
