package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

// Opt-in replay of task-scoped read-only captures. Never opens a database,
// launches a model or changes the user's task. Raw captures remain untracked.
func TestCapturedIndexedEvidenceLongRunningSynthesis(t *testing.T) {
	dir := os.Getenv("SCIAIDE_CONVERGENCE_REPLAY")
	if dir == "" {
		t.Skip("no scoped capture supplied")
	}
	data, err := os.ReadFile(filepath.Join(dir, "capture.json"))
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Run                                       map[string]any
		Steps, Events, Executions, Calls, Results []map[string]any
	}
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	str := func(r map[string]any, k string) string { v, _ := r[k].(string); return v }
	num := func(r map[string]any, k string) int { v, _ := r[k].(float64); return int(v) }
	d := RunDetail{Run: Run{ID: str(capture.Run, "id"), ProjectID: str(capture.Run, "project_id"), Inputs: raw(str(capture.Run, "inputs_json")), ResearchTaskID: str(capture.Run, "research_task_id")}}
	if err := json.Unmarshal([]byte(str(capture.Run, "compilation_json")), &d.Run.Compilation); err != nil {
		t.Fatal(err)
	}
	for _, r := range capture.Steps {
		d.Steps = append(d.Steps, Step{ID: str(r, "id"), NodeID: str(r, "node_id"), Ordinal: num(r, "ordinal"), Attempt: num(r, "attempt"), Status: StepStatus(str(r, "status")), Input: raw(str(r, "input_json")), InputSHA256: str(r, "input_sha256"), Output: raw(str(r, "output_json"))})
	}
	for _, r := range capture.Executions {
		d.AIExecutions = append(d.AIExecutions, AIExecution{ID: str(r, "id"), WorkflowStepID: str(r, "workflow_step_id"), Attempt: num(r, "attempt"), ChatRunID: str(r, "chat_run_id"), Status: str(r, "status"), InputSHA256: str(r, "input_sha256"), Output: raw(str(r, "output_json")), OutputSHA256: str(r, "output_sha256"), OutputText: str(r, "output_text")})
	}
	results := map[string]map[string]any{}
	for _, r := range capture.Results {
		results[str(r, "tool_call_id")] = r
	}
	calls := &indexedCallsFixture{}
	for _, r := range capture.Calls {
		id := str(r, "run_id")
		if id == "" {
			id = str(r, "workflow_run_id")
		}
		call := tool.Call{ID: str(r, "id"), RunID: id, ToolName: str(r, "tool_name"), Status: tool.CallStatus(str(r, "status"))}
		if result, ok := results[call.ID]; ok {
			call.Result = &tool.Result{Status: tool.ResultStatus(str(result, "status")), Structured: raw(str(result, "structured_json"))}
			if err := json.Unmarshal([]byte(str(result, "citations_json")), &call.Result.Citations); err != nil {
				t.Fatal(err)
			}
		}
		calls.calls = append(calls.calls, call)
	}
	for _, attempt := range []int{18, 20, 22} {
		t.Run(fmt.Sprintf("attempt_%d", attempt), func(t *testing.T) {
			replay := d
			replay.Events = nil
			replay.Steps = append([]Step{}, d.Steps...)
			var execution AIExecution
			var input json.RawMessage
			for _, r := range capture.Executions {
				if num(r, "attempt") != attempt || str(r, "workflow_step_id") != findStepByNode(d.Steps, "evidence_screening").ID {
					continue
				}
				for _, e := range d.AIExecutions {
					if e.ID == str(r, "id") {
						execution = e
					}
				}
				prompt := str(r, "prompt_text")
				input = raw(strings.TrimSpace(strings.Split(strings.Split(prompt, "<stage_input>")[1], "</stage_input>")[0]))
			}
			if execution.ID == "" {
				t.Fatal("capture lacks attempt")
			}
			for _, r := range capture.Events {
				if str(r, "created_at") >= func() string {
					for _, e := range capture.Executions {
						if str(e, "id") == execution.ID {
							return str(e, "started_at")
						}
					}
					return ""
				}() {
					continue
				}
				replay.Events = append(replay.Events, RuntimeEvent{Type: str(r, "event_type"), Payload: raw(str(r, "payload_json"))})
			}
			step := *findStepByNode(replay.Steps, "evidence_screening")
			step.Attempt, step.Status = attempt, StepRunning
			node := compilationNodeMap(replay.Run.Compilation)[step.NodeID]
			// Prompt markers were reissued for this Chat. Restore to Workflow
			// domain then require the ORIGINAL recorded hash; no fake binding.
			var in struct {
				Candidates []tool.CitationRef `json:"candidates"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				t.Fatal(err)
			}
			for i := range in.Candidates {
				r := in.Candidates[i]
				in.Candidates[i].Reference = citation.KnowledgeReference(replay.Run.ID, r.IndexVersionID, r.ChunkID, r.QuoteSHA256)
			}
			input = restoreWorkflowCitationMarkers(input, in.Candidates, execution.ChatRunID)
			if hashJSON(input) != execution.InputSHA256 {
				t.Fatalf("captured input cannot reproduce exact binding: got=%s want=%s", hashJSON(input), execution.InputSHA256)
			}
			step.Input, step.InputSHA256 = input, execution.InputSHA256
			for i := range replay.Steps {
				if replay.Steps[i].ID == step.ID {
					replay.Steps[i] = step
				}
			}
			repo := &indexedAcceptedFixture{}
			// Keywords have a distinct bounded continuation, not indexed reread
			// progress. Simulate both already-used keyword rounds to isolate the
			// long-running read loop; retain the exact original stage input hash.
			for i := 0; i < 2; i++ {
				replay.Events = append(replay.Events, RuntimeEvent{Type: "workflow.local_evidence_requested", Payload: mustJSON(map[string]any{"materialKey": localEvidenceMaterialKey(replay), "queries": []string{"fixture-already-completed-keyword-rounds"}})})
			}
			s := &RuntimeService{repository: repo, tools: tool.NewService(calls, tool.JSONSchemaValidator{}), now: time.Now, newID: func() (string, error) { return "replay-event", nil }}
			if added, err := s.newIndexedEvidence(context.Background(), replay, step, node, execution); err != nil || len(added) != 0 {
				t.Fatal("captured zero-growth round counted as progress", len(added), err)
			}
			value, _, err := normalizeWorkflowAIStageSubmissionForInput(execution.OutputText, selectedEvidencePhaseNode(node, step.Input), step.Input)
			if err != nil {
				t.Fatal("captured submission schema", err)
			}
			_, validationInput, reads, err := s.indexedAIStageSubmission(context.Background(), replay, step, node, execution, value)
			if err != nil {
				t.Fatal(err)
			}
			if len(reads) == 0 || string(validationInput) == string(step.Input) {
				t.Fatal("reread fixture did not include evicted references")
			}
			if err := s.completeAIExecution(context.Background(), replay, step, node, execution, true); err != nil {
				t.Fatal(err)
			}
			if repo.queued {
				t.Fatal("captured rereads scheduled another synthesis")
			}
			if attempt != 22 {
				if len(repo.repairs) != 1 || repo.execution.ErrorCode != "WORKFLOW_AI_OUTPUT_INVALID" {
					t.Fatal("genuine source/assessment error was hidden", repo.failure, repo.execution.ErrorMessage)
				}
				t.Logf("actual attempt %d: no indexed continuation; existing scientific submission error correctly remains: %s", attempt, repo.execution.ErrorMessage)
				return
			}
			if repo.failure != "" || len(repo.repairs) > 0 {
				t.Fatalf("captured synthesis still invalid: failure=%s validation=%s", repo.failure, repo.execution.ErrorMessage)
			}
			if len(repo.output) == 0 || rawJSONEqual(repo.execution.Output, raw(`{}`)) {
				t.Fatal("captured valid synthesis was discarded")
			}
			t.Logf("actual attempt %d: %d tool-read snapshots, zero new source chunks; valid scientific output accepted without continuation", attempt, len(reads))
		})
	}
}
