package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/apperr"
)

// Replay only an explicitly selected, frozen diagnostic capture. No database,
// model, network, or live task is opened or modified by this test.
func TestCapturedSelectedEvidenceContinuationReason(t *testing.T) {
	path := os.Getenv("SCIAIDE_SELECTED_CONTINUATION_CAPTURE")
	if path == "" {
		t.Skip("no selected evidence continuation capture supplied")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Detail   RunDetail `json:"detail"`
		RawSteps []struct {
			ID     string `json:"id"`
			Input  string `json:"input_json"`
			Output string `json:"output_json"`
		} `json:"rawSteps"`
		RawRunInputs string      `json:"rawRunInputs"`
		PromptInput  string      `json:"promptInput"`
		Execution    AIExecution `json:"execution"`
		Calls        []tool.Call `json:"calls"`
	}
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	d := capture.Detail
	d.Run.Inputs, d.Run.Status = raw(capture.RawRunInputs), RunRunning
	for i := range d.Steps {
		for _, source := range capture.RawSteps {
			if source.ID == d.Steps[i].ID {
				d.Steps[i].Input, d.Steps[i].Output = raw(source.Input), raw(source.Output)
			}
		}
	}
	e := capture.Execution
	var in struct {
		Candidates []tool.CitationRef `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(capture.PromptInput), &in); err != nil {
		t.Fatal(err)
	}
	for i := range in.Candidates {
		r := &in.Candidates[i]
		r.Reference = citation.KnowledgeReference(d.Run.ID, r.IndexVersionID, r.ChunkID, r.QuoteSHA256)
	}
	input := restoreWorkflowCitationMarkers(raw(capture.PromptInput), in.Candidates, e.ChatRunID)
	if hashJSON(input) != e.InputSHA256 {
		t.Fatal("captured original input does not match the frozen execution hash")
	}
	step := findNodeStep(d.Steps, "evidence_screening")
	if step == nil || step.ID != e.WorkflowStepID {
		t.Fatal("capture does not bind the selected evidence stage")
	}
	step.Attempt, step.Status, step.Input, step.InputSHA256 = e.Attempt, StepRunning, input, e.InputSHA256
	var events []RuntimeEvent
	for _, event := range d.Events {
		if event.CreatedAt.Before(e.CreatedAt) {
			events = append(events, event)
		}
	}
	d.Events = events
	node := selectedEvidencePhaseNode(compilationNodeMap(d.Run.Compilation)[step.NodeID], input)
	value, _, err := normalizeWorkflowAIStageSubmissionForInput(e.OutputText, node, input)
	if err != nil {
		t.Fatal("captured corrected submission schema", err)
	}
	calls := &indexedCallsFixture{}
	calls.calls = capture.Calls
	s := &RuntimeService{repository: &submissionValidationRepository{detail: d}, tools: tool.NewService(calls, tool.JSONSchemaValidator{})}
	value, validationInput, reads, err := s.indexedAIStageSubmission(context.Background(), d, *step, node, e, value)
	if err != nil {
		t.Fatal("captured evidence binding", err)
	}
	err = validateWorkflowAIStageOutput(node, value, validationInput)
	var issues *CitationAssessmentErrors
	if !errors.As(err, &issues) || len(issues.Issues) != 3 {
		t.Fatal("captured metadata-only recommendations must retain all three original validation issues", err)
	}
	t.Logf("actual corrected submission: current reads=%d; validation=%v", len(reads), err)
	_, submissionErr := s.ValidateResearchSubmission(context.Background(), d.Run.ConversationID, d.Run.ID, step.ID, e.OutputText)
	var public *apperr.Error
	if !errors.As(submissionErr, &public) || public.Code != "WORKFLOW_AI_SUBMISSION_INVALID" || !errors.As(submissionErr, &issues) || len(issues.Issues) != 3 {
		t.Fatal("newly read evidence must not hide a complete submission's original three semantic errors from the same model run", submissionErr)
	}
}
