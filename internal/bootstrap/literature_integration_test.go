package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

type literatureConnectorFixture struct {
	name     string
	paged    bool
	noGrowth bool
}

func (c literatureConnectorFixture) Source() research.Source {
	return research.Source{ID: c.name, Name: c.name, Description: "Local literature fixture", Domain: "literature", Host: "fixture.invalid", Homepage: "https://fixture.invalid", KeyFree: true}
}
func (c literatureConnectorFixture) Search(_ context.Context, o research.SearchOptions) ([]research.Work, error) {
	count := 15
	start := 0
	if c.paged {
		count = 20
		if o.Offset >= 40 {
			count, start = 1, 200
		}
	}
	if strings.Contains(o.Query, "targeted") && !c.noGrowth {
		count = 1
		start = 100
	}
	values := []research.Work{}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("%s-%d", c.name, start+i)
		title := "Unrelated " + id
		if c.name == "pubmed" && (i == 14 || start == 100) {
			title = "Eligible " + id
		}
		title += " time\u2010restricted eating"
		values = append(values, research.Work{SourceID: c.name, SourceRecordID: id, Title: title, Abstract: "Complete source abstract", Authors: []research.Author{{Name: "Author " + id}}, Year: 2024, Identifiers: research.Identifiers{DOI: "10.5555/" + id}, WorkType: "journal-article"})
	}
	return values, nil
}
func (c literatureConnectorFixture) Fetch(context.Context, string) (research.Work, error) {
	return research.Work{}, fmt.Errorf("unexpected fetch")
}

func TestLiteratureAutomaticSupplementBeforeHumanConfirmation(t *testing.T) {
	t.Run("direct", func(t *testing.T) { testLiteratureAutomaticLoop(t, false) })
	t.Run("hierarchical_readback", func(t *testing.T) { testLiteratureAutomaticLoop(t, true) })
	t.Run("pages_without_repeated_coverage", func(t *testing.T) { testLiteratureAutomaticLoop(t, false, true) })
	t.Run("unchanged_supplement_reuses_coverage", func(t *testing.T) { testLiteratureAutomaticLoop(t, false, false, true) })
}

func testLiteratureAutomaticLoop(t *testing.T, hierarchical bool, paging ...bool) {
	paged := len(paging) > 0 && paging[0]
	noGrowth := len(paging) > 1 && paging[1]
	var mu sync.Mutex
	seen := map[string]bool{}
	coverageCalls := 0
	synthesisCalls, readbackCalls := 0, 0
	eligible := map[string]bool{}
	server := newResearchStarterOutputTestServer(t, func(output map[string]any, prompt string) {
		if _, ok := output["phase"]; !ok {
			return
		}
		start := strings.LastIndex(prompt, "<stage_input>")
		if start < 0 {
			t.Error("missing stage input")
			return
		}
		text := prompt[start+len("<stage_input>"):]
		end := strings.Index(text, "</stage_input>")
		if end < 0 {
			t.Error("missing stage input close")
			return
		}
		var in struct {
			Children []struct {
				CandidateIDs []string `json:"candidateIds"`
			} `json:"childSummaries"`
			Records []struct {
				CandidateID string `json:"candidateId"`
				Title       string `json:"title"`
			} `json:"evidenceRecords"`
			Candidates []struct {
				ID               string `json:"id"`
				Title            string `json:"title"`
				Abstract         string `json:"abstract"`
				AbstractSegments []struct {
					ID   string `json:"id"`
					Text string `json:"text"`
				} `json:"abstractSegments"`
			} `json:"candidates"`
			Literature struct {
				Triage   bool   `json:"triage"`
				Phase    string `json:"phase"`
				Readback bool   `json:"readback"`
			} `json:"_literature"`
		}
		if err := json.Unmarshal([]byte(text[:end]), &in); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		assessments := []any{}
		notes := []any{}
		ids := []string{}
		for _, c := range in.Candidates {
			decision := "exclude"
			if hierarchical {
				decision = "support"
			}
			if strings.HasPrefix(c.Title, "Eligible") {
				decision = "core"
				ids = append(ids, c.ID)
				eligible[c.ID] = true
			}
			if in.Literature.Phase == "batch" {
				if in.Literature.Readback {
					readbackCalls++
				} else if in.Literature.Triage && seen[c.ID] {
					t.Error("unchanged candidate was screened again", c.ID)
				}
				seen[c.ID] = true
				if c.Abstract == "" && len(c.AbstractSegments) == 0 {
					t.Error("abstract omitted")
				}
			}
			assessments = append(assessments, map[string]any{"candidateId": c.ID, "decision": decision, "relevance": "high", "reason": "Assessed against frozen scope", "importAction": map[string]string{"core": "direct", "support": "verify", "exclude": "exclude"}[decision], "purpose": "Verify evidence relevant to frozen question"})
			if len(c.AbstractSegments) == 0 {
				t.Error("missing source segment inventory")
				return
			}
			notes = append(notes, map[string]any{"candidateId": c.ID, "design": "unknown", "population": "unknown", "intervention": "unknown", "comparator": "unknown", "outcome": "unknown", "finding": "unknown", "uncertainty": "unknown", "quotes": []any{map[string]any{"field": "abstract", "segmentId": c.AbstractSegments[0].ID}}})
		}
		for key := range output {
			delete(output, key)
		}
		output["phase"] = in.Literature.Phase
		output["summary"] = "Fixture assessment"
		if in.Literature.Phase == "batch" {
			if hierarchical {
				for _, value := range notes {
					n := value.(map[string]any)
					for _, key := range []string{"design", "population", "intervention", "comparator", "outcome", "finding", "uncertainty"} {
						n[key] = strings.Repeat("原文待核验", 50)
					}
				}
			}
			output["candidateAssessments"] = assessments
			if in.Literature.Triage {
				for _, value := range notes {
					n := value.(map[string]any)
					for _, key := range []string{"design", "population", "intervention", "comparator", "outcome", "finding", "uncertainty"} {
						delete(n, key)
					}
				}
			}
			output["evidenceNotes"] = notes
			return
		}
		for _, r := range in.Records {
			if hierarchical && in.Literature.Phase == "synthesis" {
				ids = append(ids, r.CandidateID)
				continue
			}
			if strings.HasPrefix(r.Title, "Eligible") {
				ids = append(ids, r.CandidateID)
			}
		}
		for _, child := range in.Children {
			for _, id := range child.CandidateIDs {
				if eligible[id] {
					ids = append(ids, id)
				}
			}
		}
		if in.Literature.Phase == "synthesis" {
			synthesisCalls++
			findings := []any{}
			for _, id := range ids {
				findings = append(findings, map[string]any{"text": "Source assessment remains provisional", "candidateIds": []string{id}})
			}
			output["findings"] = findings
			output["uncertainties"] = []any{}
			return
		}
		sufficient := false
		if in.Literature.Phase == "coverage" {
			coverageCalls++
			sufficient = !hierarchical || coverageCalls >= 2
		}
		output["summary"] = "Fixture assessment"
		output["recommendedCandidateIds"] = ids
		output["coverage"] = map[string]any{"strength": "limited", "sufficientForClaimedScope": sufficient, "independentStudyEstimate": len(in.Candidates), "directPopulationMatches": len(ids), "abstractAvailable": len(in.Candidates), "metadataOnly": 0, "gaps": []string{}}
		output["supplementalQueries"] = []string{}
		output["recommendation"] = "expand_search"
		if sufficient {
			output["recommendation"] = "use_recommendation"
		}
		output["recheckCandidateIds"] = []string{}
		output["rechecks"] = []any{}
		if hierarchical && coverageCalls == 1 {
			output["recheckCandidateIds"] = []string{ids[0]}
			output["rechecks"] = []any{map[string]any{"candidateId": ids[0], "question": "Verify the eligible abstract against the provisional finding"}}
		}
		output["findings"] = []any{}
		if len(ids) > 0 {
			output["findings"] = []any{map[string]any{"text": "Provisional source finding", "candidateIds": ids}}
		}
		output["uncertainties"] = []any{}
	})
	application, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}, ResearchConnectors: []research.Connector{literatureConnectorFixture{"pubmed", paged, noGrowth}, literatureConnectorFixture{"openalex", paged, noGrowth}, literatureConnectorFixture{"crossref", paged, noGrowth}}})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	application.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, application, server.URL)
	project, err := application.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Literature automatic loop"})
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the production host protocol using an isolated local model and
	// deterministic sources; scientific schema checks have focused unit tests.
	schema := json.RawMessage(`{"type":"object","required":["phase","summary"],"properties":{"phase":{"type":"string"},"summary":{"type":"string"},"recommendedCandidateIds":{"type":"array","items":{"type":"string"}},"candidateAssessments":{"type":"array","items":{"type":"object"}},"coverage":{"type":"object"},"supplementalQueries":{"type":"array","items":{"type":"string"}},"recommendation":{"type":"string"},"evidenceNotes":{"type":"array","items":{"type":"object"}},"recheckCandidateIds":{"type":"array","items":{"type":"string"}},"rechecks":{"type":"array","items":{"type":"object"}},"findings":{"type":"array","items":{"type":"object"}},"uncertainties":{"type":"array","items":{"type":"object"}}}}`)
	definition := workflow.Definition{SchemaVersion: 1, Name: "Literature", Inputs: []workflow.Port{{Name: "query", Type: workflow.TypeString, Required: true}}, Nodes: []workflow.Node{
		{ID: "literature_discovery", Name: "Search", Kind: workflow.NodeTool, ToolName: "builtin.research.workflow.search", Arguments: json.RawMessage(`{"limit":20,"referencesOnly":true}`)},
		{ID: "candidate_screening", Name: "Screen", Kind: workflow.NodeAIAnalysis, Arguments: json.RawMessage(`{}`), Prompt: "Screen frozen candidates.", PromptVersion: "dynamic-candidate-screening-v11", ReviewPolicy: workflow.AIReviewAuto, OutputSchema: schema},
		{ID: "candidate_review", Name: "Confirm materials", Kind: workflow.NodeCandidateSelection, Arguments: json.RawMessage(`{}`), Prompt: "Confirm materials"},
	}, Edges: []workflow.Edge{
		{FromNode: "$input", FromPort: "query", ToNode: "literature_discovery", ToPort: "query"},
		{FromNode: "$input", FromPort: "query", ToNode: "candidate_review", ToPort: "query"},
		{FromNode: "literature_discovery", FromPort: "structured.candidates", ToNode: "candidate_screening", ToPort: "candidates"},
		{FromNode: "candidate_screening", FromPort: "analysis", ToNode: "candidate_review", ToPort: "screening"},
		{FromNode: "literature_discovery", FromPort: "structured.candidates", ToNode: "candidate_review", ToPort: "candidates"},
	}}
	if preview := application.WorkflowFacade.Validate(project.ID, definition); len(preview.Diagnostics) > 0 {
		t.Logf("diagnostics: %+v", preview.Diagnostics)
	}
	saved, err := application.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.WorkflowFacade.Start(workflow.StartCommand{ProjectID: project.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"query":"initial scope"}`), PermissionMode: "full_access", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	waiting := waitForWorkflowState(t, application, project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation, 30*time.Second)
	wantSearches := 1
	if waiting.Steps[0].Attempt != wantSearches || waiting.Steps[2].Status != workflow.StepWaitingHumanConfirmation {
		t.Fatalf("did not supplement before confirmation: %+v", waiting.Steps)
	}
	var input struct {
		Candidates []research.Candidate `json:"candidates"`
		Screening  struct {
			Recommended []string `json:"recommendedCandidateIds"`
		} `json:"screening"`
	}
	_ = json.Unmarshal(waiting.Steps[2].Input, &input)
	wantRecommended := 1
	if hierarchical {
		wantRecommended = 45
	}
	if noGrowth {
		wantRecommended = 1
	}
	if len(input.Screening.Recommended) != wantRecommended {
		t.Fatal("supplemental evidence not offered", string(waiting.Steps[2].Input))
	}
	if _, err := application.WorkflowFacade.ReadLiteratureCandidate(project.ID, started.Run.ID, waiting.Steps[2].ID, input.Screening.Recommended[0]); err != nil {
		t.Fatal("lazy source readback failed", err)
	}
	normalizedBatches := 0
	for _, execution := range waiting.AIExecutions {
		var output struct {
			Phase string `json:"phase"`
		}
		if json.Unmarshal(execution.Output, &output) != nil || output.Phase != "batch" {
			continue
		}
		normalizedBatches++
		if execution.Status != "completed" || !strings.Contains(execution.OutputText, "segmentId") || strings.Contains(execution.OutputText, "Complete source abstract") || !strings.Contains(string(execution.Output), "Complete source abstract") {
			t.Fatal("source normalization did not preserve raw text and exact accepted output")
		}
	}
	var normalizationEvents int
	if err := application.store.DB().QueryRow(`SELECT COUNT(*) FROM workflow_events WHERE workflow_run_id=? AND event_type='workflow.ai_output_normalized' AND payload_json LIKE '%literature_quote_source_segment%'`, started.Run.ID).Scan(&normalizationEvents); err != nil || normalizedBatches == 0 || normalizationEvents != normalizedBatches {
		t.Fatalf("source quote audit: events=%d batches=%d err=%v", normalizationEvents, normalizedBatches, err)
	}
	mu.Lock()
	defer mu.Unlock()
	wantCoverage := 0
	if noGrowth {
		wantCoverage = 0
		var reused int
		if err := application.store.DB().QueryRow(`SELECT COUNT(*) FROM workflow_events WHERE workflow_run_id=? AND event_type='workflow.literature_coverage_reused'`, started.Run.ID).Scan(&reused); err != nil || reused != 0 {
			t.Fatalf("coverage reuse count=%d err=%v", reused, err)
		}
	}
	if hierarchical {
		if synthesisCalls != 0 || readbackCalls != 0 {
			t.Fatal("preselection repeated analysis", synthesisCalls, readbackCalls)
		}
	}
	wantCandidates := 45
	if noGrowth {
		wantCandidates = 45
	}
	if paged {
		wantCandidates = 60
	}
	if len(seen) != wantCandidates || coverageCalls != wantCoverage {
		t.Fatal("missing candidates/coverage", len(seen), coverageCalls)
	}
}
