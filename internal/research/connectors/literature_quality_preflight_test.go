package connectors

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/research"
)

type qualityRequest struct {
	Method       string     `json:"method"`
	Endpoint     string     `json:"endpoint"`
	EncodedQuery string     `json:"encodedQuery"`
	Parameters   url.Values `json:"parameters"`
}

type qualityPrepared struct {
	Request qualityRequest        `json:"request"`
	Source  research.SourceSearch `json:"source"`
}

type qualityDispatchTrace struct {
	literatureComparisonTrace
	Request qualityRequest `json:"request"`
}

type qualitySlot struct {
	Case     qualityCase
	Source   string
	ArmIndex int
}

func qualitySchedule(p qualityPlan) []qualitySlot {
	result := []qualitySlot{}
	for ci, c := range p.Cases {
		for si, source := range p.Sources {
			for oi := range 2 {
				result = append(result, qualitySlot{Case: c, Source: source, ArmIndex: (oi + ci + si) % 2})
			}
		}
	}
	return result
}

var qualityEndpoints = map[string]string{"eric": "https://api.ies.ed.gov/eric/", "openalex": "https://api.openalex.org/works", "crossref": "https://api.crossref.org/works"}

func qualitySHA(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

func snapshotQualityRequest(req *http.Request, source string) (qualityRequest, error) {
	allowed := map[string]string{"eric": "search,format,start,rows,fields", "openalex": "search,per-page,sort,filter", "crossref": "query.bibliographic,rows,sort,filter"}[source]
	if req == nil || req.URL == nil || allowed == "" || req.Method != http.MethodGet || req.Body != nil && req.Body != http.NoBody || req.ContentLength > 0 || req.URL.User != nil || req.URL.Fragment != "" || req.URL.Opaque != "" || req.Host != "" && req.Host != req.URL.Host {
		return qualityRequest{}, fmt.Errorf("quality request envelope is outside fixed GET scope")
	}
	endpoint := req.URL.Scheme + "://" + req.URL.Host + req.URL.EscapedPath()
	if endpoint != qualityEndpoints[source] || len(req.URL.RawQuery) > 32<<10 {
		return qualityRequest{}, fmt.Errorf("quality request endpoint/size is outside frozen scope")
	}
	params, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil || len(params) != len(strings.Split(allowed, ",")) {
		return qualityRequest{}, fmt.Errorf("quality request parameters differ from fixed first-page shape")
	}
	for _, name := range strings.Split(allowed, ",") {
		if len(params[name]) != 1 || params[name][0] == "" {
			return qualityRequest{}, fmt.Errorf("quality request parameter missing/duplicated")
		}
	}
	// Headers, credentials and arbitrary URLs are deliberately never serialized.
	return qualityRequest{Method: req.Method, Endpoint: endpoint, EncodedQuery: req.URL.RawQuery, Parameters: params}, nil
}

func qualityPreflight(p qualityPlan) (map[string]qualityPrepared, error) {
	if len(p.PopulationSuffixes) != 3 {
		return nil, fmt.Errorf("missing reviewed population suffix controls")
	}
	prepared := map[string]qualityPrepared{}
	for _, c := range p.Cases {
		for _, source := range p.Sources {
			for ai, arm := range p.Arms {
				body := map[string]string{"eric": `{"response":{"numFound":0,"docs":[]}}`, "openalex": `{"results":[]}`, "crossref": `{"status":"ok","message":{"items":[]}}`}[source]
				fixture := &qualityReplayTransport{endpoint: qualityEndpoints[source], sourceID: source, body: []byte(body)}
				client := NewClient()
				client.attempts, client.http.Transport = 1, fixture
				service, err := research.NewService(defaultWithClient(client))
				if err != nil {
					return nil, err
				}
				projections := map[string]string{}
				if ai == 1 {
					projections[source] = c.Focused[source]
				}
				found, err := service.Search(context.Background(), research.SearchCommand{Query: c.LogicalQuery, ProviderQueries: projections, SourceIDs: []string{source}, Limit: p.Limit, Years: p.Years})
				if err != nil || fixture.calls != 1 || fixture.request == nil || len(found.Sources) != 1 || found.Sources[0].Status != research.SearchEmpty {
					return nil, fmt.Errorf("preflight failed before network: %s/%s/%s: %v", c.ID, source, arm, err)
				}
				status := found.Sources[0]
				if status.PublicationYears != p.Years || !status.YearFilterApplied {
					return nil, fmt.Errorf("preflight year audit changed")
				}
				prepared[c.ID+":"+arm+":"+source] = qualityPrepared{Request: *fixture.request, Source: status}
			}
			a, b := prepared[c.ID+":"+p.Arms[0]+":"+source], prepared[c.ID+":"+p.Arms[1]+":"+source]
			suffix := p.PopulationSuffixes[source]
			if suffix == "" || b.Source.EffectiveQuery != a.Source.EffectiveQuery+suffix {
				return nil, fmt.Errorf("post-projection control changed more than population suffix: %s/%s", c.ID, source)
			}
			queryKey := "search"
			if source == "crossref" {
				queryKey = "query.bibliographic"
			}
			for _, slot := range []qualityPrepared{a, b} {
				want := url.Values{}
				switch source {
				case "eric":
					want = url.Values{"search": {slot.Source.ProviderQuery}, "rows": {fmt.Sprint(p.Limit)}, "start": {"0"}, "format": {"json"}, "fields": {ericFields}}
				case "openalex":
					want = url.Values{"search": {slot.Source.ProviderQuery}, "per-page": {fmt.Sprint(p.Limit)}, "sort": {"relevance_score:desc"}, "filter": {fmt.Sprintf("from_publication_date:%d-01-01,to_publication_date:%d-12-31", p.Years.From, p.Years.To)}}
				case "crossref":
					want = url.Values{"query.bibliographic": {slot.Source.ProviderQuery}, "rows": {fmt.Sprint(p.Limit)}, "sort": {"relevance"}, "filter": {fmt.Sprintf("from-pub-date:%d-01-01,until-pub-date:%d-12-31", p.Years.From, p.Years.To)}}
				}
				if !reflect.DeepEqual(slot.Request.Parameters, want) || slot.Source.ProviderQuery != research.ProviderQueryWithPublicationYears(source, slot.Source.EffectiveQuery, p.Years) {
					return nil, fmt.Errorf("adapter request and source/year audit disagree: %s/%s", c.ID, source)
				}
			}
			ap, bp := a.Request.Parameters, b.Request.Parameters
			for key, value := range ap {
				if key != queryKey && !reflect.DeepEqual(value, bp[key]) {
					return nil, fmt.Errorf("non-query request parameter changed: %s/%s", c.ID, source)
				}
			}
		}
	}
	return prepared, nil
}

type qualityDispatchGuard struct {
	next     http.RoundTripper
	expected map[string]qualityPrepared
	mu       sync.Mutex
	observed map[string]qualityRequest
	rejected int
}

func (g *qualityDispatchGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	scope, ok := req.Context().Value(literatureComparisonScopeKey{}).(literatureComparisonScope)
	snapshot, err := snapshotQualityRequest(req, scope.SourceID)
	g.mu.Lock()
	want, known := g.expected[scope.Key]
	_, duplicate := g.observed[scope.Key]
	arm, validIndex := map[int]string{0: "baseline", 1: "population_focus"}[scope.QueryIndex]
	valid := ok && known && validIndex && scope.MaxHTTP == 1 && scope.Key == scope.CaseID+":"+arm+":"+scope.SourceID && !duplicate && err == nil && reflect.DeepEqual(snapshot, want.Request)
	if valid {
		g.observed[scope.Key] = snapshot
	} else {
		g.rejected++
	}
	g.mu.Unlock()
	if !valid {
		return nil, fmt.Errorf("quality dispatch refused before network: request differs from preflight or slot already dispatched")
	}
	return g.next.RoundTrip(req)
}

func writeQualityJSONNew(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(b, '\n'))
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func TestLiteratureQualityPreflightControls(t *testing.T) {
	p, _ := readQualityPlanVersion(t, "literature-quality-pilot-v2")
	prepared, err := qualityPreflight(p)
	if err != nil || len(prepared) != 12 {
		t.Fatal(err)
	}
	legacy, _ := readQualityPlan(t)
	legacy.PopulationSuffixes = p.PopulationSuffixes // inspect old expressions, never rewrite old protocol
	if _, err := qualityPreflight(legacy); err == nil || !strings.Contains(err.Error(), "ai_feedback_writing/crossref") {
		t.Fatal("legacy word-multiplicity confound was not caught before network", err)
	}
	if out := os.Getenv("SCIAIDE_LITERATURE_QUALITY_PREFLIGHT_OUT"); out != "" {
		if !filepath.IsAbs(out) {
			t.Fatal("preflight export requires a new absolute file")
		}
		if err := writeQualityJSONNew(out, prepared); err != nil {
			t.Fatal(err)
		}
	}
	t.Log("v2: all 6 projected pairs have exact population suffix and unchanged non-query parameters; v1 confound rejected; networkHttp=0 modelHttp=0")
}

func TestLiteratureQualityExpandedPreflight(t *testing.T) {
	p, _ := readQualityPlanVersion(t, "literature-quality-pilot-v3")
	prepared, err := qualityPreflight(p)
	if err != nil || len(prepared) != 24 || len(qualitySchedule(p)) != 24 {
		t.Fatal("four-topic preflight failed before network", err)
	}
	t.Log("v3: 4 topics, 12 controlled pairs, maxHttp=24 maxSeconds=480; networkHttp=0 modelHttp=0")
}

func TestLiteratureQualityDispatchGuardRejectsBeforeDelegate(t *testing.T) {
	p, _ := readQualityPlanVersion(t, "literature-quality-pilot-v2")
	prepared, err := qualityPreflight(p)
	if err != nil {
		t.Fatal(err)
	}
	key := "ai_feedback_writing:baseline:openalex"
	for name, mutate := range map[string]func(*http.Request){
		"year": func(r *http.Request) {
			q := r.URL.Query()
			q.Set("filter", "from_publication_date:2000-01-01")
			r.URL.RawQuery = q.Encode()
		},
		"query": func(r *http.Request) {
			q := r.URL.Query()
			q.Set("search", "different query")
			r.URL.RawQuery = q.Encode()
		},
		"limit": func(r *http.Request) { q := r.URL.Query(); q.Set("per-page", "50"); r.URL.RawQuery = q.Encode() },
		"unknown": func(r *http.Request) {
			q := r.URL.Query()
			q.Set("api_key", "fixture-not-a-secret")
			r.URL.RawQuery = q.Encode()
		},
		"duplicate":   func(r *http.Request) { q := r.URL.Query(); q.Add("search", "another"); r.URL.RawQuery = q.Encode() },
		"method":      func(r *http.Request) { r.Method = http.MethodPost },
		"host":        func(r *http.Request) { r.URL.Host = "example.invalid" },
		"host-header": func(r *http.Request) { r.Host = "example.invalid" },
		"userinfo":    func(r *http.Request) { r.URL.User = url.User("fixture-user") },
		"body":        func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("fixture")) },
	} {
		t.Run(name, func(t *testing.T) {
			next := &qualityReplayTransport{endpoint: qualityEndpoints["openalex"]}
			g := &qualityDispatchGuard{next: next, expected: prepared, observed: map[string]qualityRequest{}}
			want := prepared[key].Request
			req, _ := http.NewRequest(http.MethodGet, want.Endpoint+"?"+want.EncodedQuery, nil)
			mutate(req)
			req = req.WithContext(context.WithValue(req.Context(), literatureComparisonScopeKey{}, literatureComparisonScope{Key: key, CaseID: p.Cases[0].ID, SourceID: "openalex", QueryIndex: 0, MaxHTTP: 1}))
			if _, err := g.RoundTrip(req); err == nil || next.calls != 0 || g.rejected != 1 || len(g.observed) != 0 {
				t.Fatal("invalid request reached delegate or was recorded as dispatched", err, next.calls)
			}
		})
	}
}

func TestLiteratureQualityDispatchGuardOneAttempt(t *testing.T) {
	p, _ := readQualityPlanVersion(t, "literature-quality-pilot-v2")
	prepared, err := qualityPreflight(p)
	if err != nil {
		t.Fatal(err)
	}
	key := "ai_feedback_writing:baseline:openalex"
	want := prepared[key].Request
	next := &qualityReplayTransport{endpoint: want.Endpoint, body: []byte(`{}`)}
	g := &qualityDispatchGuard{next: next, expected: prepared, observed: map[string]qualityRequest{}}
	req, _ := http.NewRequest(http.MethodGet, want.Endpoint+"?"+want.EncodedQuery, nil)
	req = req.WithContext(context.WithValue(req.Context(), literatureComparisonScopeKey{}, literatureComparisonScope{Key: key, CaseID: "ai_feedback_writing", SourceID: "openalex", QueryIndex: 0, MaxHTTP: 1}))
	resp, err := g.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, err := g.RoundTrip(req); err == nil || next.calls != 1 || g.rejected != 1 {
		t.Fatal("duplicate request reached delegate", err, next.calls)
	}
	// A forged scope cannot attach an otherwise valid request to another arm.
	g = &qualityDispatchGuard{next: next, expected: prepared, observed: map[string]qualityRequest{}}
	req = req.WithContext(context.WithValue(req.Context(), literatureComparisonScopeKey{}, literatureComparisonScope{Key: key, CaseID: "ai_feedback_writing", SourceID: "openalex", QueryIndex: 1, MaxHTTP: 1}))
	if _, err := g.RoundTrip(req); err == nil || next.calls != 1 || g.rejected != 1 {
		t.Fatal("forged scope reached delegate", err)
	}
}

type qualityDelegateFunc func(*http.Request) (*http.Response, error)

func (f qualityDelegateFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLiteratureQualityDispatchGuardConcurrentSlot(t *testing.T) {
	p, _ := readQualityPlanVersion(t, "literature-quality-pilot-v2")
	prepared, err := qualityPreflight(p)
	if err != nil {
		t.Fatal(err)
	}
	key := "ai_feedback_writing:baseline:openalex"
	want := prepared[key].Request
	var calls atomic.Int32
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	next := qualityDelegateFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	})
	capture := &literatureComparisonTransport{next: next, dir: t.TempDir(), max: 12, bySlot: map[string]int{}}
	g := &qualityDispatchGuard{next: capture, expected: prepared, observed: map[string]qualityRequest{}}
	start, done := make(chan struct{}), make(chan error, 2)
	for range 2 {
		go func() {
			req, _ := http.NewRequest(http.MethodGet, want.Endpoint+"?"+want.EncodedQuery, nil)
			req = req.WithContext(context.WithValue(req.Context(), literatureComparisonScopeKey{}, literatureComparisonScope{Key: key, CaseID: "ai_feedback_writing", SourceID: "openalex", QueryIndex: 0, MaxHTTP: 1}))
			<-start
			resp, err := g.RoundTrip(req)
			if resp != nil {
				resp.Body.Close()
			}
			done <- err
		}()
	}
	close(start)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("no delegate entered")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("duplicate slot was not rejected")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("duplicate slot was not rejected before delegate completed")
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("accepted delegate did not finish")
	}
	if calls.Load() != 1 || len(capture.traces) != 1 || capture.bySlot[key] != 1 || capture.denied != 0 || g.rejected != 1 {
		t.Fatal("duplicate slot consumed actual transport/capture budget", calls.Load(), len(capture.traces), g.rejected)
	}
}

func TestLiteratureQualityDispatchTLSFixture(t *testing.T) {
	version := os.Getenv("SCIAIDE_LITERATURE_QUALITY_FIXTURE_PROTOCOL")
	if version == "" {
		version = "literature-quality-pilot-v2"
	}
	if version != "literature-quality-pilot-v2" && version != "literature-quality-pilot-v3" {
		t.Fatal("unsupported fixture protocol")
	}
	p, encoded := readQualityPlanVersion(t, version)
	prepared, err := qualityPreflight(p)
	if err != nil {
		t.Fatal(err)
	}
	wire := make(chan qualityRequest, 12)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Scheme, r.URL.Host = "https", r.Host
		var source string
		for id, endpoint := range qualityEndpoints {
			if "https://"+r.Host+r.URL.Path == endpoint {
				source = id
			}
		}
		observed, err := snapshotQualityRequest(r, source)
		if err != nil {
			http.Error(w, "invalid fixture wire request", http.StatusBadRequest)
			return
		}
		wire <- observed
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, map[string]string{"eric": `{"response":{"numFound":0,"docs":[]}}`, "openalex": `{"results":[]}`, "crossref": `{"status":"ok","message":{"items":[]}}`}[source])
	}))
	defer server.Close()
	transport := server.Client().Transport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	serverURL, _ := url.Parse(server.URL)
	transport.TLSClientConfig.ServerName = serverURL.Hostname()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "api.ies.ed.gov:443" && address != "api.openalex.org:443" && address != "api.crossref.org:443" {
			return nil, fmt.Errorf("fixture refuses unknown dial")
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	dir := t.TempDir()
	if out := os.Getenv("SCIAIDE_LITERATURE_QUALITY_FIXTURE_OUT"); out != "" {
		if !filepath.IsAbs(out) {
			t.Fatal("fixture capture requires a new absolute directory")
		}
		if err := os.Mkdir(out, 0700); err != nil {
			t.Fatal(err)
		}
		dir = out
	}
	if err := os.WriteFile(filepath.Join(dir, "plan.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeQualityJSONNew(filepath.Join(dir, "preflight.json"), prepared); err != nil {
		t.Fatal(err)
	}
	preflightBytes, err := os.ReadFile(filepath.Join(dir, "preflight.json"))
	if err != nil {
		t.Fatal(err)
	}
	capture := &literatureComparisonTransport{next: transport, dir: dir, max: p.MaxHTTP, bySlot: map[string]int{}}
	guard := &qualityDispatchGuard{next: capture, expected: prepared, observed: map[string]qualityRequest{}}
	client := NewClient()
	client.attempts, client.http.Transport = 1, guard
	service, err := research.NewService(defaultWithClient(client))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	rows, traces := []qualityRow{}, []qualityDispatchTrace{}
	for _, slot := range qualitySchedule(p) {
		c, source, ai := slot.Case, slot.Source, slot.ArmIndex
		arm := p.Arms[ai]
		key := c.ID + ":" + arm + ":" + source
		ctx := context.WithValue(context.Background(), literatureComparisonScopeKey{}, literatureComparisonScope{Key: key, CaseID: c.ID, SourceID: source, QueryIndex: ai, MaxHTTP: 1})
		projections := map[string]string{}
		if ai == 1 {
			projections[source] = c.Focused[source]
		}
		result, err := service.Search(ctx, research.SearchCommand{Query: c.LogicalQuery, ProviderQueries: projections, SourceIDs: []string{source}, Limit: 5, Years: p.Years})
		var received qualityRequest
		select {
		case received = <-wire:
		default:
			t.Fatal("missing actual TLS fixture request", result.Sources, err)
		}
		if err != nil || len(result.Sources) != 1 || result.Sources[0].Status != research.SearchEmpty || !reflect.DeepEqual(received, prepared[key].Request) {
			t.Fatal("actual TLS fixture request differed from preflight/boundary capture", c.ID, arm, source, err)
		}
		rows = append(rows, qualityRow{CaseID: c.ID, Arm: arm, SourceID: source, ActualHTTP: 1, Search: result})
		traces = append(traces, qualityDispatchTrace{literatureComparisonTrace: *capture.traces[len(capture.traces)-1], Request: guard.observed[key]})
	}
	if guard.rejected != 0 || len(traces) != p.MaxHTTP {
		t.Fatal("controlled capture lost request evidence")
	}
	result := map[string]any{"captureVersion": "quality-dispatch-v2", "evidenceMode": "controlled_tls_fixture", "guardRejected": guard.rejected, "preflightSha256": qualitySHA(preflightBytes), "specSha256": qualitySHA(encoded), "startedAt": started, "endedAt": time.Now().UTC(), "complete": true, "actualHttp": len(traces), "budgetDenied": capture.denied, "modelHttp": 0, "rows": rows, "http": traces}
	if err := writeQualityJSONNew(filepath.Join(dir, "capture.json"), result); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d controlled TLS requests verified at server, source API HTTP=0 model HTTP=0; NOT a live quality experiment", len(traces))
}
