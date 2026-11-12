package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Optional replay of one read-only task capture. Never opens the user's DB,
// rewrites historical JSON, executes captured code or requests a real model.
func TestCapturedBoundWineReviewKeepsHistoryAndSeparatesRepair(t *testing.T) {
	dir := os.Getenv("SCIAIDE_BINDING_REPLAY")
	if dir == "" {
		t.Skip("no task capture supplied")
	}
	data, err := os.ReadFile(filepath.Join(dir, "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Run struct {
			ID             string    `json:"id"`
			ProjectID      string    `json:"project_id"`
			ResearchTaskID string    `json:"research_task_id"`
			Purpose        Purpose   `json:"workflow_purpose"`
			Status         RunStatus `json:"status"`
			Inputs         string    `json:"inputs_json"`
			InputsSHA256   string    `json:"inputs_sha256"`
			Compilation    string    `json:"compilation_json"`
			CurrentStep    int       `json:"current_step_ordinal"`
			ErrorCode      string    `json:"error_code"`
		} `json:"run"`
		Steps []struct {
			ID            string     `json:"id"`
			NodeID        string     `json:"node_id"`
			WorkflowRunID string     `json:"workflow_run_id"`
			Kind          NodeKind   `json:"node_kind"`
			Ordinal       int        `json:"ordinal"`
			Attempt       int        `json:"attempt"`
			Status        StepStatus `json:"status"`
			Input         string     `json:"input_json"`
			InputSHA256   string     `json:"input_sha256"`
			Output        string     `json:"output_json"`
			ErrorCode     string     `json:"error_code"`
		} `json:"steps"`
		Executions []struct {
			ID          string `json:"id"`
			StepID      string `json:"workflow_step_id"`
			Attempt     int    `json:"attempt"`
			InputSHA256 string `json:"input_sha256"`
			Status      string `json:"status"`
			Output      string `json:"output_json"`
			OutputSHA   string `json:"output_sha256"`
		} `json:"executions"`
		Events []struct {
			RunID   string `json:"workflow_run_id"`
			Type    string `json:"event_type"`
			Payload string `json:"payload_json"`
		} `json:"events"`
	}
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	detail := RunDetail{Run: Run{ID: capture.Run.ID, ProjectID: capture.Run.ProjectID, ResearchTaskID: capture.Run.ResearchTaskID,
		WorkflowPurpose: capture.Run.Purpose, Status: capture.Run.Status, Inputs: json.RawMessage(capture.Run.Inputs), InputsSHA256: capture.Run.InputsSHA256,
		CurrentStep: capture.Run.CurrentStep, ErrorCode: capture.Run.ErrorCode}}
	if err := json.Unmarshal([]byte(capture.Run.Compilation), &detail.Run.Compilation); err != nil {
		t.Fatal(err)
	}
	for _, s := range capture.Steps {
		detail.Steps = append(detail.Steps, Step{ID: s.ID, WorkflowRunID: s.WorkflowRunID, NodeID: s.NodeID, NodeKind: s.Kind,
			Ordinal: s.Ordinal, Attempt: s.Attempt, Status: s.Status, Input: json.RawMessage(s.Input), InputSHA256: s.InputSHA256, Output: json.RawMessage(s.Output), ErrorCode: s.ErrorCode})
	}
	for _, e := range capture.Executions {
		detail.AIExecutions = append(detail.AIExecutions, AIExecution{ID: e.ID, WorkflowStepID: e.StepID, Attempt: e.Attempt,
			InputSHA256: e.InputSHA256, Status: e.Status, Output: json.RawMessage(e.Output), OutputSHA256: e.OutputSHA})
	}
	for _, e := range capture.Events {
		detail.Events = append(detail.Events, RuntimeEvent{WorkflowRunID: e.RunID, Type: e.Type, Payload: json.RawMessage(e.Payload)})
	}
	before := string(mustJSON(detail))
	binding := currentResearchDataBinding(detail)
	if binding == nil || binding.Source != "workflow_start" || len(binding.Files) != 1 || binding.Files[0].Name != "wine-red-initial-1000.csv" || binding.Files[0].SHA256 != "9eb91365b12daff50dcfbc74fe8bace1caf513d990e1639318c328e22d4a8094" {
		t.Fatalf("adopted file missing: %+v", binding)
	}
	implementation := findStepByNode(detail.Steps, "method_implementation")
	calculation := findStepByNode(detail.Steps, "python_analysis")
	if implementation == nil || calculation == nil {
		t.Fatal("missing actual implementation/calculation")
	}
	analysis, err := outputPort(implementation.Output, "analysis")
	if err != nil {
		t.Fatal(err)
	}
	var old struct {
		Input struct {
			Confirmed bool `json:"binding_confirmed"`
			Required  bool `json:"binding_confirmation_required"`
		} `json:"analysisInput"`
	}
	if json.Unmarshal(analysis, &old) != nil || old.Input.Confirmed || !old.Input.Required {
		t.Fatal("captured erroneous consent not preserved")
	}
	var computed struct {
		Structured struct {
			Value struct {
				Status  string `json:"status"`
				Started bool   `json:"calculation_started"`
			} `json:"value"`
		} `json:"structured"`
	}
	if json.Unmarshal(calculation.Output, &computed) != nil || computed.Structured.Value.Status != "blocked_for_binding_confirmation" || computed.Structured.Value.Started {
		t.Fatal("capture does not describe the actual skipped calculation")
	}
	node := compilationNodeMap(detail.Run.Compilation)[implementation.NodeID]
	prompt := buildAIStagePrompt(detail, *implementation, node)
	if !strings.Contains(prompt, researchDataBindingInstruction) || !strings.Contains(prompt, "<current_data_binding>") || !strings.Contains(prompt, "<stage_input>\n"+string(implementation.Input)+"\n</stage_input>") || !strings.Contains(string(detail.Run.Inputs), "本次仅准备候选研究路线") {
		t.Fatal("legacy execution loses current binding or immutable original context")
	}
	recommendation := researchRevisionRecommendation(detail)
	if recommendation == nil || recommendation.Status != "needs_input" || len(recommendation.RequiredInputs) != 5 || len(recommendation.RepairableCorrections) != 1 || recommendation.PendingStartLabel == "" || recommendation.NodeID != "" {
		t.Fatalf("mixed correction work lost or silently authorized: %+v", recommendation)
	}
	gate := currentStep(detail)
	_, review, err := currentRejectedReview(detail, *gate)
	if err != nil || !strings.Contains(string(review), `"approved":false`) {
		t.Fatal("old rejection was changed or no longer verifiable", err)
	}
	if string(mustJSON(detail)) != before {
		t.Fatal("read-only replay modified history")
	}
	t.Log("one formally bound CSV; old false consent and no calculation preserved; 1 AI correction and 5 pending claims retained; no automatic retry or approval")
}
