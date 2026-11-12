package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Optional, task-scoped capture replay. No database writes or model calls.
func TestCapturedSuccessfulReviewAvoidsPointerOnlyCorrection(t *testing.T) {
	dir := os.Getenv("SCIAIDE_EFFICIENCY_REPLAY")
	if dir == "" {
		t.Skip("no task capture supplied")
	}
	read := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var capture struct {
		Run struct {
			ID          string `json:"id"`
			ProjectID   string `json:"project_id"`
			Compilation string `json:"compilation_json"`
			Inputs      string `json:"inputs_json"`
		} `json:"run"`
		Steps []struct {
			ID            string     `json:"id"`
			NodeID        string     `json:"node_id"`
			WorkflowRunID string     `json:"workflow_run_id"`
			Ordinal       int        `json:"ordinal"`
			Attempt       int        `json:"attempt"`
			Status        StepStatus `json:"status"`
			Input         string     `json:"input_json"`
			InputSHA256   string     `json:"input_sha256"`
			Output        string     `json:"output_json"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(read("snapshot.json"), &capture); err != nil {
		t.Fatal(err)
	}
	detail := RunDetail{Run: Run{ID: capture.Run.ID, ProjectID: capture.Run.ProjectID, Inputs: json.RawMessage(capture.Run.Inputs)}}
	if err := json.Unmarshal([]byte(capture.Run.Compilation), &detail.Run.Compilation); err != nil {
		t.Fatal(err)
	}
	var reviewStep, implementationStep Step
	for _, recorded := range capture.Steps {
		step := Step{ID: recorded.ID, WorkflowRunID: recorded.WorkflowRunID, NodeID: recorded.NodeID, Ordinal: recorded.Ordinal, Attempt: recorded.Attempt, Status: recorded.Status, Input: json.RawMessage(recorded.Input), InputSHA256: recorded.InputSHA256, Output: json.RawMessage(recorded.Output)}
		detail.Steps = append(detail.Steps, step)
		if step.NodeID == "independent_review" {
			reviewStep = step
		}
		if step.NodeID == "method_implementation" {
			implementationStep = step
		}
	}
	if reviewStep.ID == "" || implementationStep.ID == "" {
		t.Fatal("capture lacks review/implementation")
	}
	nodes := compilationNodeMap(detail.Run.Compilation)
	reviewNode := nodes[reviewStep.NodeID]
	original := read("review-first-response.json")
	if err := validateWorkflowAIStageOutput(reviewNode, json.RawMessage(original), reviewStep.Input); err == nil || !strings.Contains(err.Error(), "/computedResults/") {
		t.Fatal("actual missing-value failure was not reproduced", err)
	}
	before := string(mustJSON(detail))
	normalized, changes, err := normalizeWorkflowAIStageSubmissionForInput(string(original), reviewNode, reviewStep.Input)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWorkflowAIStageOutput(reviewNode, normalized, reviewStep.Input); err != nil {
		t.Fatal("actual first candidate still needs a model correction", err)
	}
	aliases := 0
	for _, change := range changes {
		if change.Rule == "frozen_kernel_value_path" {
			aliases++
		}
	}
	if aliases != 8 {
		t.Fatal("expected exactly eight observed pointer aliases", aliases, changes)
	}
	final, err := outputPort(reviewStep.Output, "analysis")
	if err != nil || !rawJSONEqual(normalized, final) {
		t.Fatal("replay changed the persisted scientific review", err)
	}
	again, changes, err := normalizeWorkflowAIStageSubmissionForInput(string(normalized), reviewNode, reviewStep.Input)
	if err != nil || len(changes) != 0 || !rawJSONEqual(again, normalized) {
		t.Fatal("real replay not idempotent", err)
	}
	bad := decodeObject(original)
	bases := bad["reviewFindings"].([]any)[0].(map[string]any)["basis"].([]any)
	bases[0].(map[string]any)["quote"] = "FORGED CURRENT EVIDENCE"
	result, _, err := normalizeWorkflowAIStageSubmissionForInput(string(mustJSON(bad)), reviewNode, reviewStep.Input)
	if err != nil || validateWorkflowAIStageOutput(reviewNode, result, reviewStep.Input) == nil {
		t.Fatal("forged real evidence accepted or erased", err)
	}
	prompt := buildAIStagePrompt(detail, implementationStep, nodes[implementationStep.NodeID])
	for _, want := range []string{"scipy==1.18.1", "numpy==2.5.3", "<recorded_python_environment>", "dependencies lists the necessary direct", "<stage_input>\n" + string(implementationStep.Input) + "\n</stage_input>"} {
		if !strings.Contains(prompt, want) {
			t.Fatal("actual environment/input not supplied", want)
		}
	}
	if string(mustJSON(detail)) != before {
		t.Fatal("capture mutated")
	}
	t.Logf("actual first review accepted with %d exact pointer changes; identical to final review; frozen capture unchanged; recorded SciPy/NumPy supplied", aliases)
}
