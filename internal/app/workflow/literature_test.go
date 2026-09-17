package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
	"testing"
	"time"
)

type literatureReaderFixture struct{ values []research.Candidate }

func (r *literatureReaderFixture) LiteratureCandidates(context.Context, string, []string) ([]research.Candidate, error) {
	return r.values, nil
}

type literatureRepositoryFixture struct {
	RuntimeRepository
	event         RuntimeEvent
	rewind        string
	output        json.RawMessage
	continuations int
}

func (r *literatureRepositoryFixture) QueueLiteratureContinuation(_ context.Context, _, _, discovery string, _ int, _ time.Time, event RuntimeEvent) error {
	r.event = event
	r.rewind = discovery
	r.continuations++
	return nil
}
func (r *literatureRepositoryFixture) CompleteStep(_ context.Context, _, _ string, output json.RawMessage, _ int, _ bool, _ json.RawMessage, _ time.Time, _ RuntimeEvent) error {
	r.output = output
	return nil
}
func literatureFixture(t *testing.T, n int) (*RuntimeService, *literatureRepositoryFixture, *literatureReaderFixture, RunDetail, CompiledNode) {
	t.Helper()
	reader := &literatureReaderFixture{}
	for i := 0; i < n; i++ {
		reader.values = append(reader.values, research.Candidate{ID: fmt.Sprint(i), Preferred: research.Work{Title: fmt.Sprintf("Study %d", i), Abstract: "Complete abstract", Authors: []research.Author{}, Year: 2024}})
	}
	repo := &literatureRepositoryFixture{}
	s := &RuntimeService{repository: repo, literature: reader, now: func() time.Time { return time.Now().UTC() }, newID: func() (string, error) { return fmt.Sprint(time.Now().UnixNano()), nil }}
	node := CompiledNode{ID: "candidate_screening", Kind: NodeAIAnalysis, PromptVersion: literatureScreeningVersion, OutputSchema: literatureScreeningSchema()}
	detail := RunDetail{Run: Run{ID: "run", ProjectID: "project", Compilation: Compilation{Nodes: []CompiledNode{node}}}, Steps: []Step{
		{ID: "discovery", NodeID: "literature_discovery", Ordinal: 0, Status: StepCompleted, Output: raw(`{"structured":{"queryIds":["q1"],"queries":["original"],"partial":false}}`)},
		{ID: "screening", NodeID: "candidate_screening", Ordinal: 1, Status: StepRunning, Attempt: 1},
		{ID: "review", NodeID: "candidate_review", Ordinal: 2, Status: StepQueued},
	}}
	return s, repo, reader, detail, node
}
func screenFixture(input json.RawMessage, adequate bool) json.RawMessage {
	var in struct {
		Candidates []literatureCandidate      `json:"candidates"`
		Literature literatureInput            `json:"_literature"`
		Records    []literatureEvidenceRecord `json:"evidenceRecords"`
		Children   []literatureSummary        `json:"childSummaries"`
	}
	_ = json.Unmarshal(input, &in)
	assessments := []literatureAssessment{}
	ids := []string{}
	notes := []literatureNote{}
	for _, c := range in.Candidates {
		decision := "exclude"
		if c.ID == "44" {
			decision = "core"
			ids = append(ids, c.ID)
		}
		assessments = append(assessments, literatureAssessment{CandidateID: c.ID, Decision: decision, Relevance: "high", Reason: "Frozen PICO assessed against full abstract", ImportAction: map[string]string{"core": "direct", "support": "verify", "exclude": "exclude"}[decision], Purpose: "Evaluate frozen question"})
		notes = append(notes, fixtureLiteratureNote(c))
	}
	if in.Literature.Phase == "batch" {
		return mustJSON(map[string]any{"phase": "batch", "summary": "batch", "candidateAssessments": assessments, "evidenceNotes": notes})
	}
	if in.Literature.Phase == "synthesis" {
		findings := []literatureFinding{}
		ids := []string{}
		for _, r := range in.Records {
			if r.Assessment.Decision != "exclude" {
				ids = append(ids, r.CandidateID)
			}
		}
		for start := 0; start < len(ids); start += 25 {
			findings = append(findings, literatureFinding{Text: "Source must be verified", CandidateIDs: ids[start:min(start+25, len(ids))]})
		}
		for _, child := range in.Children {
			findings = append(findings, child.Findings...)
			findings = append(findings, child.Uncertainties...)
		}
		return mustJSON(map[string]any{"phase": "synthesis", "summary": "group", "findings": findings, "uncertainties": []literatureFinding{}})
	}
	for _, r := range in.Records {
		if r.Assessment.Decision == "core" {
			ids = append(ids, r.CandidateID)
		}
	}
	for _, child := range in.Children {
		for _, id := range child.CandidateIDs {
			if id == "44" {
				ids = append(ids, id)
			}
		}
	}
	recommendation := "expand_search"
	strength := "insufficient"
	if adequate {
		recommendation = "use_recommendation"
		strength = "adequate"
	}
	findings := []literatureFinding{}
	if len(ids) > 0 {
		findings = append(findings, literatureFinding{Text: "Provisional finding", CandidateIDs: ids})
	}
	return mustJSON(map[string]any{"phase": "coverage", "summary": "review", "recommendedCandidateIds": ids, "coverage": map[string]any{"strength": strength, "sufficientForClaimedScope": adequate, "independentStudyEstimate": len(ids), "directPopulationMatches": len(ids), "abstractAvailable": len(ids), "metadataOnly": 0, "gaps": []string{}}, "supplementalQueries": []string{}, "recommendation": recommendation, "recheckCandidateIds": []string{}, "rechecks": []literatureRecheck{}, "findings": findings, "uncertainties": []literatureFinding{}})
}

func fixtureLiteratureNote(c literatureCandidate) literatureNote {
	quotes := []literatureQuote{{Field: "title", Quote: c.Title}}
	if c.Abstract != "" {
		r := []rune(c.Abstract)
		quotes = append(quotes, literatureQuote{Field: "abstract", Quote: string(r[:min(200, len(r))])})
	}
	return literatureNote{CandidateID: c.ID, Design: "unknown", Population: "unknown", Intervention: "unknown", Comparator: "unknown", Outcome: "unknown", Finding: "unknown", Uncertainty: "unknown", Quotes: quotes}
}
func TestLiteratureBatchesPreserveLateCandidatesAndResume(t *testing.T) {
	s, repo, reader, detail, node := literatureFixture(t, 45)
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		input, err := s.prepareLiteratureInput(context.Background(), detail, node, raw(`{"researchContext":{"scope":"frozen"}}`))
		if err != nil {
			t.Fatal(err)
		}
		var in struct {
			Candidates []literatureCandidate `json:"candidates"`
			Literature literatureInput       `json:"_literature"`
		}
		_ = json.Unmarshal(input, &in)
		if in.Literature.Phase != "batch" || len(in.Candidates) > 20 {
			t.Fatal(string(input))
		}
		for _, c := range in.Candidates {
			if seen[c.ID] {
				t.Fatal("duplicate batch", c.ID)
			}
			seen[c.ID] = true
			if c.Abstract != "Complete abstract" {
				t.Fatal("abstract lost")
			}
		}
		output := screenFixture(input, false)
		if err := (tool.JSONSchemaValidator{}).Validate(node.OutputSchema, output); err != nil {
			t.Fatal(err)
		}
		if err := validateWorkflowAIStageOutput(node, output, input); err != nil {
			t.Fatal(err)
		}
		execution := AIExecution{ID: fmt.Sprint(i), Status: "completed", Output: output}
		step := detail.Steps[1]
		step.Input = input
		if err := s.completeLiteratureScreening(context.Background(), detail, step, node, execution, output); err != nil {
			t.Fatal(err)
		}
		detail.AIExecutions = append(detail.AIExecutions, execution)
		detail.Events = append(detail.Events, repo.event)
		// Reconstruct the service after each persisted batch.
		s = &RuntimeService{repository: repo, literature: reader, now: s.now, newID: s.newID}
	}
	if len(seen) != 45 || !seen["44"] {
		t.Fatal("candidate tail omitted", seen)
	}
	input, err := s.prepareLiteratureInput(context.Background(), detail, node, raw(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Candidates []literatureCandidate      `json:"candidates"`
		Literature literatureInput            `json:"_literature"`
		Records    []literatureEvidenceRecord `json:"evidenceRecords"`
	}
	_ = json.Unmarshal(input, &in)
	if in.Literature.Phase != "selection" {
		t.Fatal(string(input))
	}
	step := detail.Steps[1]
	step.Input = input
	step.Input = mustJSON(map[string]any{"_literature": literatureInput{Phase: "coverage", State: in.Literature.State}})
	output := screenFixture(input, true)
	if err := s.completeLiteratureScreening(context.Background(), detail, step, node, AIExecution{ID: "coverage"}, output); err != nil {
		t.Fatal(err)
	}
	if len(repo.output) == 0 || repo.continuations != 3 {
		t.Fatal("did not finish after coverage")
	}
	// Changed source content cannot reuse a previous exclusion.
	reader.values[0].Preferred.Abstract = "Newly available abstract with additional evidence"
	input, err = s.prepareLiteratureInput(context.Background(), detail, node, raw(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(input, &in)
	if in.Literature.Phase != "batch" || len(in.Candidates) != 1 || in.Candidates[0].ID != "0" {
		t.Fatal("stale assessment reused", string(input))
	}
}

func TestLiteratureSupplementLimitsAndSourceFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name         string
		round        int
		failed       bool
		continuation bool
		status       string
	}{
		{"supplement", 0, false, false, "budget_exhausted"}, {"third supplement", 2, false, false, "budget_exhausted"}, {"budget", 3, false, false, "budget_exhausted"}, {"source", 3, true, false, "source_blocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, _, detail, node := literatureFixture(t, 0)
			info := literatureInput{Phase: "coverage", State: literatureCheckpoint{Phase: "coverage", Round: tc.round, Queries: []string{"original"}, SourceFailures: tc.failed}}
			step := detail.Steps[1]
			step.Input = mustJSON(map[string]any{"_literature": info})
			output := screenFixture(step.Input, false)
			if err := s.completeLiteratureScreening(context.Background(), detail, step, node, AIExecution{ID: "coverage"}, output); err != nil {
				t.Fatal(err)
			}
			if tc.continuation {
				if repo.rewind != "discovery" || repo.continuations != 1 {
					t.Fatal("no automatic search")
				}
				detail.Events = append(detail.Events, repo.event)
				input, err := s.prepareLiteratureInput(context.Background(), detail, CompiledNode{ID: "literature_discovery"}, raw(`{"query":"original","referencesOnly":true}`))
				if err != nil {
					t.Fatal(err)
				}
				var query struct {
					Query string `json:"query"`
					Limit int    `json:"limit"`
				}
				_ = json.Unmarshal(input, &query)
				if query.Query != "targeted followup" || query.Limit > 50 {
					t.Fatal(string(input))
				}
			} else {
				var result struct {
					Analysis struct {
						Retrieval struct {
							Status string `json:"status"`
						} `json:"retrieval"`
					} `json:"analysis"`
				}
				_ = json.Unmarshal(repo.output, &result)
				if result.Analysis.Retrieval.Status != tc.status {
					t.Fatal(string(repo.output))
				}
			}
		})
	}
}

func TestLiteratureRefusesMissingAssessmentAndInventedID(t *testing.T) {
	_, _, _, _, node := literatureFixture(t, 0)
	input := raw(`{"candidates":[{"id":"44"},{"id":"45"}]}`)
	output := screenFixture(raw(`{"candidates":[{"id":"44"}]}`), true)
	if err := validateWorkflowAIStageOutput(node, output, input); err == nil {
		t.Fatal("missing assessment accepted")
	}
	if err := validateWorkflowAIStageOutput(node, screenFixture(input, true), raw(`{"candidates":[{"id":"44"}]}`)); err == nil {
		t.Fatal("invented id accepted")
	}
}

func TestLiteraturePagingBudgetAndFrozenSelection(t *testing.T) {
	s, repo, reader, detail, node := literatureFixture(t, 1)
	state := literatureCheckpoint{Phase: "coverage", Queries: []string{"original"}, Pages: []literaturePage{{Query: "original", Source: "pubmed", Offset: 20}}}
	step := detail.Steps[1]
	step.Input = mustJSON(map[string]any{"_literature": literatureInput{Phase: "coverage", State: state}})
	var output map[string]any
	_ = json.Unmarshal(screenFixture(step.Input, false), &output)
	output["supplementalQueries"] = []string{"original"}
	if err := s.completeLiteratureScreening(context.Background(), detail, step, node, AIExecution{ID: "page"}, mustJSON(output)); err != nil {
		t.Fatal(err)
	}
	detail.Events = append(detail.Events, repo.event)
	input, err := s.prepareLiteratureInput(context.Background(), detail, CompiledNode{ID: "literature_discovery"}, raw(`{"query":"original"}`))
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Offset int `json:"offset"`
	}
	_ = json.Unmarshal(input, &page)
	if page.Offset != 0 || repo.continuations != 0 {
		t.Fatal("unexpected automatic paging", string(input))
	}
	detail.Events = nil
	state = literatureCheckpoint{Phase: "batch", Batches: literatureMaxBatches}
	detail.Events = []RuntimeEvent{{Type: literatureCheckpointEvent, Payload: mustJSON(map[string]any{"discoveryId": "discovery", "state": state})}}
	input, err = s.prepareLiteratureInput(context.Background(), detail, node, raw(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Literature literatureInput `json:"_literature"`
	}
	_ = json.Unmarshal(input, &in)
	if in.Literature.Phase != "coverage" || in.Literature.Pending != 1 {
		t.Fatal("batch budget silently dropped candidates", string(input))
	}
	detail.Steps[1].Status = StepCompleted
	detail.Steps[1].Output = raw(`{"candidates":[{"id":"frozen","title":"Original"}]}`)
	reader.values[0].Preferred.Title = "Changed database record"
	input, err = s.prepareLiteratureInput(context.Background(), detail, CompiledNode{ID: "candidate_review"}, raw(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var frozen struct {
		Candidates []literatureCandidate `json:"candidates"`
	}
	_ = json.Unmarshal(input, &frozen)
	if len(frozen.Candidates) != 1 || frozen.Candidates[0].Title != "Original" {
		t.Fatal("selection snapshot mutated", string(input))
	}
}
