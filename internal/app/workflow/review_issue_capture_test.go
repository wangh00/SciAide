package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCapturedReviewIssueTransaction(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REVIEW_DRIFT_CAPTURE")
	if dir == "" {
		t.Skip("no isolated capture")
	}
	read := func(name string) []byte {
		b, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	var d RunDetail
	if err := json.Unmarshal(read("derived/detail.json"), &d); err != nil {
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

	step := *findStepByNode(d.Steps, "independent_review")
	node := compilationNodeMap(d.Run.Compilation)[step.NodeID]
	var turns []struct {
		Index int    `json:"turn_index"`
		Text  string `json:"draft_text"`
	}
	_ = json.Unmarshal(read("model_turn_journal.json"), &turns)
	for _, turn := range turns {
		value, _, err := normalizeWorkflowAIStageSubmissionForInput(turn.Text, node, step.Input)
		if err != nil {
			t.Logf("TURN %d schema: %v", turn.Index, err)
			continue
		}
		value = canonicalCitationSubmission(value, d, step, node, "d9774918-aac5-4034-a79f-9d79eac3ea71")
		err = validateWorkflowAIStageOutput(node, value, step.Input)
		t.Logf("TURN %d validation: %v", turn.Index, err)
		var basis *ReviewBasisErrors
		if errors.As(err, &basis) {
			if err := os.WriteFile(filepath.Join(dir, "derived", fmt.Sprintf("turn-%d-diagnostic.json", turn.Index)), mustJSON(basis), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "derived", fmt.Sprintf("turn-%d-normalized.json", turn.Index)), value, 0600); err != nil {
			t.Fatal(err)
		}
	}
	candidate := read("derived/issue-merged.json")
	value, _, err := normalizeWorkflowAIStageSubmissionForInput(string(candidate), node, step.Input)
	if err != nil {
		t.Fatal(err)
	}
	value = canonicalCitationSubmission(value, d, step, node, "d9774918-aac5-4034-a79f-9d79eac3ea71")
	if err := validateWorkflowAIStageOutput(node, value, step.Input); err != nil {
		t.Fatal("sparse replay failed frozen validation", err)
	}
	t.Log("sparse missing-issue addition passes complete frozen validation; original second-draft verdict and valid quotes preserved")
	projected, instruction := researchInputSharedProjection(step.Input, true)
	old, oldInstruction := researchInputSharedProjection(step.Input)
	t.Logf("input raw=%d old projection+encoding=%d new projection+encoding=%d", len([]rune(string(step.Input))), len([]rune(string(old)+oldInstruction)), len([]rune(string(projected)+instruction)))
}
