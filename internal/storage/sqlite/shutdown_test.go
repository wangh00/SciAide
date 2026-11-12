package sqlite

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/events"
)

func TestLifecycleInterruptIsAtomicAndDoesNotOverwriteCompletedTools(t *testing.T) {
	db, _ := workflowReasoningHistory(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := db.Exec(`UPDATE runs SET status='running' WHERE id='chat'`); err != nil {
		t.Fatal(err)
	}
	r := NewRunRepository(db)
	tools := NewToolRepository(db)
	for _, id := range []string{"completed", "active"} {
		c := tool.Call{ID: id, RunID: "chat", ProviderCallID: id, ToolName: "builtin.workspace.read", ToolVersion: "1", Arguments: json.RawMessage(`{"path":"fixture.txt"}`), Status: tool.CallPending, Idempotent: true, Risk: tool.RiskLow, Permissions: []tool.PermissionRequirement{}, CreatedAt: now, UpdatedAt: now}
		if err := tools.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE tool_calls SET status='completed',completed_at=?,updated_at=? WHERE id='completed'`, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tool_results(tool_call_id,status,text_content,artifacts_json,citations_json,truncated,meta_json,created_at) VALUES ('completed','success','saved original evidence','[]','[]',0,'{}',?)`, formatTime(now)); err != nil {
		t.Fatal(err)
	}
	if err := r.BeginModelTurn(ctx, "chat", 1, now); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateModelTurnDraft(ctx, "chat", 1, "saved draft, not a final answer", 0, now); err != nil {
		t.Fatal(err)
	}
	data := json.RawMessage(`{"runId":"chat","status":"interrupted","errorCode":"APP_CLOSING"}`)
	e := events.New("shutdown-event", "chat", "run", "run.interrupted", 0, data)
	got, changed, err := r.InterruptRun(ctx, "chat", "APP_CLOSING", "host stopped", now, e)
	if err != nil || !changed || got.Status != chat.RunInterrupted || got.ErrorCode != "APP_CLOSING" {
		t.Fatal(got, changed, err)
	}
	var status, text string
	if err := db.QueryRow(`SELECT c.status,r.text_content FROM tool_calls c JOIN tool_results r ON r.tool_call_id=c.id WHERE c.id='completed'`).Scan(&status, &text); err != nil || status != "completed" || text != "saved original evidence" {
		t.Fatal("completed evidence overwritten", status, text, err)
	}
	if err := db.QueryRow(`SELECT status,error_code FROM tool_calls WHERE id='active'`).Scan(&status, &text); err != nil || status != "interrupted" || text != "APP_CLOSING" {
		t.Fatal("active call mislabeled", status, text, err)
	}
	if err := db.QueryRow(`SELECT status FROM model_turn_journal WHERE run_id='chat'`).Scan(&status); err != nil || status != "interrupted" {
		t.Fatal(status, err)
	}
	if _, changed, err := r.InterruptRun(ctx, "chat", "APP_CLOSING", "host stopped", now, e); err != nil || changed {
		t.Fatal("duplicate interruption changed history", changed, err)
	}
	if _, changed, err := r.CancelRun(ctx, "chat", "RUN_CANCELLED", "user cancelled later", now, e); err != nil || changed {
		t.Fatal("late cancellation overwrote interruption", changed, err)
	}
}

func TestLifecycleInterruptLeavesDurableQuestionWaiting(t *testing.T) {
	db, _ := workflowReasoningHistory(t)
	if _, err := db.Exec(`UPDATE runs SET status='waiting_approval',pending_user_input_call_id='pending-question' WHERE id='chat'`); err != nil {
		t.Fatal(err)
	}
	e := events.New("shutdown", "chat", "run", "run.interrupted", 0, json.RawMessage(`{}`))
	got, changed, err := NewRunRepository(db).InterruptRun(context.Background(), "chat", "APP_CLOSING", "host stopped", time.Now(), e)
	if err != nil || changed || got.Status != chat.RunWaitingUserInput || got.PendingUserInputCallID != "pending-question" {
		t.Fatal("shutdown discarded waiting question", got, changed, err)
	}
}
