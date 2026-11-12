package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/wangh00/SciAide/internal/app/tool"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Offline replay only; never edits or advances the formal task.
func TestCapturedContextRetryValidation(t *testing.T) {
	dir := os.Getenv("SCIAIDE_CONTEXT_RETRY_CAPTURE")
	if dir == "" {
		t.Skip("scoped capture only")
	}
	read := func(n string) []byte {
		b, e := os.ReadFile(filepath.Join(dir, n))
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	node := CompiledNode{ID: "independent_review", OutputSchema: read("schema.json")}
	input := json.RawMessage(read("input.json"))
	var data struct {
		Evidence []tool.CitationRef `json:"evidenceContext"`
	}
	if json.Unmarshal(input, &data) != nil {
		t.Fatal("invalid captured input")
	}
	var journal []struct {
		RunID string `json:"run_id"`
		Draft string `json:"draft_text"`
	}
	if json.Unmarshal(read("journal.json"), &journal) != nil || len(journal) != 1 {
		t.Fatal("invalid journal capture")
	}
	value, _, err := normalizeWorkflowAIStageSubmissionForInput(journal[0].Draft, node, input)
	if err != nil {
		t.Fatal(err)
	}
	value = restoreWorkflowCitationMarkers(value, data.Evidence, journal[0].RunID)
	err = validateWorkflowAIStageOutput(node, value, input)
	if err == nil {
		t.Fatal("expected actual diagnostic")
	}
	t.Logf("diagnostic=%v", err)
	var basis *ReviewBasisErrors
	if !errors.As(err, &basis) {
		t.Fatal("missing basis diagnosis")
	}
	if e := os.WriteFile(filepath.Join(dir, "basis.json"), mustJSON(basis), 0600); e != nil {
		t.Fatal(e)
	}
	var state *ReviewFindingStateErrors
	if !errors.As(err, &state) {
		t.Fatal("missing actual origin-state diagnosis")
	}
	if e := os.WriteFile(filepath.Join(dir, "finding-state.json"), mustJSON(state), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "validation.txt"), []byte(err.Error()), 0600); e != nil {
		t.Fatal(e)
	}
}

func TestCapturedContextRetryGuidance(t *testing.T) {
	dir := os.Getenv("SCIAIDE_CONTEXT_RETRY_CAPTURE")
	if dir == "" {
		t.Skip("scoped capture only")
	}
	read := func(n string) []byte {
		b, e := os.ReadFile(filepath.Join(dir, n))
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	var runs, steps []map[string]json.RawMessage
	if json.Unmarshal(read("workflow-run.json"), &runs) != nil || len(runs) != 1 || json.Unmarshal(read("workflow-steps.json"), &steps) != nil {
		t.Fatal("bad snapshot")
	}
	str := func(v json.RawMessage) string { var s string; _ = json.Unmarshal(v, &s); return s }
	r := runs[0]
	detail := RunDetail{Run: Run{ID: str(r["id"]), ProjectID: str(r["project_id"]), ResearchTaskID: str(r["research_task_id"]), Status: RunRunning, Inputs: raw(str(r["inputs_json"])), InputsSHA256: str(r["inputs_sha256"]), CompilationSHA256: str(r["compilation_sha256"])}}
	_ = json.Unmarshal(r["current_step_ordinal"], &detail.Run.CurrentStep)
	if err := json.Unmarshal([]byte(str(r["compilation_json"])), &detail.Run.Compilation); err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		step := Step{ID: str(s["id"]), WorkflowRunID: str(s["workflow_run_id"]), NodeID: str(s["node_id"]), NodeKind: NodeKind(str(s["node_kind"])), Status: StepStatus(str(s["status"])), Input: raw(str(s["input_json"])), InputSHA256: str(s["input_sha256"]), Output: raw(str(s["output_json"]))}
		_ = json.Unmarshal(s["ordinal"], &step.Ordinal)
		_ = json.Unmarshal(s["attempt"], &step.Attempt)
		if s["completed_at"] != nil && string(s["completed_at"]) != "null" {
			when, _ := time.Parse(time.RFC3339Nano, str(s["completed_at"]))
			step.CompletedAt = &when
		}
		if step.NodeID == "independent_review" {
			step.Status = StepRunning
			detail.Run.CurrentStep = step.Ordinal
		}
		detail.Steps = append(detail.Steps, step)
	}
	repo := &sharingCapturedGuidanceRepository{stageMemoryFixture: stageMemoryFixture{detail: detail}}
	g, found, err := (&RuntimeService{repository: repo}).GuidanceForConversation(context.Background(), "capture")
	if err != nil || !found {
		t.Fatal(err)
	}
	b := mustJSON(map[string]any{"executionSystemContext": g.ExecutionSystemContext, "executionDynamicState": g.ExecutionDynamicState, "executionTaskReadSchema": g.ExecutionTaskReadSchema, "skillDiscovery": g.SkillDiscovery})
	if err := os.WriteFile(filepath.Join(dir, "guidance-built.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}
