package workflow

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Offline capture only; the derived quote patch comes from the agent replay.
func TestCapturedCitationShapesFullValidationAndBatchResume(t *testing.T) {
	dir := os.Getenv("SCIAIDE_MIXED_CITATION_CAPTURE")
	if dir == "" {
		t.Skip("no isolated capture")
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var d RunDetail
	if err := json.Unmarshal(read("detail.json"), &d); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"workflow_steps.json", "workflow_ai_executions.json", "workflow_events.json"} {
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(read(name), &rows); err != nil {
			t.Fatal(err)
		}
		str := func(v json.RawMessage) string { var s string; _ = json.Unmarshal(v, &s); return s }
		for _, r := range rows {
			id := str(r["id"])
			for i := range d.Steps {
				if d.Steps[i].ID == id {
					d.Steps[i].Input = raw(str(r["input_json"]))
					d.Steps[i].Output = raw(str(r["output_json"]))
				}
			}
			for i := range d.AIExecutions {
				if d.AIExecutions[i].ID == id {
					d.AIExecutions[i].Output = raw(str(r["output_json"]))
				}
			}
			for i := range d.Events {
				if d.Events[i].ID == id {
					d.Events[i].Payload = raw(str(r["payload_json"]))
				}
			}
		}
	}
	before := mustJSON(d)
	step := *findStepByNode(d.Steps, "evidence_screening")
	node := compilationNodeMap(d.Run.Compilation)[step.NodeID]
	var execution AIExecution
	for _, e := range d.AIExecutions {
		if e.WorkflowStepID == step.ID && e.Attempt == step.Attempt {
			execution = e
		}
	}
	var turns []struct {
		Text string `json:"draft_text"`
	}
	_ = json.Unmarshal(read("turns.json"), &turns)
	phase := selectedEvidencePhaseNode(node, step.Input)
	bad, _, err := normalizeWorkflowAIStageSubmissionForInput(turns[0].Text, phase, step.Input)
	if err != nil {
		t.Fatal(err)
	}
	bad = canonicalCitationSubmission(bad, d, step, phase, execution.ChatRunID)
	err = validateWorkflowAIStageOutput(phase, bad, step.Input)
	var quote *CitationQuoteError
	if !errors.As(err, &quote) {
		t.Fatal("real first invalid quote not reproduced", err)
	}
	good := canonicalCitationSubmission(read("derived/merged.json"), d, step, phase, execution.ChatRunID)
	if err := validateWorkflowAIStageOutput(phase, good, step.Input); err != nil {
		t.Fatal("complete frozen business validation still failed", err)
	}
	retry := d
	retry.Steps = append([]Step{}, d.Steps...)
	current := findStepByNode(retry.Steps, step.NodeID)
	current.Status = StepQueued
	current.Input = raw(`{}`)
	current.InputSHA256 = ""
	bound, err := bindNodeInput(retry, node)
	if err != nil {
		t.Fatal(err)
	}
	bound, err = prepareSelectedEvidence(retry, bindIndexedEvidence(retry, node, bound))
	if err != nil {
		t.Fatal(err)
	}
	var resumed struct {
		Info selectedEvidenceInput `json:"_selectedEvidence"`
	}
	_ = json.Unmarshal(bound, &resumed)
	if resumed.Info.Phase != "coverage" || len(resumed.Info.Analyses) != 24 {
		t.Fatalf("retry redoes batches: phase=%s analyses=%d", resumed.Info.Phase, len(resumed.Info.Analyses))
	}
	if !reflect.DeepEqual(before, mustJSON(d)) {
		t.Fatal("captured state mutated")
	}
	t.Log("first quote rejected; derived single-cell correction passes full frozen validation; retry resumes coverage with 24 saved document analyses")
}
