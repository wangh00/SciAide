package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

func TestCancelledAIRetryRestoresOnlyUntouchedDownstreamAndKeepsHistory(t *testing.T) {
	h := newWorkflowRuntimeHarness(t, runtimeFixtureTool{name: "fixture.keep", risk: tool.RiskLow, idempotent: true})
	saved := h.save(t, workflow.Definition{SchemaVersion: 1, Name: "Cancelled retry", Inputs: []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}}, Nodes: []workflow.Node{
		{ID: "keep", Name: "Keep", Kind: workflow.NodeTool, ToolName: "fixture.keep", Arguments: json.RawMessage(`{}`)},
		{ID: "read", Name: "AI reading", Kind: workflow.NodeAIAnalysis, Prompt: "Return bounded summary", PromptVersion: "fixture-v1", OutputSchema: json.RawMessage(`{"type":"object"}`), Arguments: json.RawMessage(`{}`)},
		{ID: "selection", Name: "Later decision", Kind: workflow.NodeHumanConfirmation, Prompt: "Continue?", Arguments: json.RawMessage(`{}`)},
	}, Edges: []workflow.Edge{{FromNode: "$input", FromPort: "topic", ToNode: "keep", ToPort: "query"}, {FromNode: "keep", FromPort: "structured.answer", ToNode: "read", ToPort: "context"}, {FromNode: "read", FromPort: "analysis", ToNode: "selection", ToPort: "context"}}})
	started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, ModelProfileID: "profile", ModelID: "model", Inputs: json.RawMessage(`{"topic":"fixture"}`)})
	if err != nil {
		t.Fatal(err)
	}
	failed := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunFailed) // No AI executor is deliberately configured.
	if failed.Steps[0].Status != workflow.StepCompleted {
		t.Fatal("prior checkpoint missing")
	}
	if _, err := h.store.DB().Exec(`UPDATE workflow_runs SET status='cancelled',cancel_requested=1 WHERE id=?`, failed.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.DB().Exec(`UPDATE workflow_steps SET status='cancelled' WHERE workflow_run_id=? AND ordinal>=1`, failed.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.DB().Exec(`UPDATE workflow_steps SET attempt=33 WHERE id=?`, failed.Steps[1].ID); err != nil {
		t.Fatal(err)
	}
	// A late old execution must stay superseded after restoration, even if its
	// provider ignored cancellation. Use an isolated persisted active attempt.
	if _, err := h.store.DB().Exec(`INSERT INTO workflow_ai_executions(id,workflow_run_id,workflow_step_id,attempt,node_kind,model_profile_id,model_id,reasoning_level,prompt_version,prompt_text,prompt_sha256,input_sha256,allowed_tools_json,output_schema_json,output_schema_sha256,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "late-execution", failed.Run.ID, failed.Steps[1].ID, 33, "ai_analysis", "profile", "model", "medium", "fixture-v1", "fixture", strings.Repeat("a", 64), strings.Repeat("b", 64), "[]", "{}", strings.Repeat("c", 64), "running", formatTime(time.Now()), formatTime(time.Now())); err != nil {
		t.Fatal(err)
	}
	repo := NewWorkflowRuntimeRepository(h.store.DB())
	now := time.Now().UTC()
	event := workflow.RuntimeEvent{ID: "cancelled-retry", WorkflowRunID: failed.Run.ID, Type: "workflow.step_retry_queued", Payload: json.RawMessage(`{}`), CreatedAt: now}
	if err := repo.ResetStepForRetry(context.Background(), failed.Run.ID, failed.Steps[2].ID, nil, now, event); err == nil {
		t.Fatal("restored wrong cancelled step")
	}
	if err := repo.ResetStepForRetry(context.Background(), failed.Run.ID, failed.Steps[1].ID, nil, now, event); err != nil {
		t.Fatal(err)
	}
	reset, err := repo.GetRun(context.Background(), h.project.ID, failed.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reset.Run.Status != workflow.RunQueued || reset.Run.CancelRequested || reset.Run.CurrentStep != 1 || reset.Steps[0].ToolCallID != failed.Steps[0].ToolCallID || string(reset.Steps[0].Output) != string(failed.Steps[0].Output) || reset.Steps[1].Status != workflow.StepQueued || reset.Steps[1].Attempt != 33 || reset.Steps[2].Status != workflow.StepQueued || reset.Steps[2].Attempt != 0 {
		t.Fatal("cancelled recovery lost checkpoints or downstream state")
	}
	if err := repo.FinishAIExecution(context.Background(), workflow.AIExecution{ID: "late-execution", Status: "completed", Output: json.RawMessage(`{"summary":"late"}`), UpdatedAt: now}); err == nil {
		t.Fatal("late superseded execution completed after restore")
	}
	// Public Retry accepts the same explicit cancelled AI recovery. It must
	// still reject cancelled tool stages rather than replay side effects.
	if _, err := h.store.DB().Exec(`UPDATE workflow_runs SET status='cancelled',cancel_requested=1 WHERE id=?`, reset.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.DB().Exec(`UPDATE workflow_steps SET status='cancelled' WHERE id=?`, reset.Steps[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.runtime.Retry(context.Background(), workflow.RetryCommand{ProjectID: h.project.ID, RunID: reset.Run.ID, StepID: reset.Steps[1].ID}); err != nil {
		t.Fatal("explicit cancelled AI retry unavailable", err)
	}
	waitWorkflowStatus(t, h.runtime, h.project.ID, reset.Run.ID, workflow.RunFailed) // still no model, no hidden API call.
	h.runtime.Wait()
	if _, err := h.store.DB().Exec(`UPDATE workflow_runs SET status='cancelled',cancel_requested=1,current_step_ordinal=0 WHERE id=?`, reset.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.DB().Exec(`UPDATE workflow_steps SET status='cancelled' WHERE id=?`, reset.Steps[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.runtime.Retry(context.Background(), workflow.RetryCommand{ProjectID: h.project.ID, RunID: reset.Run.ID, StepID: reset.Steps[0].ID}); err == nil {
		t.Fatal("cancelled tool was replayed")
	}
}

type cancelledRetryAI struct {
	workflow.AIStageExecutor
	db               *sql.DB
	entered, release chan struct{}
	lateStatus       string
}

func (a *cancelledRetryAI) Start(ctx context.Context, cmd workflow.AIStartCommand) (workflow.AIStageState, error) {
	at := formatTime(time.Now())
	_, err := a.db.ExecContext(ctx, `INSERT INTO workflow_ai_executions(id,workflow_run_id,workflow_step_id,attempt,node_kind,model_profile_id,model_id,reasoning_level,prompt_version,prompt_text,prompt_sha256,input_sha256,allowed_tools_json,output_schema_json,output_schema_sha256,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, cmd.ExecutionID, cmd.WorkflowRunID, cmd.WorkflowStepID, cmd.Attempt, string(cmd.NodeKind), "profile", "model", "medium", cmd.PromptVersion, cmd.PromptText, cmd.PromptSHA256, cmd.InputSHA256, "[]", string(cmd.OutputSchema), cmd.OutputSchemaSHA256, "running", at, at)
	return workflow.AIStageState{ExecutionID: cmd.ExecutionID, ChatRunID: fmt.Sprintf("fixture-chat-%d", cmd.Attempt), Status: "running"}, err
}

func (a *cancelledRetryAI) Get(_ context.Context, e workflow.AIExecution) (workflow.AIStageState, error) {
	if e.Attempt == 1 {
		close(a.entered)
		<-a.release // Intentionally ignores cancellation, like a late provider.
		if a.lateStatus == "error" {
			return workflow.AIStageState{}, fmt.Errorf("late provider error")
		}
		return workflow.AIStageState{Status: "completed", Text: `{"summary":"old late answer"}`}, nil
	}
	return workflow.AIStageState{Status: "completed", Text: `{"summary":"explicitly restored answer"}`}, nil
}

func (a *cancelledRetryAI) Cancel(context.Context, workflow.AIExecution) error { return nil }

func TestCancelledAIRetryIgnoresLateProviderResponseAndError(t *testing.T) {
	for _, late := range []string{"completed", "error"} {
		t.Run(late, func(t *testing.T) {
			h := newWorkflowRuntimeHarness(t)
			ai := &cancelledRetryAI{db: h.store.DB(), entered: make(chan struct{}), release: make(chan struct{}), lateStatus: late}
			if err := h.runtime.SetAIStageExecutor(ai); err != nil {
				t.Fatal(err)
			}
			saved := h.save(t, workflow.Definition{SchemaVersion: 1, Name: "Late AI cancellation", Inputs: []workflow.Port{{Name: "topic", Type: workflow.TypeString, Required: true}}, Nodes: []workflow.Node{
				{ID: "read", Name: "Reading", Kind: workflow.NodeAIAnalysis, PromptVersion: "fixture-v1", Prompt: "Return summary", OutputSchema: json.RawMessage(`{"type":"object","required":["summary"],"properties":{"summary":{"type":"string"}}}`), Arguments: json.RawMessage(`{}`)},
				{ID: "confirm", Name: "Confirm", Kind: workflow.NodeHumanConfirmation, Prompt: "Continue?", Arguments: json.RawMessage(`{}`)},
			}, Edges: []workflow.Edge{{FromNode: "$input", FromPort: "topic", ToNode: "read", ToPort: "context"}, {FromNode: "read", FromPort: "analysis", ToNode: "confirm", ToPort: "context"}}})
			started, err := h.runtime.Start(context.Background(), workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, ModelProfileID: "profile", ModelID: "model", Inputs: json.RawMessage(`{"topic":"fixture"}`)})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-ai.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("AI did not start")
			}
			cancelled, err := h.runtime.Cancel(context.Background(), h.project.ID, started.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = h.runtime.Retry(context.Background(), workflow.RetryCommand{ProjectID: h.project.ID, RunID: started.Run.ID, StepID: cancelled.Steps[0].ID}); err != nil {
				t.Fatal(err)
			}
			close(ai.release)
			restored := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation)
			if restored.Steps[0].Status != workflow.StepCompleted || restored.Steps[0].Attempt != 2 || !strings.Contains(string(restored.Steps[0].Output), "explicitly restored answer") {
				t.Fatal("late old AI changed restored stage")
			}
			if len(restored.AIExecutions) != 2 || restored.AIExecutions[0].Status != "cancelled" || restored.AIExecutions[1].Status != "completed" {
				t.Fatal("old attempt was accepted or replaced", restored.AIExecutions)
			}
		})
	}
}
