package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/modelcap"
)

type implementationStorageFixture struct {
	db   *sql.DB
	path string
	repo *RunRepository
	base chat.ImplementationDraft
}

func newImplementationStorageFixture(t *testing.T) *implementationStorageFixture {
	t.Helper()
	db, path := workflowReasoningHistory(t)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workflow_ai_executions SET status='cancelled'`); err != nil {
		t.Fatal(err)
	}
	f := &implementationStorageFixture{db: db, path: path, repo: NewRunRepository(db)}
	f.createAttempt(t, 10)
	return f
}

func (f *implementationStorageFixture) createAttempt(t *testing.T, attempt int) {
	t.Helper()
	ctx, now := context.Background(), time.Now().UTC()
	var conversationID string
	if err := f.db.QueryRow(`SELECT conversation_id FROM workflow_conversations WHERE workflow_run_id='wr'`).Scan(&conversationID); err != nil {
		t.Fatal(err)
	}
	input := strings.Repeat("b", 64)
	if _, err := f.db.Exec(`UPDATE workflow_runs SET status='running',current_step_ordinal=0,cancel_requested=0 WHERE id='wr';`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE workflow_steps SET node_id='method_implementation',status='running',attempt=?,input_sha256=? WHERE id='step'`, attempt, input); err != nil {
		t.Fatal(err)
	}
	runID := fmt.Sprintf("parts-%d", attempt)
	run := chat.Run{ID: runID, ConversationID: conversationID, UserMessageID: runID + "-user", AssistantMessageID: runID + "-assistant", ModelProfileID: "profile", ModelID: "fixture", RequestedReasoningLevel: modelcap.ReasoningHigh, Status: chat.RunRunning, CreatedAt: now, StartedAt: &now, UpdatedAt: now}
	u := conversation.Message{ID: run.UserMessageID, ConversationID: conversationID, RunID: runID, Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now}
	a := conversation.Message{ID: run.AssistantMessageID, ConversationID: conversationID, RunID: runID, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now, UpdatedAt: now}
	c := chat.WorkflowAIExecution{ID: runID + "-execution", WorkflowRunID: "wr", WorkflowStepID: "step", Attempt: attempt, NodeKind: "agent_stage", PromptVersion: "dynamic-implementation-v6-impl-parts-v1", PromptText: "same frozen scientific requirements", PromptSHA256: strings.Repeat("a", 64), InputSHA256: input, OutputSchema: json.RawMessage(`{"type":"object","required":["code","methodSummary"],"properties":{"code":{"type":"string"},"methodSummary":{"type":"string"}}}`), OutputSchemaSHA256: strings.Repeat("c", 64), AllowedTools: json.RawMessage(`[]`), CreatedAt: now}
	if err := f.repo.CreateWorkflowAIWithMessages(ctx, run, u, a, c); err != nil {
		t.Fatal(err)
	}
	f.base = chat.ImplementationDraft{RunID: runID, ExecutionID: c.ID, WorkflowRunID: "wr", WorkflowStepID: "step", Attempt: attempt, InputSHA256: input, PromptSHA256: c.PromptSHA256, SchemaSHA256: c.OutputSchemaSHA256, GenerationID: "generation", PartCount: 1, Metadata: json.RawMessage(`{"methodSummary":"same agreed method"}`), Code: "import json\n", SourceTurnIndex: 1, UpdatedAt: now}
}

func (f *implementationStorageFixture) completedTurn(t *testing.T, raw string) int {
	t.Helper()
	ctx, now := context.Background(), time.Now().UTC()
	run, err := f.repo.IncrementModelTurns(ctx, f.base.RunID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repo.BeginModelTurn(ctx, run.ID, run.ModelTurns, now); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.UpdateModelTurnDraft(ctx, run.ID, run.ModelTurns, raw, 0, now); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.FinishModelTurn(ctx, run.ID, run.ModelTurns, chat.ModelTurnCompleted, "tool_calls", 0, now); err != nil {
		t.Fatal(err)
	}
	return run.ModelTurns
}

func (f *implementationStorageFixture) finishAttempt(t *testing.T, status, failure string) {
	t.Helper()
	if _, err := f.db.Exec(`UPDATE runs SET status=?,error_code=? WHERE id=?`, status, failure, f.base.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE workflow_ai_executions SET status=?,error_code=? WHERE id=?`, status, failure, f.base.ExecutionID); err != nil {
		t.Fatal(err)
	}
}

func TestImplementationCheckpointStorageRejectionAndRestartPreserveDiagnostic(t *testing.T) {
	f := newImplementationStorageFixture(t)
	ctx := context.Background()
	v := f.base
	v.SourceEvidence = []model.CompletedToolEvidence{{SourceRunID: v.RunID, CallID: "read", ToolName: "builtin.workspace.read_text", Arguments: json.RawMessage(`{"path":"method.txt"}`), Result: "READ-ONCE-EVIDENCE-SENTINEL"}}
	v.Complete = true
	v.SourceTurnIndex = f.completedTurn(t, "completed full source")
	if err := f.repo.SaveImplementationDraft(ctx, v); err != nil {
		t.Fatal(err)
	}
	v.Rejected, v.CorrectionCount, v.ValidationError = true, 1, "preserve paired IDs"
	v.SourceTurnIndex = f.completedTurn(t, "local audited full candidate")
	if err := f.repo.SaveImplementationDraft(ctx, v); err != nil {
		t.Fatal(err)
	}
	v.GenerationID, v.Rejected, v.Complete = "correction", false, false
	v.Code = "corrected = True\n"
	v.SourceTurnIndex = f.completedTurn(t, "completed corrected first source part")
	if err := f.repo.SaveImplementationDraft(ctx, v); err != nil {
		t.Fatal(err)
	}
	f.finishAttempt(t, "cancelled", "RUN_CANCELLED")
	f.createAttempt(t, 11)
	loaded, found, err := f.repo.ImplementationDraft(ctx, f.base.RunID)
	if err != nil || !found || !loaded.AdoptionPending || loaded.ValidationError != "preserve paired IDs" || loaded.CorrectionCount != 1 || loaded.Code != v.Code || !sameImplementationEvidence(loaded, v) {
		t.Fatal("lost repaired prefix or rejection context", found, loaded, err)
	}
	if err := f.repo.SaveImplementationDraft(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.db, err = OpenExisting(ctx, f.path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer f.db.Close()
	f.repo = NewRunRepository(f.db)
	loaded, found, err = f.repo.ImplementationDraft(ctx, f.base.RunID)
	if err != nil || !found || loaded.AdoptionPending || loaded.ValidationError != v.ValidationError || loaded.GenerationID != v.GenerationID || !sameImplementationEvidence(loaded, v) {
		t.Fatal("restart lost adoption", found, loaded, err)
	}
}

func TestImplementationCheckpointContentExhaustionStartsFreshExplicitAttempt(t *testing.T) {
	for _, failure := range []string{"WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED", "WORKFLOW_AI_OUTPUT_INVALID"} {
		t.Run(failure, func(t *testing.T) {
			f := newImplementationStorageFixture(t)
			ctx := context.Background()
			v := f.base
			v.Complete = true
			v.SourceTurnIndex = f.completedTurn(t, "rejected complete candidate")
			if err := f.repo.SaveImplementationDraft(ctx, v); err != nil {
				t.Fatal(err)
			}
			f.finishAttempt(t, "failed", failure)
			f.createAttempt(t, 11)
			if _, found, err := f.repo.ImplementationDraft(ctx, f.base.RunID); found || err != nil {
				t.Fatal("explicit retry would revalidate known-rejected complete source", found, err)
			}
			v.RunID, v.ExecutionID, v.Attempt, v.SourceTurnIndex = f.base.RunID, f.base.ExecutionID, f.base.Attempt, 0
			if err := f.repo.SaveImplementationDraft(ctx, v); err == nil {
				t.Fatal("known-rejected checkpoint was adopted")
			}
		})
	}
}

func TestImplementationCheckpointRejectionMarkerDoesNotPublishSource(t *testing.T) {
	f := newImplementationStorageFixture(t)
	ctx := context.Background()
	v := f.base
	v.PartCount, v.Metadata, v.Code, v.Rejected, v.CorrectionCount, v.ValidationError = 0, json.RawMessage(`{}`), "", true, 1, "invalid legacy full envelope"
	v.SourceEvidence = []model.CompletedToolEvidence{{SourceRunID: v.RunID, CallID: "read", ToolName: "builtin.workspace.read_text", Arguments: json.RawMessage(`{"path":"method.txt"}`), Result: "READ-ONCE-EVIDENCE-SENTINEL"}}
	v.SourceTurnIndex = f.completedTurn(t, "immutable rejected original response")
	if err := f.repo.SaveImplementationDraft(ctx, v); err != nil {
		t.Fatal(err)
	}
	f.finishAttempt(t, "cancelled", "RUN_CANCELLED")
	f.createAttempt(t, 11)
	loaded, found, err := f.repo.ImplementationDraft(ctx, f.base.RunID)
	if err != nil || !found || !loaded.Rejected || loaded.Code != "" || loaded.Complete || loaded.CorrectionCount != 1 || !sameImplementationEvidence(loaded, v) {
		t.Fatal("invalid envelope became accepted source or reset repair budget", loaded, found, err)
	}
	if err := f.repo.SaveImplementationDraft(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	loaded.GenerationID, loaded.Rejected, loaded.PartCount = "corrected", false, 1
	loaded.Metadata, loaded.Code = f.base.Metadata, "corrected = True\n"
	loaded.SourceTurnIndex = f.completedTurn(t, "corrected completed part")
	if err := f.repo.SaveImplementationDraft(ctx, loaded); err != nil {
		t.Fatal("could not save repaired first part", err)
	}
}

func TestImplementationCheckpointStorageRejectsChangedBindingAndLateWrites(t *testing.T) {
	for _, mode := range []string{"initial_not_first", "unfinished_response", "changed_attempt", "changed_input", "cancel_window", "wrong_current_step", "terminal", "replace_metadata", "lost_diagnostic"} {
		t.Run(mode, func(t *testing.T) {
			f := newImplementationStorageFixture(t)
			ctx := context.Background()
			v := f.base
			v.SourceTurnIndex = f.completedTurn(t, "valid completed first part")
			if mode == "replace_metadata" || mode == "lost_diagnostic" {
				if err := f.repo.SaveImplementationDraft(ctx, v); err != nil {
					t.Fatal(err)
				}
				v.PartCount++
				v.Code += "result = True\n"
				v.SourceTurnIndex = f.completedTurn(t, "next completed part")
			}
			var err error
			switch mode {
			case "initial_not_first":
				v.PartCount = 2
			case "unfinished_response":
				_, err = f.db.Exec(`UPDATE model_turn_journal SET status='interrupted' WHERE run_id=?`, v.RunID)
			case "changed_attempt":
				_, err = f.db.Exec(`UPDATE workflow_steps SET attempt=attempt+1 WHERE id='step'`)
			case "changed_input":
				_, err = f.db.Exec(`UPDATE workflow_steps SET input_sha256=? WHERE id='step'`, strings.Repeat("f", 64))
			case "cancel_window":
				_, err = f.db.Exec(`UPDATE workflow_runs SET cancel_requested=1 WHERE id='wr'`)
			case "wrong_current_step":
				_, err = f.db.Exec(`UPDATE workflow_runs SET current_step_ordinal=1 WHERE id='wr'`)
			case "terminal":
				f.finishAttempt(t, "cancelled", "RUN_CANCELLED")
			case "replace_metadata":
				v.Metadata = json.RawMessage(`{"methodSummary":"changed"}`)
			case "lost_diagnostic":
				v.ValidationError = "injected unrelated diagnosis"
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := f.repo.SaveImplementationDraft(ctx, v); err == nil {
				t.Fatal("accepted invalid checkpoint", mode)
			}
		})
	}
}

func TestImplementationCheckpointEvidenceCannotBeRewritten(t *testing.T) {
	for _, mode := range []string{"append", "adoption", "rejection"} {
		t.Run(mode, func(t *testing.T) {
			f := newImplementationStorageFixture(t)
			ctx := context.Background()
			v := f.base
			v.SourceEvidence = []model.CompletedToolEvidence{{SourceRunID: v.RunID, CallID: "read", ToolName: "builtin.workspace.read_text", Arguments: json.RawMessage(`{"path":"method.txt"}`), Result: "original-read-result"}}
			v.Complete = mode == "rejection"
			v.SourceTurnIndex = f.completedTurn(t, "first completed part")
			if err := f.repo.SaveImplementationDraft(ctx, v); err != nil {
				t.Fatal(err)
			}
			originalRun := v.RunID
			switch mode {
			case "append":
				v.PartCount++
				v.Code += "result = True\n"
				v.SourceTurnIndex = f.completedTurn(t, "second part")
			case "adoption":
				f.finishAttempt(t, "cancelled", "RUN_CANCELLED")
				f.createAttempt(t, 11)
				var err error
				v, _, err = f.repo.ImplementationDraft(ctx, f.base.RunID)
				if err != nil {
					t.Fatal(err)
				}
			case "rejection":
				v.Rejected, v.CorrectionCount, v.ValidationError = true, 1, "full method validation failed"
				v.SourceTurnIndex = f.completedTurn(t, "local validation")
			}
			v.SourceEvidence[0].Result = "silently-rewritten-read-result"
			if err := f.repo.SaveImplementationDraft(ctx, v); err == nil {
				t.Fatal("accepted evidence rewrite", mode)
			}
			stored, found, err := f.repo.ImplementationDraft(ctx, originalRun)
			if err != nil || !found || stored.SourceEvidence[0].Result != "original-read-result" {
				t.Fatal("original evidence record changed", found, err)
			}
		})
	}
}
