package workflow

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Optional replay of task-scoped read-only captures. No live database/model,
// original code execution or retroactive review/preflight changes.
func TestCapturedTabularAuditGuidancePreservesHistoricalResearchSnapshot(t *testing.T) {
	dir := os.Getenv("SCIAIDE_TABULAR_AUDIT_REPLAY")
	if dir == "" {
		t.Skip("no task-scoped capture supplied")
	}
	files := map[string][]byte{}
	read := func(name string, value any) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = data
		if err := json.Unmarshal(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}), value); err != nil {
			t.Fatal(err)
		}
	}
	// The run row also contains numbers/booleans, so read it as RawMessages.
	var rawRun map[string]json.RawMessage
	read("workflow.json", &rawRun)
	capturedRun := map[string]string{}
	for _, key := range []string{"id", "project_id", "research_task_id", "status", "inputs_json", "compilation_json"} {
		var capturedRunValue string
		if err := json.Unmarshal(rawRun[key], &capturedRunValue); err != nil {
			t.Fatal(key, err)
		}
		capturedRun[key] = capturedRunValue
	}
	var compilation Compilation
	if err := json.Unmarshal([]byte(capturedRun["compilation_json"]), &compilation); err != nil {
		t.Fatal(err)
	}
	detail := RunDetail{Run: Run{ID: capturedRun["id"], ProjectID: capturedRun["project_id"], ResearchTaskID: capturedRun["research_task_id"], Status: RunStatus(capturedRun["status"]), Inputs: raw(capturedRun["inputs_json"]), Compilation: compilation}}
	var rawSteps []map[string]json.RawMessage
	read("steps.json", &rawSteps)
	for _, row := range rawSteps {
		str := func(key string) string {
			t.Helper()
			var value string
			if err := json.Unmarshal(row[key], &value); err != nil {
				t.Fatal(key, err)
			}
			return value
		}
		detail.Steps = append(detail.Steps, Step{ID: str("id"), WorkflowRunID: str("workflow_run_id"), NodeID: str("node_id"), NodeKind: NodeKind(str("node_kind")), Status: StepStatus(str("status")), Input: raw(str("input_json")), InputSHA256: str("input_sha256"), Output: raw(str("output_json"))})
	}
	before := mustJSON(detail)
	nodes := compilationNodeMap(compilation)
	for _, id := range []string{"method_selection", "method_implementation", "result_interpretation", "report_drafting", "independent_review"} {
		step := findStepByNode(detail.Steps, id)
		if step == nil || step.InputSHA256 != hashJSON(step.Input) {
			t.Fatal("capture missing/current input digest mismatch", id)
		}
		prompt := buildAIStagePrompt(detail, *step, nodes[id])
		if !strings.Contains(prompt, computationAuditInstruction) || !strings.Contains(prompt, string(step.Input)) || len(reportExecutionPromptVersion(nodes[id], prompt)) > 128 {
			t.Fatal("new audit cannot execute with exact original contract", id)
		}
		t.Logf("%s: input=%d bytes, newly built prompt=%d bytes", id, len(step.Input), len(prompt))
	}
	preflight := findStepByNode(detail.Steps, "data_preflight")
	if preflight == nil || strings.Contains(string(preflight.Output), "tabularEqualityAudit") || strings.Contains(string(nodes["data_preflight"].Arguments), "tabular_equality_observation") {
		t.Fatal("capture was rewritten to fabricate a new structural observation")
	}
	review := findStepByNode(detail.Steps, "independent_review")
	analysis, err := outputPort(review.Output, "analysis")
	if err != nil || decodeObject(analysis)["approved"] != false || !bytes.Equal(before, mustJSON(detail)) {
		t.Fatal("historical rejection/input/output/route changed", err)
	}
	for name, original := range files {
		if current, err := os.ReadFile(filepath.Join(dir, name)); err != nil || !bytes.Equal(original, current) {
			t.Fatal("capture changed", name, err)
		}
	}
	t.Log("captured rejection and frozen old preflight preserved; next explicit computational execution receives guidance, old missing equality counts remain unknown")
}
