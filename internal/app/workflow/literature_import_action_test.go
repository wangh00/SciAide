package workflow

import (
	"context"
	"encoding/json"
	"github.com/wangh00/SciAide/internal/app/tool"
	"testing"
)

func TestLiteratureImportRecommendationIsNotRetention(t *testing.T) {
	for _, tc := range []struct {
		decision, action, purpose string
		recommended, valid        bool
	}{
		{"core", "direct", "直接比较两种干预的随机试验", true, true},
		{"support", "verify", "题名直接比较两方案，缺摘要，需核对结局", true, true},
		{"support", "background", "泛代谢综述，缺少本课题对照线索", false, true},
		{"exclude", "exclude", "研究对象明确不符合", false, true},
		{"support", "direct", "没有直接依据", false, false},
		{"exclude", "verify", "矛盾", false, false},
		{"support", "verify", "", false, false},
		{"support", "", "未排除不能默认推荐", false, false},
	} {
		a := literatureAssessment{CandidateID: "a", Decision: tc.decision, ImportAction: tc.action, Purpose: tc.purpose}
		if (validateLiteratureImportAction(a) == nil) != tc.valid || literatureImportRecommended(a) != tc.recommended {
			t.Fatalf("%+v", tc)
		}
	}
}

func TestLiteratureActionsSurviveCheckpointAndSelection(t *testing.T) {
	s, repo, _, detail, node := literatureFixture(t, 4)
	input, err := s.prepareLiteratureInput(context.Background(), detail, node, raw(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Candidates []literatureCandidate `json:"candidates"`
	}
	json.Unmarshal(input, &in)
	actions := []string{"direct", "verify", "background", "exclude"}
	decisions := []string{"core", "support", "support", "exclude"}
	assessments := []literatureAssessment{}
	notes := []map[string]any{}
	for i, c := range in.Candidates {
		assessments = append(assessments, literatureAssessment{CandidateID: c.ID, Decision: decisions[i], Relevance: "high", Reason: "Source grounded", ImportAction: actions[i], Purpose: "Contribution or missing information"})
		notes = append(notes, map[string]any{"candidateId": c.ID, "quotes": fixtureLiteratureNote(c).Quotes})
	}
	output := mustJSON(map[string]any{"phase": "batch", "summary": "screened", "candidateAssessments": assessments, "evidenceNotes": notes})
	phase := literaturePhaseNode(node, input)
	if err := (tool.JSONSchemaValidator{}).Validate(phase.OutputSchema, output); err != nil {
		t.Fatal(err)
	}
	if err := validateWorkflowAIStageOutput(phase, output, input); err != nil {
		t.Fatal(err)
	}
	e := AIExecution{ID: "screened", Status: "completed", Output: output}
	step := detail.Steps[1]
	step.Input = input
	if err := s.completeLiteratureScreening(context.Background(), detail, step, node, e, output); err != nil {
		t.Fatal(err)
	}
	detail.Events = append(detail.Events, repo.event)
	detail.AIExecutions = append(detail.AIExecutions, e)
	prior, _ := literaturePrior(detail, literatureState(detail))
	ids := []string{}
	for _, c := range in.Candidates {
		if literatureImportRecommended(prior[c.ID]) {
			ids = append(ids, c.ID)
		}
	}
	if len(ids) != 2 || ids[0] != "0" || ids[1] != "1" {
		t.Fatal(ids)
	}
	analysis := map[string]any{"recommendedCandidateIds": ids}
	info := literatureInput{State: literatureState(detail)}
	if err := s.finalizeLiteratureEvidence(context.Background(), detail, info, &analysis); err != nil {
		t.Fatal(err)
	}
	kept := analysis["selectionCandidates"].([]literatureCandidate)
	if len(kept) != 3 {
		t.Fatal("background must remain visible, exclusion must not", len(kept))
	}
	restored := analysis["candidateAssessments"].([]literatureAssessment)
	if restored[2].ImportAction != "background" || restored[2].Purpose == "" {
		t.Fatal("action lost", restored)
	}
}

func TestLiteratureImportActionSubmissionPreflight(t *testing.T) {
	s, r, _ := submissionValidationFixture(t)
	step := &r.detail.Steps[4]
	step.NodeID = "candidate_screening"
	step.Input = raw(`{"_literature":{"phase":"batch","triage":true},"candidates":[{"id":"a","title":"Graph algorithm comparison"}]}`)
	step.InputSHA256 = hashJSON(step.Input)
	node := CompiledNode{ID: "candidate_screening", Kind: NodeAIAnalysis, PromptVersion: literatureScreeningVersion, OutputSchema: literatureScreeningSchema()}
	node.OutputSchemaSHA256 = hashJSON(node.OutputSchema)
	r.detail.Run.Compilation.Nodes = []CompiledNode{node}
	r.detail.Run.Compilation.CompilationSHA256 = ""
	encoded, err := canonicalJSON(r.detail.Run.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	r.detail.Run.CompilationSHA256 = hashBytes(encoded)
	r.detail.Run.Compilation.CompilationSHA256 = r.detail.Run.CompilationSHA256
	value := map[string]any{"phase": "batch", "summary": "screened", "candidateAssessments": []map[string]any{{"candidateId": "a", "decision": "support", "relevance": "high", "reason": "Title is relevant but abstract missing", "importAction": "verify", "purpose": "Verify algorithm comparison and benchmark conditions"}}, "evidenceNotes": []map[string]any{{"candidateId": "a", "quotes": []map[string]any{{"field": "title", "segmentId": "t0001"}}}}}
	if _, err := s.ValidateResearchSubmission(context.Background(), "conversation", "run", step.ID, string(mustJSON(value))); err != nil {
		t.Fatal(err)
	}
	delete(value["candidateAssessments"].([]map[string]any)[0], "importAction")
	if _, err := s.ValidateResearchSubmission(context.Background(), "conversation", "run", step.ID, string(mustJSON(value))); err == nil {
		t.Fatal("missing import decision accepted")
	}
}
