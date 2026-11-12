package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

// Only frozen, task-scoped captures are read. Failed real submissions remain
// rejected; this replays diagnostics and scheduling, not scientific acceptance.
func TestCapturedCitationAssessmentRepairExposesAllDefects(t *testing.T) {
	dir := os.Getenv("SCIAIDE_ASSESSMENT_REPLAY_DIR")
	if dir == "" {
		t.Skip("no frozen citation assessment capture supplied")
	}
	read := func(name string, dst any) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || json.Unmarshal(data, dst) != nil {
			t.Fatal("invalid selected capture", name, err)
		}
	}
	var detail RunDetail
	read("workflow-detail-original.json", &detail)
	// json.Marshal of a surrounding capture may HTML-escape embedded RawMessage
	// bytes. Restore the exact persisted JSON columns, never recalculate stored
	// hashes to accommodate the presentation serialization.
	var rawSteps []struct {
		ID     string `json:"id"`
		Input  string `json:"input_json"`
		Output string `json:"output_json"`
	}
	read("steps-original.json", &rawSteps)
	for i := range detail.Steps {
		for _, source := range rawSteps {
			if source.ID == detail.Steps[i].ID {
				detail.Steps[i].Input, detail.Steps[i].Output = raw(source.Input), raw(source.Output)
			}
		}
	}
	var rawExecutions []struct {
		ID     string `json:"id"`
		Output string `json:"output_json"`
	}
	read("executions-original.json", &rawExecutions)
	for i := range detail.AIExecutions {
		for _, source := range rawExecutions {
			if source.ID == detail.AIExecutions[i].ID {
				detail.AIExecutions[i].Output = raw(source.Output)
				if detail.AIExecutions[i].OutputSHA256 != "" && hashJSON(detail.AIExecutions[i].Output) != detail.AIExecutions[i].OutputSHA256 {
					t.Fatal("persisted output does not match its original recorded hash", source.ID)
				}
			}
		}
	}
	step := findNodeStep(detail.Steps, "evidence_screening")
	if step == nil || step.Status != StepFailed || step.Attempt != 10 {
		t.Fatal("capture does not contain the reported coverage failure")
	}
	node := selectedEvidencePhaseNode(compilationNodeMap(detail.Run.Compilation)[step.NodeID], step.Input)
	var execution AIExecution
	for _, e := range detail.AIExecutions {
		if e.WorkflowStepID == step.ID && e.Attempt == step.Attempt {
			execution = e
		}
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
	read("tools-original.json", &records)
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
	repo := &submissionValidationRepository{detail: detail}
	service := &RuntimeService{repository: repo, tools: tool.NewService(calls, tool.JSONSchemaValidator{})}
	var turns []struct {
		Index int    `json:"turn_index"`
		Text  string `json:"draft_text"`
	}
	read("model-turns-attempt-10-original.json", &turns)
	before := mustJSON(detail)
	for _, turn := range turns {
		if turn.Index != 2 && turn.Index != 4 {
			continue
		}
		value, _, err := normalizeWorkflowAIStageSubmissionForInput(turn.Text, node, step.Input)
		if err != nil {
			t.Fatal("real response failed schema before its reported business issue", err)
		}
		value, validationInput, _, err := service.indexedAIStageSubmission(context.Background(), detail, *step, node, execution, value)
		if err != nil {
			t.Fatal("real evidence source binding failed", err)
		}
		err = validateSelectedEvidence(value, validationInput)
		var issues *CitationAssessmentErrors
		if !errors.As(err, &issues) || len(issues.Issues) != 4 {
			t.Fatal("capture no longer reports all four missing source assessments", err)
		}
		if turn.Index == 2 && len(issues.QuoteIssues) != 2 {
			t.Fatal("first response lost its simultaneous two quote defects", err)
		}
		refs, err := service.workflowAICitationSeeds(context.Background(), detail, *step)
		if err != nil {
			t.Fatal(err)
		}
		visible := modelVisibleAssessmentErrors(issues, refs, execution.ChatRunID)
		for _, marker := range []string{"[K-98099926239C]", "[K-4DCEA669B4B2]", "[K-4232915D7D9C]", "[K-512A6D9F2280]"} {
			if !strings.Contains(visible.Error(), marker) {
				t.Fatal("reported missing marker is absent from same-run repair", marker, visible)
			}
		}
		t.Logf("real turn %d still rejected: all %d missing assessments and %d quote defects reported together", turn.Index, len(issues.Issues), len(issues.QuoteIssues))
	}
	// Explicit retry with unchanged sources retains the accepted latest batches;
	// it must not schedule the 34 document analyses again for this content error.
	var retryDetail RunDetail
	if err := json.Unmarshal(mustJSON(detail), &retryDetail); err != nil {
		t.Fatal(err)
	}
	// Restore exact source outputs after the surrounding JSON round trip.
	for i := range retryDetail.Steps {
		retryDetail.Steps[i].Input, retryDetail.Steps[i].Output = cloneRaw(detail.Steps[i].Input), cloneRaw(detail.Steps[i].Output)
	}
	retryStep := findNodeStep(retryDetail.Steps, "evidence_screening")
	retryStep.Status, retryStep.Input, retryStep.InputSHA256 = StepQueued, raw(`{}`), ""
	retryDetail.Run.Status = RunQueued
	base, err := bindNodeInput(retryDetail, compilationNodeMap(retryDetail.Run.Compilation)[retryStep.NodeID])
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := prepareSelectedEvidence(retryDetail, bindIndexedEvidence(retryDetail, node, base))
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Key  string                `json:"sourceKey"`
		Info selectedEvidenceInput `json:"_selectedEvidence"`
	}
	_ = json.Unmarshal(fresh, &in)
	if in.Info.Phase != "coverage" || in.Info.Direct || len(in.Info.Analyses) != 34 || in.Key != decodeObject(step.Input)["sourceKey"] {
		t.Fatal("retry repeated accepted batch work or lost current source identity")
	}
	if !rawJSONEqual(before, mustJSON(detail)) {
		t.Fatal("replay altered the frozen real task")
	}
	// A rejected quotation patch does not constitute a valid complete result.
	if _, _, err := normalizeWorkflowAIStageSubmissionForInput(turns[2].Text, node, step.Input); err == nil {
		t.Fatal("quotation-only repair was mistaken for a complete result")
	}
}
