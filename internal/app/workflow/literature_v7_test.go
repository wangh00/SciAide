package workflow

import (
	"context"
	"encoding/json"
	"github.com/wangh00/SciAide/internal/app/tool"
	"testing"
)

func withProviderQueryFixture(d tool.Definition) tool.Definition {
	if d.QualifiedName == "builtin.research.workflow.import" {
		out := decodeObject(d.OutputSchema)
		out["properties"].(map[string]any)["materials"] = map[string]any{"type": "array"}
		d.OutputSchema = mustJSON(out)
	}
	if d.QualifiedName == "builtin.knowledge.search" {
		schema := decodeObject(d.InputSchema)
		schema["properties"].(map[string]any)["queries"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
		schema["properties"].(map[string]any)["perDocument"] = map[string]any{"type": "boolean"}
		d.InputSchema = mustJSON(schema)
		out := decodeObject(d.OutputSchema)
		out["properties"].(map[string]any)["documentCoverage"] = map[string]any{"type": "array"}
		d.OutputSchema = mustJSON(out)
	}
	if d.QualifiedName == "builtin.research.workflow.search" {
		schema := decodeObject(d.InputSchema)
		schema["properties"].(map[string]any)["providerQueries"] = map[string]any{"type": "object"}
		d.InputSchema = mustJSON(schema)
	}
	return d
}

func TestLiteratureV7TriageThenExtraction(t *testing.T) {
	s, repo, _, detail, node := literatureFixture(t, 2)
	input, err := s.prepareLiteratureInput(context.Background(), detail, node, raw(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Literature literatureInput       `json:"_literature"`
		Candidates []literatureCandidate `json:"candidates"`
	}
	json.Unmarshal(input, &in)
	if !in.Literature.Triage || len(in.Candidates) != 2 {
		t.Fatal(string(input))
	}
	notes := []map[string]any{}
	assessments := []literatureAssessment{}
	for i, c := range in.Candidates {
		decision := "exclude"
		if i == 1 {
			decision = "support"
		}
		assessments = append(assessments, literatureAssessment{CandidateID: c.ID, Decision: decision, Relevance: "medium", Reason: "source grounded", ImportAction: map[string]string{"support": "verify", "exclude": "exclude"}[decision], Purpose: "Verify question match"})
		notes = append(notes, map[string]any{"candidateId": c.ID, "quotes": fixtureLiteratureNote(c).Quotes})
	}
	output := mustJSON(map[string]any{"phase": "batch", "summary": "triage", "candidateAssessments": assessments, "evidenceNotes": notes})
	phase := literaturePhaseNode(node, input)
	if err := (tool.JSONSchemaValidator{}).Validate(phase.OutputSchema, output); err != nil {
		t.Fatal(err)
	}
	if err := validateWorkflowAIStageOutput(phase, output, input); err != nil {
		t.Fatal(err)
	}
	step := detail.Steps[1]
	step.Input = input
	e := AIExecution{ID: "triage", Status: "completed", Output: output}
	if err := s.completeLiteratureScreening(context.Background(), detail, step, node, e, output); err != nil {
		t.Fatal(err)
	}
	detail.AIExecutions = append(detail.AIExecutions, e)
	detail.Events = append(detail.Events, repo.event)
	input, err = s.prepareLiteratureInput(context.Background(), detail, node, raw(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	in.Literature = literatureInput{}
	json.Unmarshal(input, &in)
	if in.Literature.Phase != "selection" {
		t.Fatal("preselection must not trigger detailed extraction", string(input))
	}
}

func TestLiteratureV7UsesPlannedQueriesOnly(t *testing.T) {
	s, _, _, detail, _ := literatureFixture(t, 0)
	got, err := s.prepareLiteratureInput(context.Background(), detail, CompiledNode{ID: "literature_discovery"}, raw(`{"query":"old seed","queries":["complete topic","complementary topic"],"offset":40,"limit":50,"providerQueries":{"crossref":["compact one","compact two"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	var args struct {
		Query   string
		Queries []string
		Offset  int
		Limit   int
	}
	json.Unmarshal(got, &args)
	if args.Query != "complete topic" || len(args.Queries) != 1 || args.Offset != 0 || args.Limit != 20 {
		t.Fatal(string(got))
	}
}
