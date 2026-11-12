package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func TestWorkflowPromptSnapshotUnicodeStorageBoundary(t *testing.T) {
	db, _ := workflowReasoningHistory(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var conversationID string
	if err := db.QueryRow(`SELECT conversation_id FROM workflow_conversations WHERE workflow_run_id='wr'`).Scan(&conversationID); err != nil {
		t.Fatal(err)
	}
	type testCase struct{ name, text, errorField string }
	cases := []testCase{
		{"ascii boundary", strings.Repeat("a", chat.MaxWorkflowPromptChars), ""},
		{"Chinese above old byte limit", strings.Repeat("科研", 50000), ""},
		{"Chinese boundary", strings.Repeat("数", chat.MaxWorkflowPromptChars), ""},
		{"emoji boundary", strings.Repeat("🧪", chat.MaxWorkflowPromptChars), ""},
		{"over character limit", strings.Repeat("数", chat.MaxWorkflowPromptChars+1), "prompt_text"},
		{"empty", "", "prompt_text"},
		{"invalid UTF8", string([]byte{0xff}), "prompt_text"},
		{"bad digest", "正常输入", "input_sha256"},
		{"bad tools", "正常输入", "allowed_tools_json"},
		{"bad schema", "正常输入", "output_schema_json"},
	}
	if dir := os.Getenv("SCIAIDE_REVIEW_SNAPSHOT_REPLAY"); dir != "" {
		prompt, err := os.ReadFile(filepath.Join(dir, "replayed-prompt.txt"))
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, testCase{"captured review", string(prompt), ""})
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UTC()
			id := fmt.Sprintf("snapshot-%d", index)
			run := chat.Run{ID: id, ConversationID: conversationID, UserMessageID: id + "-u", AssistantMessageID: id + "-a", ModelProfileID: "profile", ModelID: "fixture", RequestedReasoningLevel: modelcap.ReasoningHigh, Status: chat.RunQueued, CreatedAt: now, UpdatedAt: now}
			u := conversation.Message{ID: run.UserMessageID, ConversationID: conversationID, RunID: id, Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now}
			a := conversation.Message{ID: run.AssistantMessageID, ConversationID: conversationID, RunID: id, Role: conversation.RoleAssistant, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now}
			execution := chat.WorkflowAIExecution{ID: id + "-execution", WorkflowRunID: "wr", WorkflowStepID: "step", Attempt: 100 + index, NodeKind: "agent_stage", PromptVersion: "review-scope-v1", PromptText: tc.text, PromptSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(tc.text))), InputSHA256: strings.Repeat("b", 64), AllowedTools: json.RawMessage(`[]`), OutputSchema: json.RawMessage(`{"type":"object"}`), OutputSchemaSHA256: strings.Repeat("c", 64), CreatedAt: now}
			switch tc.errorField {
			case "input_sha256":
				execution.InputSHA256 = "bad"
			case "allowed_tools_json":
				execution.AllowedTools = json.RawMessage(`[`)
			case "output_schema_json":
				execution.OutputSchema = json.RawMessage(`{`)
			}
			repo := NewRunRepository(db)
			err := repo.CreateWorkflowAIWithMessages(ctx, run, u, a, execution)
			if tc.errorField != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errorField) {
					t.Fatalf("wrong diagnostic: %v", err)
				}
				for _, query := range []string{`SELECT count(*) FROM runs WHERE id=?`, `SELECT count(*) FROM messages WHERE run_id=?`, `SELECT count(*) FROM workflow_ai_chat_runs WHERE chat_run_id=?`} {
					var count int
					if err := db.QueryRow(query, id).Scan(&count); err != nil || count != 0 {
						t.Fatal("partial state after rejection", count, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := db.Exec(`UPDATE runs SET status='completed' WHERE id=?`, id); err != nil {
					t.Error(err)
				}
			})
			saved, exists, err := repo.WorkflowAIContract(ctx, id)
			if err != nil || !exists || saved.PromptText != tc.text || saved.PromptSHA256 != execution.PromptSHA256 {
				t.Fatal("prompt changed or truncated", err)
			}
			// Optional stage projection receives only digest-verified input. Reopen
			// via a second repository to avoid relying on in-memory binding state.
			input := `{"source":"原始😀材料","successCriteria":["unchanged"]}`
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte(input)))
			if _, err = db.Exec(`UPDATE workflow_steps SET input_json=?,input_sha256=? WHERE id='step'`, input, digest); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`UPDATE workflow_ai_executions SET input_sha256=? WHERE id=?`, digest, execution.ID); err != nil {
				t.Fatal(err)
			}
			verified, ok, e := NewRunRepository(db).WorkflowAIContract(ctx, id)
			if e != nil || !ok || string(verified.Input) != input || verified.PromptText != tc.text {
				t.Fatal("verified input unavailable after repository reopen", e)
			}
			if _, err = db.Exec(`UPDATE workflow_steps SET input_json='{}' WHERE id='step'`); err != nil {
				t.Fatal(err)
			}
			stale, ok, e := repo.WorkflowAIContract(ctx, id)
			if e != nil || !ok || len(stale.Input) != 0 {
				t.Fatal("unverified optional input leaked or added legacy failure", e)
			}

			var chars, bytes int
			if err := db.QueryRow(`SELECT length(prompt_text),length(cast(prompt_text as blob)) FROM workflow_ai_executions WHERE id=?`, execution.ID).Scan(&chars, &bytes); err != nil {
				t.Fatal(err)
			}
			t.Logf("stored %d characters / %d bytes intact", chars, bytes)
		})
	}
	assertDatabaseHealthy(t, db)
}
