package workflow

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCapturedReviewSameSourceCells(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REVIEW_CELL_CAPTURE")
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
	base := ""
	for _, turn := range turns {
		if turn.Index == 4 {
			base = turn.Text
		}
	}
	value, _, err := normalizeWorkflowAIStageSubmissionForInput(base, node, step.Input)
	if err != nil {
		t.Fatal(err)
	}
	chat := "bb9c2c37-3832-4f8a-83e2-a0735aa8d8a6"
	value = canonicalCitationSubmission(value, d, step, node, chat)
	err = validateWorkflowAIStageOutput(node, value, step.Input)
	var diagnostic *ReviewBasisErrors
	if !errors.As(err, &diagnostic) {
		t.Fatal("capture not reproduced", err)
	}
	t.Log(diagnostic.Error())
	for _, s := range diagnostic.Sources {
		ids := []string{}
		for _, seg := range s.Segments {
			ids = append(ids, seg.ID)
		}
		t.Log(s.Cell, s.Pointer, s.InputSHA256, ids)
	}
	if err := os.WriteFile(filepath.Join(dir, "derived", "basis-diagnostic.json"), mustJSON(diagnostic), 0600); err != nil {
		t.Fatal(err)
	}
	// Agent replay writes its merged result separately, without changing this
	// capture or scientific decisions. The original complete checks run here.
	for _, name := range []string{"merged-turn-5.json", "merged-turn-6.json"} {
		candidate, e := os.ReadFile(filepath.Join(dir, "derived", name))
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			t.Fatal(e)
		}
		candidate, _, e = normalizeWorkflowAIStageSubmissionForInput(string(candidate), node, step.Input)
		if e != nil {
			t.Fatal(name, "merged frozen schema", e)
		}
		candidate = canonicalCitationSubmission(candidate, d, step, node, chat)
		if err := validateWorkflowAIStageOutput(node, candidate, step.Input); err != nil {
			t.Fatal(name, err)
		}
		t.Log(name, "passes complete frozen validation; scientific verdict remains rejected")
	}
}
