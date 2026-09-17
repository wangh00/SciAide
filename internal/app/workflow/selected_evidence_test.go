package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/wangh00/SciAide/internal/app/tool"
	"strings"
	"testing"
)

func TestSelectedEvidencePhaseSchemaIsIdempotent(t *testing.T) {
	for _, mode := range []string{`{"phase":"coverage","direct":true}`, `{"phase":"batch"}`, `{"phase":"coverage"}`} {
		t.Run(mode, func(t *testing.T) {
			base := CompiledNode{ID: "evidence_screening", PromptVersion: selectedEvidenceVersion, OutputSchema: selectedEvidenceSchema()}
			input := raw(`{"_selectedEvidence":` + mode + `}`)
			once := selectedEvidencePhaseNode(base, input)
			for i := 0; i < 4; i++ {
				again := selectedEvidencePhaseNode(once, input)
				if err := (tool.JSONSchemaValidator{}).ValidateSchema(again.OutputSchema); err != nil {
					t.Fatal(err)
				}
				if string(once.OutputSchema) != string(again.OutputSchema) || once.OutputSchemaSHA256 != again.OutputSchemaSHA256 || once.Prompt != again.Prompt {
					t.Fatal("repeated preparation changed the frozen contract")
				}
				once = again
			}
		})
	}
}

func TestDirectEvidenceSubmissionCompletesAndRecoversWithoutRepair(t *testing.T) {
	docs := []string{}
	notes := []map[string]any{}
	for i := 0; i < 32; i++ {
		id := fmt.Sprintf("document-%d", i)
		docs = append(docs, id)
		notes = append(notes, map[string]any{"documentId": id, "finding": "No usable result evidence", "limitations": "metadata only", "references": []string{}})
	}
	input, err := prepareSelectedEvidence(RunDetail{}, mustJSON(map[string]any{"documentIds": docs, "candidates": []any{}}))
	if err != nil {
		t.Fatal(err)
	}
	output := raw(`{"summary":"Evidence is limited","recommendedReferences":[],"citationAssessments":[],"coverage":{"strength":"insufficient","sufficientForClaimedScope":false,"independentStudyEstimate":0,"directPopulationEvidence":false,"fullTextEvidenceAvailable":false,"gaps":["No result evidence"]},"recommendedScope":"Limited draft only","recommendation":"proceed_limited","limitations":["Metadata only"]}`)
	value := decodeObject(output)
	value["documentAnalyses"] = notes
	output = mustJSON(value)
	base := CompiledNode{ID: "evidence_screening", Kind: NodeAgentStage, ReviewPolicy: AIReviewAuto, PromptVersion: selectedEvidenceVersion, OutputSchema: selectedEvidenceSchema()}
	node := selectedEvidencePhaseNode(base, input)
	step := Step{ID: "evidence-step", NodeID: base.ID, NodeKind: base.Kind, Attempt: 1, Status: StepRunning, Input: input, InputSHA256: hashJSON(input)}
	detail := RunDetail{Run: Run{ID: "run", Compilation: Compilation{Nodes: []CompiledNode{base}}}, Steps: []Step{step}}
	execution := AIExecution{WorkflowStepID: step.ID, Attempt: step.Attempt, InputSHA256: step.InputSHA256, OutputText: string(output)}
	// Same preflight used by the submission path, followed by the runtime's
	// second phase projection and actual completion/recovery persistence calls.
	if _, _, err := normalizeWorkflowAIStageSubmissionForInput(execution.OutputText, node, input); err != nil {
		t.Fatal(err)
	}
	r := &submissionRuntimeRepository{}
	if err := submissionRuntimeService(r).completeAIExecution(context.Background(), detail, step, node, execution, true); err != nil {
		t.Fatal(err)
	}
	if r.failure != "" || len(r.repairs) != 0 || len(r.output) == 0 || r.execution.Status != "completed" {
		t.Fatalf("valid direct evidence failed: %+v", r)
	}
	recovered := &submissionRuntimeRepository{}
	if err := submissionRuntimeService(recovered).projectFinishedAIExecution(context.Background(), detail, step, node, r.execution); err != nil {
		t.Fatal(err)
	}
	if recovered.failure != "" || !rawJSONEqual(recovered.output, r.output) || len(recovered.repairs) != 0 {
		t.Fatalf("recovery changed output: %+v", recovered)
	}
	delete(value, "documentAnalyses")
	if _, _, err := normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(value)), node, input); err == nil {
		t.Fatal("fix weakened the required document analyses contract")
	}
}

func TestSelectedEvidenceBatchesRestoreAndCoverMissingDocument(t *testing.T) {
	s, repo, _, detail, _ := literatureFixture(t, 0)
	detail.Steps[1].NodeID = "evidence_screening"
	docs := []string{}
	citations := []tool.CitationRef{}
	for i := 0; i < 7; i++ {
		id := fmt.Sprint(i)
		docs = append(docs, id)
		if i < 6 {
			citations = append(citations, tool.CitationRef{DocumentID: id, Reference: "K-" + id, Quote: strings.Repeat("source", 4000)})
		}
	}
	base := mustJSON(map[string]any{"documentIds": docs, "candidates": citations})
	for batch := 0; batch < 2; batch++ {
		input, err := prepareSelectedEvidence(detail, base)
		if err != nil {
			t.Fatal(err)
		}
		var in struct {
			Info selectedEvidenceInput `json:"_selectedEvidence"`
		}
		json.Unmarshal(input, &in)
		if in.Info.Phase != "batch" || len(in.Info.Documents) == 0 {
			t.Fatal(string(input))
		}
		notes := []map[string]any{}
		for _, id := range in.Info.Documents {
			refs := []string{}
			if id != "6" {
				refs = append(refs, "K-"+id)
			}
			notes = append(notes, map[string]any{"documentId": id, "finding": "unknown", "applicability": "source context", "limitations": "excerpt only", "references": refs})
		}
		output := mustJSON(map[string]any{"documentAnalyses": notes})
		node := selectedEvidencePhaseNode(CompiledNode{ID: "evidence_screening", PromptVersion: selectedEvidenceVersion, OutputSchema: selectedEvidenceSchema()}, input)
		if err := (tool.JSONSchemaValidator{}).Validate(node.OutputSchema, output); err != nil {
			t.Fatal(err)
		}
		if err := validateSelectedEvidence(output, input); err != nil {
			t.Fatal(err)
		}
		e := AIExecution{ID: fmt.Sprint(batch), WorkflowStepID: detail.Steps[1].ID, Status: "completed", Output: output}
		step := detail.Steps[1]
		step.Input = input
		if err := s.completeSelectedEvidenceBatch(context.Background(), detail, step, e); err != nil {
			t.Fatal(err)
		}
		detail.AIExecutions = append(detail.AIExecutions, e)
		detail.Events = append(detail.Events, repo.event)
	}
	input, err := prepareSelectedEvidence(detail, base)
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Info selectedEvidenceInput `json:"_selectedEvidence"`
	}
	json.Unmarshal(input, &in)
	if in.Info.Phase != "coverage" || len(in.Info.Analyses) != 7 {
		t.Fatal(string(input))
	}
	if err := validateSelectedEvidence(raw(`{"recommendation":"proceed","recommendedReferences":[],"coverage":{"sufficientForClaimedScope":true}}`), input); err == nil {
		t.Fatal("missing document passed coverage")
	}
	changed := mustJSON(map[string]any{"documentIds": docs, "candidates": citations, "researchContext": "changed"})
	input, err = prepareSelectedEvidence(detail, changed)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(input, &in)
	if in.Info.Phase != "batch" {
		t.Fatal("stale analysis reused")
	}
}
