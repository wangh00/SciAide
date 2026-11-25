package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
)

type discoveryToolFixture struct {
	denied       bool
	searched     int
	read         int
	listed       int
	fieldRead    int
	last         research.DiscoverySearchCommand
	foreignIndex bool
}

func (f *discoveryToolFixture) ReadTaskDiscoveryField(_ context.Context, c research.DiscoveryFieldCommand) (json.RawMessage, error) {
	f.fieldRead++
	if c.ProjectID != "p" || c.ResearchTaskID != "task" || c.QueryID != "q" || c.CandidateID != "candidate" {
		return nil, errors.New("foreign saved field")
	}
	return json.Marshal(map[string]any{"status": "unverified_discovery_field", "field": c.Field, "offset": c.Offset, "limit": c.Limit, "content": "exact saved field"})
}

func (f *discoveryToolFixture) ValidateResearchDiscoveryTool(context.Context, string, string, string, string, string) error {
	if f.denied {
		return errors.New("scope denied")
	}
	return nil
}
func (f *discoveryToolFixture) Search(_ context.Context, c research.DiscoverySearchCommand) (research.DiscoverySearchResult, error) {
	f.searched++
	f.last = c
	return research.DiscoverySearchResult{Query: research.Query{ID: "q", ProjectID: "p", ResearchTaskID: "task", Partial: true, Sources: []research.SourceSearch{{SourceID: "eric", Status: research.SearchOK, Count: 1}, {SourceID: "crossref", Status: research.SearchFailed, ErrorCode: research.FailureRateLimited, Retryable: true}}}}, nil
}
func (f *discoveryToolFixture) ReadTaskDiscoveryPage(_ context.Context, c research.CandidateListCommand) (research.CandidatePage, error) {
	f.read++
	if c.ProjectID != "p" || c.ResearchTaskID != "task" || c.QueryID != "q" {
		return research.CandidatePage{}, errors.New("foreign query")
	}
	return research.CandidatePage{Offset: c.Offset, Limit: c.Limit, Total: 7, Items: []research.Candidate{{ID: "candidate", Preferred: research.Work{SourceID: "eric", SourceRecordID: "EJ1234567", Title: strings.Repeat("标题🧪", 1000), Abstract: strings.Repeat("摘要\n\"", 2000), RawSnapshot: json.RawMessage(`{"private":"raw-source-only"}`)}}}}, nil
}
func (f *discoveryToolFixture) QueryHistory(_ context.Context, c research.QueryPageCommand) ([]research.Query, error) {
	f.listed++
	owner := "task"
	if f.foreignIndex {
		owner = "foreign"
	}
	return []research.Query{{ID: "q", ProjectID: "p", ResearchTaskID: owner, Text: "saved query", ResultCount: 7}}, nil
}

func TestTaskDiscoveryToolsUseHostScopeAndBoundedUnverifiedViews(t *testing.T) {
	ctx := context.Background()
	f := &discoveryToolFixture{}
	tools := NewResearchDiscoveryTools(f, f)
	for i, value := range tools {
		d, err := value.Definition(ctx)
		if err != nil || tool.ValidateDefinition(d) != nil || !d.Idempotent {
			t.Fatal("invalid task discovery definition", d, err)
		}
		if i == 0 && (d.Risk != tool.RiskModerate || len(d.Permissions) == 0) || i == 1 && (d.Risk != tool.RiskLow || len(d.Permissions) != 0) {
			t.Fatal("task discovery changed network or read permissions", d)
		}
	}
	inv := tool.Invocation{SubjectKind: tool.SubjectChatRun, ProjectID: "p", ResearchTaskID: "task", RunID: "run", CallID: "call", Arguments: json.RawMessage(`{"query":"q","sourceIds":["eric","crossref"],"limit":5}`)}
	result, err := tools[0].Invoke(ctx, inv)
	if err != nil || f.searched != 1 || f.last.ProjectID != "p" || f.last.ResearchTaskID != "task" || f.last.SnapshotKey != "call" || f.last.EnrichMetadata || len(result.Citations) != 0 || len(result.Artifacts) != 0 {
		t.Fatal("discovery lost host binding or promoted material", f.last, result, err)
	}
	var payload struct {
		Status     string                  `json:"status"`
		Partial    bool                    `json:"partial"`
		Sources    []research.SourceSearch `json:"sources"`
		Next       int                     `json:"nextOffset"`
		Candidates []struct {
			Title, Abstract struct {
				Text    string `json:"text"`
				Omitted bool   `json:"omitted"`
				SHA     string `json:"sha256"`
			}
		} `json:"candidates"`
	}
	if json.Unmarshal(result.Structured, &payload) != nil || payload.Status != "unverified_discovery" || !payload.Partial || payload.Next != 1 || len(payload.Sources) != 2 || payload.Sources[1].Status != research.SearchFailed || payload.Sources[1].ErrorCode != research.FailureRateLimited {
		t.Fatal("failure/partial state became an empty successful search", string(result.Structured))
	}
	c := payload.Candidates[0]
	if !c.Title.Omitted || !c.Abstract.Omitted || utf8.RuneCountInString(c.Title.Text) != 64 || c.Abstract.Text != "" || len(c.Abstract.SHA) != 64 || strings.Contains(string(result.Structured), "raw-source-only") || len(result.Structured) > maxDiscoveryPageBytes {
		t.Fatal("discovery preview hid omission or exposed raw source")
	}
	inv.Arguments = json.RawMessage(`{"queryId":"q","offset":3}`)
	if _, err = tools[1].Invoke(ctx, inv); err != nil || f.searched != 1 {
		t.Fatal("saved-page read repeated source search", err)
	}
	inv.Arguments = json.RawMessage(`{}`)
	if _, err = tools[1].Invoke(ctx, inv); err != nil || f.listed != 1 || f.searched != 1 {
		t.Fatal("saved query identity cannot be recovered without searching", err)
	}
	f.foreignIndex = true
	if _, err = tools[1].Invoke(ctx, inv); err == nil {
		t.Fatal("foreign query index accepted")
	}
}

func TestTaskDiscoveryToolsRejectScopeAndArgumentsBeforeIO(t *testing.T) {
	ctx := context.Background()
	f := &discoveryToolFixture{denied: true}
	tools := NewResearchDiscoveryTools(f, f)
	inv := tool.Invocation{SubjectKind: tool.SubjectChatRun, ProjectID: "p", ResearchTaskID: "task", Arguments: json.RawMessage(`{"query":"q"}`)}
	if _, err := tools[0].Invoke(ctx, inv); err == nil {
		t.Fatal("missing scope accepted")
	}
	inv.SubjectKind = tool.SubjectWorkflowRun
	if _, err := tools[0].Invoke(ctx, inv); err == nil {
		t.Fatal("frozen workflow acquired task discovery")
	}
	f.denied = false
	inv.SubjectKind = tool.SubjectChatRun
	for _, args := range []string{`{"query":"q","researchTaskId":"foreign"}`, `{"query":"q","limit":21}`, `{"query":"q","sourceIds":["not-a-source"]}`} {
		inv.Arguments = json.RawMessage(args)
		if _, err := tools[0].Invoke(ctx, inv); err == nil {
			t.Fatal("invalid search arguments accepted", args)
		}
	}
	for _, args := range []string{`{"queryId":"q","projectId":"other"}`, `{"queryId":"q","limit":4}`, `{"offset":-1}`, `{"queryId":""}`, `{"candidateId":"candidate"}`, `{"queryId":"q","field":"abstract"}`, `{"queryId":"q","candidateId":"candidate","field":"rawSnapshot"}`, `{"queryId":"q","candidateId":"candidate","limit":401}`, `{"queryId":"q","queryId":"other"}`, `{"queryId":"q","candidateId":"candidate","field":"title","field":"abstract"}`} {
		inv.Arguments = json.RawMessage(args)
		if _, err := tools[1].Invoke(ctx, inv); err == nil {
			t.Fatal("invalid read arguments accepted", args)
		}
	}
	if f.searched != 0 || f.read != 0 || f.listed != 0 || f.fieldRead != 0 {
		t.Fatal("rejected call performed IO", f)
	}
}

func TestTaskDiscoveryFieldReadReusesScopedReadTool(t *testing.T) {
	f := &discoveryToolFixture{}
	reader := NewResearchDiscoveryTools(f, f)[1]
	inv := tool.Invocation{SubjectKind: tool.SubjectChatRun, ProjectID: "p", ResearchTaskID: "task", RunID: "run", CallID: "call", Arguments: json.RawMessage(`{"queryId":"q","candidateId":"candidate","field":"abstract","offset":3,"limit":400}`)}
	result, err := reader.Invoke(context.Background(), inv)
	if err != nil || f.fieldRead != 1 || f.read != 0 || f.listed != 0 || f.searched != 0 || len(result.Citations) != 0 || len(result.Artifacts) != 0 {
		t.Fatal("saved-field read fetched, imported or lost scope", f, result, err)
	}
	if !strings.Contains(string(result.Structured), `"offset":3`) || !strings.Contains(string(result.Structured), `"limit":400`) {
		t.Fatal("field range was changed")
	}
	f.denied = true
	if _, err = reader.Invoke(context.Background(), inv); err == nil || f.fieldRead != 1 {
		t.Fatal("scope failure performed a field read")
	}
}

func TestTaskDiscoveryIndexFitsWholeEntriesAndExactCursor(t *testing.T) {
	items := []map[string]any{}
	for i := 0; i < 3; i++ {
		items = append(items, map[string]any{"id": i, "label": strings.Repeat("🧪\"\n", 90)})
	}
	raw, err := fitDiscoveryIndex(map[string]any{"offset": 9}, "queries", items, 9, true, false)
	if err != nil || len(raw) > maxDiscoveryPageBytes || !json.Valid(raw) {
		t.Fatal("invalid bounded index", len(raw), err)
	}
	var value struct {
		Queries []map[string]any `json:"queries"`
		Next    int              `json:"nextOffset"`
	}
	if json.Unmarshal(raw, &value) != nil || len(value.Queries) < 1 || len(value.Queries) >= len(items) || value.Next != 9+len(value.Queries) {
		t.Fatal("index skipped unshown entries", string(raw))
	}
	for _, item := range value.Queries {
		if item["label"] != strings.Repeat("🧪\"\n", 90) {
			t.Fatal("index split a serialized entry")
		}
	}
	tooWide := []map[string]any{{"recordId": strings.Repeat("x", maxDiscoveryPageBytes)}}
	if _, err = fitDiscoveryIndex(map[string]any{}, "candidates", tooWide, 0, false, false); err == nil {
		t.Fatal("oversized identity silently clipped")
	}
}
