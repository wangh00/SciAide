package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Opt-in replay of frozen workflow state. The capture stays outside source
// control; retry is simulated in memory, never in the user's database.
func TestCapturedEvidenceCoverageRetryReusesCheckpoints(t *testing.T) {
	dir := os.Getenv("SCIAIDE_PROVIDER_REPLAY_DIR")
	if dir == "" {
		t.Skip("no frozen workflow supplied")
	}
	read := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var detail RunDetail
	if err := json.Unmarshal(read("workflow-detail.json"), &detail); err != nil {
		t.Fatal(err)
	}
	step := findStepByNode(detail.Steps, "evidence_screening")
	if step == nil || step.Status != StepFailed {
		t.Fatal("capture must contain failed evidence coverage")
	}
	var expected struct {
		Key      string                `json:"sourceKey"`
		Revision json.RawMessage       `json:"_reviewRevision"`
		Info     selectedEvidenceInput `json:"_selectedEvidence"`
	}
	if err := json.Unmarshal(read("input.json"), &expected); err != nil || expected.Info.Phase != "coverage" || expected.Info.Direct || len(expected.Info.Analyses) == 0 || len(expected.Revision) == 0 {
		t.Fatal("capture is not checkpoint-backed review coverage", err)
	}
	// Equivalent to reloading state after restart then explicitly retrying the
	// failed stage; completed stages/executions and attempts are not reset.
	step.Status = StepQueued
	step.Input, step.Output, step.InputSHA256 = raw(`{}`), raw(`{}`), ""
	step.ErrorCode, step.ErrorMessage, step.ToolCallID, step.IdempotencyKey = "", "", "", ""
	detail.Run.Status = RunQueued
	node := compilationNodeMap(detail.Run.Compilation)[step.NodeID]
	bound, err := bindNodeInput(detail, node)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareSelectedEvidence(detail, bound)
	if err != nil {
		t.Fatal(err)
	}
	var actual struct {
		Key      string                `json:"sourceKey"`
		Revision json.RawMessage       `json:"_reviewRevision"`
		Info     selectedEvidenceInput `json:"_selectedEvidence"`
	}
	if err := json.Unmarshal(prepared, &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Key != expected.Key || actual.Info.Phase != "coverage" || actual.Info.Direct || hashJSON(actual.Revision) != hashJSON(expected.Revision) || hashJSON(mustJSON(actual.Info)) != hashJSON(mustJSON(expected.Info)) {
		t.Fatalf("retry lost review identity or repeated completed reading: phase=%s, analyses=%d, key=%s (expected %s)", actual.Info.Phase, len(actual.Info.Analyses), actual.Key, expected.Key)
	}
	t.Logf("explicit retry after state reload stays coverage; %d document analyses and frozen review are unchanged; sourceKey=%s; no DB write/model call", len(actual.Info.Analyses), actual.Key)
}

func TestEvidenceCoverageRetriesKeepOnlyMatchingCompletedCheckpoints(t *testing.T) {
	input := raw(`{"documentIds":["doc-a","doc-b"],"candidates":[]}`)
	key := hashJSON(input)
	output := raw(`{"documentAnalyses":[{"documentId":"doc-a","finding":"accepted A","limitations":"bounded","references":[]},{"documentId":"doc-b","finding":"accepted B","limitations":"bounded","references":[]}]}`)
	for _, status := range []StepStatus{StepFailed, StepCancelled, StepInterrupted} {
		t.Run(string(status), func(t *testing.T) {
			detail := RunDetail{
				Steps: []Step{{ID: "evidence", NodeID: "evidence_screening", Status: status, Attempt: 2}},
				AIExecutions: []AIExecution{
					{ID: "batch", WorkflowStepID: "evidence", Attempt: 1, Status: "completed", Output: output},
					{ID: "coverage", WorkflowStepID: "evidence", Attempt: 2, Status: "failed", Output: raw(`{"documentAnalyses":[{"documentId":"doc-a","finding":"unaccepted draft"}]}`)},
				},
				Events: []RuntimeEvent{{Type: "workflow.selected_evidence_batch", Payload: mustJSON(map[string]any{"executionId": "batch", "sourceKey": key})}},
			}
			// Round-trip the persisted state before an explicit retry.
			if err := json.Unmarshal(mustJSON(detail), &detail); err != nil {
				t.Fatal(err)
			}
			detail.Steps[0].Status = StepQueued
			prepared, err := prepareSelectedEvidence(detail, input)
			if err != nil {
				t.Fatal(err)
			}
			var actual struct {
				Info selectedEvidenceInput `json:"_selectedEvidence"`
			}
			json.Unmarshal(prepared, &actual)
			var accepted struct {
				Analyses []json.RawMessage `json:"documentAnalyses"`
			}
			json.Unmarshal(output, &accepted)
			if actual.Info.Phase != "coverage" || actual.Info.Direct || hashJSON(mustJSON(actual.Info.Analyses)) != hashJSON(mustJSON(accepted.Analyses)) {
				t.Fatal("failed draft reused or accepted batches lost")
			}
			for _, changed := range []json.RawMessage{
				raw(`{"documentIds":["doc-a","doc-b"],"candidates":[],"_reviewRevision":{"generation":"new"}}`),
				raw(`{"documentIds":["doc-a","doc-c"],"candidates":[]}`),
			} {
				prepared, err = prepareSelectedEvidence(detail, changed)
				if err != nil {
					t.Fatal(err)
				}
				json.Unmarshal(prepared, &actual)
				if !actual.Info.Direct || len(actual.Info.Analyses) != 0 {
					t.Fatal("different evidence/review identity reused old analysis")
				}
			}
		})
	}
}
