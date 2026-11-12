package sqlite

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

func workflowQuestionStorageFixture(t *testing.T, frozenGrant, executionGrant bool) (*Store, chat.Run, tool.Call) {
	t.Helper()
	store, run, call := questionStorageFixture(t)
	var projectID string
	if err := store.DB().QueryRow(`SELECT project_id FROM conversations WHERE id=?`, run.ConversationID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	node := workflow.CompiledNode{ID: "review", Kind: workflow.NodeAgentStage}
	if frozenGrant {
		node.AllowedTools = []workflow.ToolSnapshot{{QualifiedName: userinput.ToolName}}
	}
	compilation, _ := json.Marshal(workflow.Compilation{Nodes: []workflow.CompiledNode{node}})
	allowed := `[]`
	if executionGrant {
		allowed = `["builtin.user_input.request"]`
	}
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO workflows(id,project_id,name,version,created_at,updated_at) VALUES ('w',?,'question',1,'2026-10-12','2026-10-12')`, []any{projectID}},
		{`INSERT INTO workflow_versions(id,workflow_id,version_number,definition_json,definition_sha256,compilation_json,compilation_sha256,created_at) VALUES ('v','w',1,'{}',?,'{}',?,'2026-10-12')`, []any{strings.Repeat("a", 64), strings.Repeat("b", 64)}},
		{`INSERT INTO workflow_runs(id,project_id,workflow_id,workflow_version_id,status,inputs_json,inputs_sha256,compilation_json,compilation_sha256,created_at,updated_at) VALUES ('wr',?,'w','v','running','{}',?,?,?,'2026-10-12','2026-10-12')`, []any{projectID, strings.Repeat("c", 64), string(compilation), strings.Repeat("d", 64)}},
		{`INSERT INTO workflow_steps(id,workflow_run_id,node_id,ordinal,node_kind,status,attempt,updated_at) VALUES ('step','wr','review',0,'agent_stage','running',1,'2026-10-12')`, nil},
		{`INSERT INTO workflow_conversations(workflow_run_id,conversation_id,created_at) VALUES ('wr',?,'2026-10-12')`, []any{run.ConversationID}},
		{`INSERT INTO workflow_ai_executions(id,workflow_run_id,workflow_step_id,attempt,node_kind,model_profile_id,model_id,reasoning_level,prompt_version,prompt_text,prompt_sha256,input_sha256,allowed_tools_json,output_schema_json,output_schema_sha256,status,created_at,updated_at) VALUES ('e','wr','step',1,'agent_stage','profile','fixture','medium','qi1','review',?,?,?,'{"type":"object"}',?,'running','2026-10-12','2026-10-12')`, []any{strings.Repeat("a", 64), strings.Repeat("b", 64), allowed, strings.Repeat("c", 64)}},
		{`INSERT INTO workflow_ai_chat_runs(execution_id,chat_run_id,created_at) VALUES ('e',?,'2026-10-12')`, []any{run.ID}},
	} {
		if _, err := store.DB().Exec(statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	return store, run, call
}

func TestWorkflowQuestionNeedsBothFrozenGrantsAndCurrentExecution(t *testing.T) {
	for _, tc := range []struct {
		name              string
		frozen, execution bool
		mutate            string
	}{
		{"no frozen question capability", false, true, ""},
		{"no execution question capability", true, false, ""},
		{"old attempt", true, true, `UPDATE workflow_steps SET attempt=2 WHERE id='step'`},
		{"different current step", true, true, `UPDATE workflow_runs SET current_step_ordinal=1 WHERE id='wr'`},
		{"step stopped", true, true, `UPDATE workflow_steps SET status='failed' WHERE id='step'`},
		{"execution stopped", true, true, `UPDATE workflow_ai_executions SET status='failed' WHERE id='e'`},
		{"cancel requested", true, true, `UPDATE workflow_runs SET cancel_requested=1 WHERE id='wr'`},
		{"workflow stopped", true, true, `UPDATE workflow_runs SET status='cancelled' WHERE id='wr'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, run, call := workflowQuestionStorageFixture(t, tc.frozen, tc.execution)
			if tc.mutate != "" {
				if _, err := store.DB().Exec(tc.mutate); err != nil {
					t.Fatal(err)
				}
			}
			repo := NewRunRepository(store.DB())
			if _, err := repo.WaitForUserInput(context.Background(), run.ID, call.ID, time.Now().UTC()); err == nil {
				t.Fatal("unauthorized workflow question paused run")
			}
			loaded, err := repo.Get(context.Background(), run.ID)
			if err != nil || loaded.Status != chat.RunRunning || loaded.PendingUserInputCallID != "" {
				t.Fatal("rejection changed chat state", loaded, err)
			}
		})
	}
}

func TestWorkflowQuestionReplyRechecksCurrentStageAtomically(t *testing.T) {
	for _, mutation := range []string{
		`UPDATE workflow_steps SET attempt=2 WHERE id='step'`,
		`UPDATE workflow_runs SET current_step_ordinal=1 WHERE id='wr'`,
		`UPDATE workflow_runs SET cancel_requested=1 WHERE id='wr'`,
		`UPDATE workflow_ai_executions SET status='completed' WHERE id='e'`,
		`UPDATE workflow_ai_executions SET allowed_tools_json='[]' WHERE id='e'`,
		`UPDATE workflow_runs SET compilation_json=json_set(compilation_json,'$.nodes[0].allowedTools',json('[]')) WHERE id='wr'`,
	} {
		t.Run(mutation, func(t *testing.T) {
			store, run, call := workflowQuestionStorageFixture(t, true, true)
			repo, ctx := NewRunRepository(store.DB()), context.Background()
			if _, err := repo.WaitForUserInput(ctx, run.ID, call.ID, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if _, err := store.DB().Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if _, err := repo.RespondToUserInput(ctx, userinput.Response{ConversationID: run.ConversationID, RunID: run.ID, CallID: call.ID, Answers: userinput.Answers{"q": {OptionIDs: []string{"a"}}}}, time.Now().UTC()); err == nil {
				t.Fatal("stale question reply committed")
			}
			var answers int
			if err := store.DB().QueryRow(`SELECT count(*) FROM tool_results WHERE tool_call_id=?`, call.ID).Scan(&answers); err != nil || answers != 0 {
				t.Fatal("rejected answer persisted", answers, err)
			}
		})
	}
}

func TestWorkflowHumanAnswerIsVisibleButOriginalPromptIsNot(t *testing.T) {
	store, run, call := workflowQuestionStorageFixture(t, true, true)
	ctx, repo := context.Background(), NewRunRepository(store.DB())
	if _, err := repo.WaitForUserInput(ctx, run.ID, call.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RespondToUserInput(ctx, userinput.Response{ConversationID: run.ConversationID, RunID: run.ID, CallID: call.ID, Answers: userinput.Answers{"q": {OptionIDs: []string{"a"}}}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	r := NewConversationRepository(store.DB())
	values, err := r.ListVisibleMessages(ctx, run.ConversationID, 200)
	if err != nil || len(values) != 2 {
		t.Fatal("human answer missing", len(values), err)
	}
	for _, m := range values {
		if m.ID == run.UserMessageID {
			t.Fatal("stage prompt sent to UI")
		}
		if m.Role == conversation.RoleUser && m.Internal {
			t.Fatal("human answer still hidden")
		}
	}
	full, err := r.ListMessagesForRun(ctx, run.ConversationID, run.ID)
	if err != nil || len(full) != 3 {
		t.Fatal("full history truncated", len(full), err)
	}
}
