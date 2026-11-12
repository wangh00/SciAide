package connectors

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/research"
)

type qualityCase struct {
	ID           string            `json:"id"`
	LogicalQuery string            `json:"logicalQuery"`
	Focused      map[string]string `json:"focusedProviderQueries"`
}
type qualityPlan struct {
	PopulationSuffixes     map[string]string         `json:"populationSuffixes"`
	RequestEvidenceVersion string                    `json:"requestEvidenceVersion"`
	Version                string                    `json:"version"`
	Years                  research.PublicationYears `json:"publicationYears"`
	Limit                  int                       `json:"limitPerSourceQuery"`
	MaxHTTP                int                       `json:"maxHttp"`
	MaxSeconds             int                       `json:"maxSeconds"`
	Sources                []string                  `json:"sources"`
	Arms                   []string                  `json:"arms"`
	Cases                  []qualityCase             `json:"cases"`
}
type qualityRow struct {
	CaseID     string                `json:"caseId"`
	Arm        string                `json:"arm"`
	SourceID   string                `json:"sourceId"`
	ActualHTTP int                   `json:"actualHttp"`
	Search     research.SearchResult `json:"search"`
	Aborted    bool                  `json:"aborted"`
}

func readQualityPlan(t *testing.T) (qualityPlan, []byte) {
	return readQualityPlanVersion(t, "literature-quality-pilot-v1")
}

func readQualityPlanVersion(t *testing.T, version string) (qualityPlan, []byte) {
	t.Helper()
	if version != "literature-quality-pilot-v1" && version != "literature-quality-pilot-v2" && version != "literature-quality-pilot-v3" {
		t.Fatal("unsupported quality protocol", version)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", version+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var p qualityPlan
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	expectedCases, expectedHTTP := 2, 12
	if version == "literature-quality-pilot-v3" {
		expectedCases, expectedHTTP = 4, 24
	}
	if p.Version != version || p.MaxHTTP != expectedHTTP || p.MaxSeconds != 480 || p.Limit != 5 || len(p.Cases) != expectedCases || strings.Join(p.Sources, ",") != "eric,openalex,crossref" || strings.Join(p.Arms, ",") != "baseline,population_focus" || p.Years.From != 2022 || p.Years.To != 2026 {
		t.Fatal("pilot requires a reviewed new version before expanding bounds", p)
	}
	if err := p.Years.Validate(); err != nil {
		t.Fatal(err)
	}
	if version != "literature-quality-pilot-v1" && p.RequestEvidenceVersion != "quality-dispatch-v2" {
		t.Fatal("v2 protocol requires independently captured dispatch evidence")
	}
	seen := map[string]bool{}
	for _, c := range p.Cases {
		if c.ID == "" || seen[c.ID] || c.LogicalQuery == "" || len(c.Focused) != 3 {
			t.Fatal("invalid pilot case", c)
		}
		seen[c.ID] = true
		for _, source := range p.Sources {
			if strings.TrimSpace(c.Focused[source]) == "" {
				t.Fatal("missing prespecified source expression", c.ID, source)
			}
		}
		if _, err := research.RankedBibliographicQuery(c.Focused["crossref"]); err != nil {
			t.Fatal(err)
		}
	}
	return p, b
}

func TestLiteratureQualityPilotPlan(t *testing.T) { readQualityPlan(t) }

// This transport never delegates: captured JSON passes through the production
// adapters again, while queries are recompiled by the current Service. Its URL
// is a local reconstruction, NOT evidence of the original wire query.
type qualityReplayTransport struct {
	endpoint string
	sourceID string
	body     []byte
	calls    int
	request  *qualityRequest
}

func (r *qualityReplayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.calls++
	if r.calls != 1 || req.Method != http.MethodGet || req.URL.Scheme+"://"+req.URL.Host+req.URL.EscapedPath() != r.endpoint {
		return nil, fmt.Errorf("quality replay refuses unexpected request")
	}
	if r.sourceID != "" {
		snapshot, err := snapshotQualityRequest(req, r.sourceID)
		if err != nil {
			return nil, err
		}
		r.request = &snapshot
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(r.body)), Request: req}, nil
}

func TestLiteratureQualityQueryAuditReplay(t *testing.T) {
	dir, out := os.Getenv("SCIAIDE_LITERATURE_QUALITY_REPLAY_DIR"), os.Getenv("SCIAIDE_LITERATURE_QUALITY_AUDIT_OUT")
	diagnostic := os.Getenv("SCIAIDE_LITERATURE_QUALITY_DIAGNOSTIC_PARTIAL") == "1"
	if diagnostic && dir == "" {
		t.Fatal("partial diagnostic requires a frozen capture; not a successful replay")
	}
	version := "literature-quality-pilot-v1"
	if dir != "" {
		if !filepath.IsAbs(dir) {
			t.Fatal("replay requires an absolute capture directory")
		}
		b, err := os.ReadFile(filepath.Join(dir, "plan.json"))
		var header struct {
			Version string `json:"version"`
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &header); err != nil {
			t.Fatal(err)
		}
		version = header.Version
	}
	p, encoded := readQualityPlanVersion(t, version)
	if diagnostic && p.RequestEvidenceVersion != "quality-dispatch-v2" {
		t.Fatal("partial diagnostic requires captured v2 dispatch evidence; legacy captures are not upgraded")
	}
	specHash := fmt.Sprintf("%x", sha256.Sum256(encoded))
	var captured struct {
		SpecSHA  string                      `json:"specSha256"`
		Complete bool                        `json:"complete"`
		HTTP     []literatureComparisonTrace `json:"http"`
		Rows     []qualityRow                `json:"rows"`
	}
	traces := map[string]literatureComparisonTrace{}
	rowBySlot := map[string]qualityRow{}
	captureHash := ""
	if dir != "" {
		if !filepath.IsAbs(dir) {
			t.Fatal("replay requires an absolute capture directory")
		}
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		dir = resolved
		planBytes, err := os.ReadFile(filepath.Join(dir, "plan.json"))
		if err != nil || !bytes.Equal(planBytes, encoded) {
			t.Fatal("replay requires the unchanged frozen protocol", err)
		}
		b, err := os.ReadFile(filepath.Join(dir, "capture.json"))
		if err != nil {
			t.Fatal(err)
		}
		captureHash = fmt.Sprintf("%x", sha256.Sum256(b))
		if err := json.Unmarshal(b, &captured); err != nil || captured.SpecSHA != specHash || !captured.Complete || len(captured.HTTP) > p.MaxHTTP || !diagnostic && len(captured.HTTP) != p.MaxHTTP {
			t.Fatal("replay requires all frozen source slots", err)
		}
		for i, trace := range captured.HTTP {
			if trace.Ordinal != i+1 || trace.QueryIndex < 0 || trace.QueryIndex >= len(p.Arms) || trace.BodyFile != fmt.Sprintf("http-%03d.raw", i+1) || trace.Status != 200 && (!diagnostic || trace.Status < 400 || trace.Status > 599) || trace.TransportFailed || trace.CapturedPrefixOnly || trace.BodyReadFailed || trace.BodyCloseFailed || trace.CaptureWriteFailed {
				t.Fatal("replay requires complete successful raw evidence", i+1)
			}
			key := trace.CaseID + ":" + p.Arms[trace.QueryIndex] + ":" + trace.SourceID
			if _, duplicate := traces[key]; duplicate {
				t.Fatal("duplicate replay slot", key)
			}
			traces[key] = trace
		}
		if diagnostic {
			if len(captured.Rows) != p.MaxHTTP {
				t.Fatal("partial diagnostic must preserve every planned row")
			}
			for _, row := range captured.Rows {
				key := row.CaseID + ":" + row.Arm + ":" + row.SourceID
				_, duplicate := rowBySlot[key]
				_, attempted := traces[key]
				if duplicate || row.Aborted || len(row.Search.Sources) != 1 || row.Search.Sources[0].SourceID != row.SourceID || row.ActualHTTP != 0 && row.ActualHTTP != 1 || (row.ActualHTTP == 1) != attempted {
					t.Fatal("invalid partial diagnostic row", key)
				}
				status := row.Search.Sources[0]
				if status.Status != research.SearchFailed && status.Status != research.SearchOK && status.Status != research.SearchEmpty || status.Count != len(row.Search.Works) || status.Status == research.SearchFailed && len(row.Search.Works) != 0 {
					t.Fatal("contradictory partial diagnostic status", key)
				}
				rowBySlot[key] = row
			}
		}
	} else if out != "" {
		t.Fatal("audit export requires an explicitly selected frozen capture")
	}
	entries := []map[string]any{}
	failedSlots := 0
	for _, c := range p.Cases {
		for ai, arm := range p.Arms {
			for _, source := range p.Sources {
				endpoint := map[string]string{"eric": "https://api.ies.ed.gov/eric/", "openalex": "https://api.openalex.org/works", "crossref": "https://api.crossref.org/works"}[source]
				body := []byte(map[string]string{"eric": `{"response":{"numFound":0,"docs":[]}}`, "openalex": `{"meta":{"count":0},"results":[]}`, "crossref": `{"status":"ok","message":{"items":[]}}`}[source])
				trace := literatureComparisonTrace{}
				key := c.ID + ":" + arm + ":" + source
				failed := false
				if diagnostic {
					row, exists := rowBySlot[key]
					if !exists {
						t.Fatal("missing planned diagnostic row", key)
					}
					failed = row.Search.Sources[0].Status == research.SearchFailed
				}
				if dir != "" {
					var ok bool
					trace, ok = traces[key]
					if !ok && !failed || ok && trace.Endpoint != endpoint || !failed && trace.Status != 200 {
						t.Fatal("missing/misrouted replay slot", c.ID, arm, source)
					}
					if ok {
						path, err := filepath.EvalSymlinks(filepath.Join(dir, trace.BodyFile))
						if err != nil || filepath.Dir(path) != dir {
							t.Fatal("raw evidence escaped replay directory", err)
						}
						info, err := os.Stat(path)
						if err != nil || !info.Mode().IsRegular() || info.Size() > 8<<20 {
							t.Fatal("raw evidence is not a bounded regular file", err)
						}
						f, err := os.Open(path)
						if err != nil {
							t.Fatal(err)
						}
						body, err = io.ReadAll(io.LimitReader(f, (8<<20)+1))
						closeErr := f.Close()
						if err != nil || closeErr != nil || len(body) != trace.CapturedBytes || len(body) > 8<<20 || fmt.Sprintf("%x", sha256.Sum256(body)) != trace.CapturedSHA256 {
							t.Fatal("raw evidence hash/size mismatch", trace.Ordinal, err, closeErr)
						}
					}
					if failed {
						// Do NOT parse a failure response as an empty success. This
						// local envelope is only for recompiling query/projection audit.
						body = []byte(map[string]string{"eric": `{"response":{"numFound":0,"docs":[]}}`, "openalex": `{"results":[]}`, "crossref": `{"status":"ok","message":{"items":[]}}`}[source])
					}
				}
				transport := &qualityReplayTransport{endpoint: endpoint, sourceID: source, body: body}
				client := NewClient()
				client.attempts = 1
				client.http.Transport = transport
				service, err := research.NewService(defaultWithClient(client))
				if err != nil {
					t.Fatal(err)
				}
				projections := map[string]string{}
				if ai == 1 {
					projections[source] = c.Focused[source]
				}
				found, err := service.Search(context.Background(), research.SearchCommand{Query: c.LogicalQuery, ProviderQueries: projections, SourceIDs: []string{source}, Limit: p.Limit, Years: p.Years})
				if err != nil || transport.calls != 1 || len(found.Sources) != 1 || found.Sources[0].Status == research.SearchFailed {
					t.Fatal("local production replay failed", c.ID, arm, source, found.Sources, err)
				}
				entry := map[string]any{"caseId": c.ID, "arm": arm, "sourceId": source, "bodyFile": trace.BodyFile, "bodySha256": trace.CapturedSHA256, "search": found}
				if failed {
					delete(entry, "search")
					entry["disposition"], entry["projectionAudit"], entry["projectionRequest"] = "failed_not_replayed", found.Sources[0], transport.request
					failedSlots++
				}
				if p.Version != "literature-quality-pilot-v1" {
					if !failed {
						entry["request"] = transport.request
					}
				}
				entries = append(entries, entry)
			}
		}
	}
	if out != "" {
		if !filepath.IsAbs(out) {
			t.Fatal("audit export requires a new absolute output file")
		}
		bundle := map[string]any{"version": "literature-quality-replay-v1", "kind": "current_service_raw_replay_no_network", "protocolSha256": specHash, "captureSha256": captureHash, "modelHttp": 0, "networkHttp": 0, "entries": entries}
		if diagnostic {
			bundle["kind"], bundle["diagnosticPartial"], bundle["failedSlotsNotReplayed"] = "current_service_partial_diagnostic_no_network", true, failedSlots
		}
		b, err := json.MarshalIndent(bundle, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal("refuse to overwrite replay output", err)
		}
		_, writeErr := f.Write(append(b, '\n'))
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal(writeErr, closeErr)
		}
	}
	t.Logf("current Service/adapter evidence: %d slots, failedNotReplayed=%d diagnosticPartial=%v networkHttp=0 modelHttp=0; not provider-side wire proof or quality acceptance", len(entries), failedSlots, diagnostic)
}

func TestLiteratureQualityReplayTransportIsClosed(t *testing.T) {
	for _, target := range []string{"https://api.openalex.org/works", "https://example.invalid/eric/", "http://api.ies.ed.gov/eric/"} {
		r := &qualityReplayTransport{endpoint: "https://api.ies.ed.gov/eric/"}
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.RoundTrip(req); err == nil {
			t.Fatal("replay accepted wrong source/endpoint", target)
		}
	}
	r := &qualityReplayTransport{endpoint: "https://api.ies.ed.gov/eric/", body: []byte(`{}`)}
	req, _ := http.NewRequest(http.MethodGet, r.endpoint, nil)
	resp, err := r.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, err := r.RoundTrip(req); err == nil {
		t.Fatal("replay accepted a second request")
	}
}

// Opt-in: same production Service and adapters, not a second research runner.
// The existing v2 capture boundary uses explicit HTTP/1 single-attempt transport.
func TestLiteratureQualityPilotLive(t *testing.T) {
	if os.Getenv("SCIAIDE_LITERATURE_QUALITY_LIVE") != "1" {
		t.Skip("public-source metadata-quality pilot is opt-in")
	}
	version := os.Getenv("SCIAIDE_LITERATURE_QUALITY_PROTOCOL")
	if version != "literature-quality-pilot-v2" && version != "literature-quality-pilot-v3" {
		t.Fatal("new live captures require explicit v2 or v3 protocol selection; legacy captures are not upgraded")
	}
	p, encoded := readQualityPlanVersion(t, version)
	preflight, err := qualityPreflight(p)
	if err != nil {
		t.Fatal("refuse live dispatch before validated query controls", err)
	}
	out := os.Getenv("SCIAIDE_LITERATURE_QUALITY_OUT")
	if !filepath.IsAbs(out) {
		t.Fatal("pilot requires a new absolute output directory")
	}
	if err := os.Mkdir(out, 0700); err != nil {
		t.Fatal("refuse to overwrite pilot capture", err)
	}
	if err := os.WriteFile(filepath.Join(out, "plan.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeQualityJSONNew(filepath.Join(out, "preflight.json"), preflight); err != nil {
		t.Fatal(err)
	}
	preflightBytes, err := os.ReadFile(filepath.Join(out, "preflight.json"))
	if err != nil {
		t.Fatal(err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(encoded))
	client := NewClient()
	client.attempts = 1
	client.timeout = 12 * time.Second
	single, ok := client.http.Transport.(interface {
		RoundTripSingleAttempt(*http.Request) (*http.Response, error)
	})
	if !ok {
		t.Fatal("pilot requires the non-replaying source transport")
	}
	capture := &literatureComparisonTransport{next: singleAttemptLiveDelegate{next: single}, dir: out, max: p.MaxHTTP, bySlot: map[string]int{}}
	guard := &qualityDispatchGuard{next: capture, expected: preflight, observed: map[string]qualityRequest{}}
	client.http.Transport = guard
	service, err := research.NewService(defaultWithClient(client))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.MaxSeconds)*time.Second)
	defer cancel()
	started := time.Now().UTC()
	rows := []qualityRow{}
	t.Logf("frozen quality pilot=%s maxHttp=%d maxSeconds=%d modelHttp=0", sum, p.MaxHTTP, p.MaxSeconds)
	for _, slot := range qualitySchedule(p) {
		if ctx.Err() != nil {
			break
		}
		c, source, ai := slot.Case, slot.Source, slot.ArmIndex
		arm := p.Arms[ai]
		key := c.ID + ":" + arm + ":" + source
		callCtx := context.WithValue(ctx, literatureComparisonScopeKey{}, literatureComparisonScope{Key: key, CaseID: c.ID, SourceID: source, QueryIndex: ai, MaxHTTP: 1})
		projections := map[string]string{}
		if ai == 1 {
			projections[source] = c.Focused[source]
		}
		found, e := service.Search(callCtx, research.SearchCommand{Query: c.LogicalQuery, ProviderQueries: projections, SourceIDs: []string{source}, Limit: p.Limit, Years: p.Years})
		capture.mu.Lock()
		used := capture.bySlot[key]
		capture.mu.Unlock()
		rows = append(rows, qualityRow{CaseID: c.ID, Arm: arm, SourceID: source, ActualHTTP: used, Search: found, Aborted: e != nil})
		status := "aborted"
		if len(found.Sources) == 1 {
			status = string(found.Sources[0].Status) + ":" + string(found.Sources[0].ErrorCode)
		}
		t.Logf("case=%s arm=%s source=%s http=%d status=%s records=%d", c.ID, arm, source, used, status, len(found.Works))
	}
	capture.mu.Lock()
	traces, denied := capture.traces, capture.denied
	capture.mu.Unlock()
	complete := ctx.Err() == nil && len(rows) == p.MaxHTTP
	v2Traces := []qualityDispatchTrace{}
	for _, trace := range traces {
		key := trace.CaseID + ":" + p.Arms[trace.QueryIndex] + ":" + trace.SourceID
		r := guard.observed[key]
		v2Traces = append(v2Traces, qualityDispatchTrace{literatureComparisonTrace: *trace, Request: r})
	}
	result := map[string]any{"captureVersion": "quality-dispatch-v2", "evidenceMode": "public_api", "guardRejected": guard.rejected, "preflightSha256": qualitySHA(preflightBytes), "specSha256": sum, "startedAt": started, "endedAt": time.Now().UTC(), "complete": complete, "actualHttp": len(traces), "budgetDenied": denied, "modelHttp": 0, "rows": rows, "http": v2Traces}
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "capture.json"), append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if !complete || denied != 0 || guard.rejected != 0 {
		t.Error("pilot stopped at declared bound; retain partial evidence, do not expand or rerun automatically")
	}
	for _, trace := range traces {
		if trace.CaptureWriteFailed {
			t.Error("source capture write failed", trace.Ordinal)
		}
	}
	t.Logf("complete=%v sourceHttp=%d modelHttp=0; API failures are observations, not successful empty searches", complete, len(traces))
}
