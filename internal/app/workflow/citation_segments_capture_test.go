package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/tool"
)

// The real failed responses must remain rejected. A separately constructed
// fixture correction is not a rewrite or acceptance of the user's real task.
func TestCapturedRepeatedQuoteRepairKeepsFailureAndSourceSelection(t *testing.T) {
	dir := os.Getenv("SCIAIDE_QUOTE_SELECTION_REPLAY_DIR")
	if dir == "" {
		t.Skip("no frozen repeated quotation capture supplied")
	}
	read := func(name string, dst any) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || json.Unmarshal(data, dst) != nil {
			t.Fatal("invalid task-scoped capture", name, err)
		}
	}
	var detail RunDetail
	read("workflow-detail-original.json", &detail)
	var rawRun struct {
		Inputs string `json:"inputs_json"`
	}
	read("workflow-original.json", &rawRun)
	detail.Run.Inputs = raw(rawRun.Inputs)
	var rawSteps []struct {
		ID     string `json:"id"`
		Input  string `json:"input_json"`
		Output string `json:"output_json"`
	}
	read("steps-original.json", &rawSteps)
	for i := range detail.Steps {
		for _, s := range rawSteps {
			if s.ID == detail.Steps[i].ID {
				detail.Steps[i].Input, detail.Steps[i].Output = raw(s.Input), raw(s.Output)
			}
		}
	}
	var rawExecutions []struct {
		ID     string `json:"id"`
		Output string `json:"output_json"`
	}
	read("executions-original.json", &rawExecutions)
	for i := range detail.AIExecutions {
		for _, e := range rawExecutions {
			if e.ID == detail.AIExecutions[i].ID {
				detail.AIExecutions[i].Output = raw(e.Output)
				if detail.AIExecutions[i].OutputSHA256 != "" && hashJSON(detail.AIExecutions[i].Output) != detail.AIExecutions[i].OutputSHA256 {
					t.Fatal("original successful execution output changed")
				}
			}
		}
	}
	step := findNodeStep(detail.Steps, "evidence_screening")
	if step == nil || step.Status != StepFailed || step.Attempt != 11 {
		t.Fatal("capture is not the reported latest failure")
	}
	var records []struct {
		ID            string            `json:"id"`
		RunID         string            `json:"run_id"`
		WorkflowRunID string            `json:"workflow_run_id"`
		Name          string            `json:"tool_name"`
		Status        tool.CallStatus   `json:"status"`
		ResultStatus  tool.ResultStatus `json:"result_status"`
		Structured    string            `json:"structured_json"`
		Citations     string            `json:"citations_json"`
	}
	read("all-tools-original.json", &records)
	calls := &indexedCallsFixture{}
	for _, record := range records {
		runID := record.RunID
		if runID == "" {
			runID = record.WorkflowRunID
		}
		call := tool.Call{ID: record.ID, RunID: runID, ToolName: record.Name, Status: record.Status}
		if record.ResultStatus != "" {
			call.Result = &tool.Result{Status: record.ResultStatus, Structured: raw(record.Structured)}
			if err := json.Unmarshal([]byte(record.Citations), &call.Result.Citations); err != nil {
				t.Fatal(err)
			}
		}
		calls.calls = append(calls.calls, call)
	}
	// Recreate only the pre-commit active labels in an isolated in-memory copy;
	// all captured inputs, contracts, outputs and hashes stay unchanged.
	active := detail
	active.Steps = append([]Step{}, detail.Steps...)
	active.Run.Status = RunRunning
	findNodeStep(active.Steps, step.NodeID).Status = StepRunning
	service := &RuntimeService{repository: &submissionValidationRepository{detail: active}, tools: tool.NewService(calls, tool.JSONSchemaValidator{})}
	var turns []struct {
		Index int    `json:"turn_index"`
		Text  string `json:"draft_text"`
	}
	read("model-turns-original.json", &turns)
	before := mustJSON(detail)
	_, err := service.ValidateResearchSubmission(context.Background(), detail.Run.ConversationID, detail.Run.ID, step.ID, turns[0].Text)
	var quote *CitationQuoteError
	if !errors.As(err, &quote) || quote.Reference != "[K-86E3EDB88DC3]" {
		t.Fatal("real first response no longer reproduces exact quote defect", err)
	}
	var mixed *CitationAssessmentErrors
	var quotes *CitationQuoteErrors
	if errors.As(err, &mixed) || errors.As(err, &quotes) {
		t.Fatal("previous missing assessment problem unexpectedly persists", err)
	}
	for _, turn := range turns[1:] {
		var patch struct {
			Items []struct {
				Reference string `json:"reference"`
				Quote     string `json:"supportingQuote"`
			} `json:"citationAssessmentsPatch"`
		}
		if json.Unmarshal([]byte(turn.Text), &patch) != nil || len(patch.Items) != 1 || patch.Items[0].Reference != quote.Reference || patch.Items[0].Quote != quote.SupportingQuote {
			t.Fatal("captured corrections are not the identical rejected quotation")
		}
	}
	first := "All participants were instructed to maintain a diet"
	last := "The IHTG content was reduced"
	start, end := strings.Index(quote.CandidateQuote, first), strings.Index(quote.CandidateQuote, last)
	if start < 0 || end <= start || utf8.RuneCountInString(quote.CandidateQuote[start:]) != 770 {
		t.Fatal("captured two passages no longer require 770 continuous characters")
	}
	segments := CitationQuoteSourceSegments(quote.Reference, quote.CandidateQuote)
	joined := ""
	for _, segment := range segments {
		joined += segment.Text
	}
	if joined != quote.CandidateQuote {
		t.Fatal("source selection lost or synthesized source characters")
	}
	// An explicit, test-authored scientific narrowing uses the full result.
	// The host never picks a shorter quote while retaining the broader reason.
	node := selectedEvidencePhaseNode(compilationNodeMap(detail.Run.Compilation)[step.NodeID], step.Input)
	base, _, err := normalizeWorkflowAIStageSubmissionForInput(turns[0].Text, node, step.Input)
	if err != nil {
		t.Fatal(err)
	}
	out := decodeObject(base)
	found := false
	for _, item := range out["citationAssessments"].([]any) {
		a := item.(map[string]any)
		if a["reference"] == quote.Reference {
			a["reason"] = "Replay fixture: the quoted sentence reports the six-month IHTG result only; calorie prescription is not established by this selected sentence."
			a["supportingQuote"] = quote.CandidateQuote[end:]
			found = true
		}
	}
	if !found {
		t.Fatal("missing captured assessment")
	}
	if _, err := service.ValidateResearchSubmission(context.Background(), detail.Run.ConversationID, detail.Run.ID, step.ID, string(mustJSON(out))); err != nil {
		t.Fatal("explicit fixture full correction not accepted by unchanged live rules", err)
	}
	// Original retry still starts at coverage and retains all accepted batches.
	retry := detail
	retry.Steps = append([]Step{}, detail.Steps...)
	retryStep := findNodeStep(retry.Steps, step.NodeID)
	retryStep.Status, retryStep.Input, retryStep.InputSHA256 = StepQueued, raw(`{}`), ""
	baseInput, err := bindNodeInput(retry, compilationNodeMap(retry.Run.Compilation)[step.NodeID])
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := prepareSelectedEvidence(retry, bindIndexedEvidence(retry, node, baseInput))
	if err != nil {
		t.Fatal(err)
	}
	var restored struct {
		Info selectedEvidenceInput `json:"_selectedEvidence"`
	}
	_ = json.Unmarshal(fresh, &restored)
	if restored.Info.Phase != "coverage" || len(restored.Info.Analyses) != 34 || !rawJSONEqual(before, mustJSON(detail)) {
		t.Fatal("read-only replay changed snapshot or repeated accepted batches")
	}
	t.Log("original submission still rejected; two identical repairs reproduced; full span 770 exceeds frozen 700; separately authored full correction accepted; retry retains 34 analyses, no live model or formal DB write")
}
