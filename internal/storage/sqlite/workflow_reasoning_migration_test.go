package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func workflowReasoningHistory(t *testing.T) (*sql.DB, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reasoning-99.db")
	db, err := openDatabase(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrateToVersion(ctx, db, 99); err != nil {
		t.Fatal(err)
	}
	p, err := project.NewService(NewProjectRepository(db), t.TempDir(), t.TempDir()).Create(ctx, "MAX history", "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := conversation.NewService(NewConversationRepository(db)).Create(ctx, p.ID, "frozen history")
	if err != nil {
		t.Fatal(err)
	}
	other, err := conversation.NewService(NewConversationRepository(db)).Create(ctx, p.ID, "other conversation")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile := modelprofile.Profile{ID: "profile", Name: "fixture", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, TimeoutSeconds: 60, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(db).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ id, conv string }{{"chat", c.ID}, {"other-chat", other.ID}} {
		run := chat.Run{ID: item.id, ConversationID: item.conv, UserMessageID: item.id + "-u", AssistantMessageID: item.id + "-a", ModelProfileID: profile.ID, ModelID: profile.ModelID, RequestedReasoningLevel: modelcap.ReasoningMax, Status: chat.RunCompleted, CreatedAt: now, UpdatedAt: now}
		u := conversation.Message{ID: run.UserMessageID, ConversationID: item.conv, RunID: run.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now}
		a := conversation.Message{ID: run.AssistantMessageID, ConversationID: item.conv, RunID: run.ID, Role: conversation.RoleAssistant, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now}
		if err := NewRunRepository(db).CreateWithMessages(ctx, run, u, a); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO workflows(id,project_id,name,version,created_at,updated_at) VALUES ('w',?,'history',1,'2026-10-09','2026-10-09')`, []any{p.ID}},
		{`INSERT INTO workflow_versions(id,workflow_id,version_number,definition_json,definition_sha256,compilation_json,compilation_sha256,created_at)
		 VALUES ('v','w',1,'{}',?,'{}',?,'2026-10-09')`, []any{strings.Repeat("a", 64), strings.Repeat("b", 64)}},
		{`INSERT INTO research_tasks(id,project_id,title,origin_kind,created_at,updated_at) VALUES ('task',?,'history','ai_route','2026-10-09','2026-10-09')`, []any{p.ID}},
		{`INSERT INTO workflow_runs(id,project_id,workflow_id,workflow_version_id,status,inputs_json,inputs_sha256,compilation_json,compilation_sha256,created_at,updated_at,research_task_id)
		 VALUES ('wr',?,'w','v','failed','{}',?,'{}',?,'2026-10-09','2026-10-09','task')`, []any{p.ID, strings.Repeat("c", 64), strings.Repeat("d", 64)}},
		{`INSERT INTO workflow_steps(id,workflow_run_id,node_id,ordinal,node_kind,status,updated_at)
		 VALUES ('step','wr','explore',0,'agent_stage','failed','2026-10-09')`, nil},
		{`INSERT INTO workflow_conversations(workflow_run_id,conversation_id,created_at) VALUES ('wr',?,'2026-10-09')`, []any{c.ID}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	statuses := []string{"prepared", "running", "completed", "failed", "cancelled", "interrupted"}
	for i, level := range []string{"none", "minimal", "low", "medium", "high", "xhigh"} {
		if err := insertReasoningExecution(db, fmt.Sprintf("e%d", i), i+1, level, statuses[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO workflow_ai_chat_runs VALUES ('e2','chat','2026-10-09')`); err != nil {
		t.Fatal(err)
	}
	assertDatabaseHealthy(t, db)
	return db, path
}

func insertReasoningExecution(db *sql.DB, id string, attempt int, level, status string) error {
	var completed any
	if status != "prepared" && status != "running" {
		completed = "2026-10-09"
	}
	_, err := db.Exec(`INSERT INTO workflow_ai_executions(
	 id,workflow_run_id,workflow_step_id,attempt,node_kind,model_profile_id,model_id,reasoning_level,
	 prompt_version,prompt_text,prompt_sha256,input_sha256,allowed_tools_json,output_schema_json,output_schema_sha256,
	 status,output_text,output_json,output_sha256,input_tokens,output_tokens,reasoning_tokens,model_turns,
	 error_code,error_message,created_at,started_at,completed_at,updated_at)
	 VALUES (?,'wr','step',?,'agent_stage','profile','fixture',?,'v1',?,?,?,?,?,?,?, ?,?,?,1,2,3,4,'historical','原记录','2026-10-09','2026-10-09',?,'2026-10-09')`,
		id, attempt, level, "不可截断的冻结输入\n  原始空白", strings.Repeat("a", 64), strings.Repeat("b", 64), `[ "fixture.read" ]`, `{ "type": "object" }`, strings.Repeat("c", 64), status,
		"原始输出\n", `{ "summary": "旧结果" }`, strings.Repeat("d", 64), completed)
	return err
}

func reasoningSchema(t *testing.T, db *sql.DB) string {
	t.Helper()
	var value string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='workflow_ai_executions'`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(strings.ReplaceAll(value, `"workflow_ai_executions"`, "workflow_ai_executions")), " ")
}

// Collision fixtures model the pre-100 question build, not the latest empty
// database returned by their shared harness. Rewind only the empty AI table;
// production migration history is never edited by this test-only helper.
func restoreQuestionCollisionHistory(t *testing.T, db *sql.DB) {
	t.Helper()
	var snapshots int
	if err := db.QueryRow(`SELECT count(*) FROM workflow_revision_write_snapshots`).Scan(&snapshots); err != nil || snapshots != 0 {
		t.Fatal("historical fixture must have no write snapshots", snapshots, err)
	}
	if _, err := db.Exec(`DROP TABLE workflow_revision_write_snapshots`); err != nil {
		t.Fatal(err)
	}
	// This fixture rolls migration history back to 99. Remove the later
	// migration's schema objects as well; leaving them behind is not a real
	// historical database and falsely reports duplicate-index upgrade failures.
	if _, err := db.Exec(`ALTER TABLE model_turn_journal DROP COLUMN submission_commentary`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"idx_runs_user_message", "idx_runs_assistant_message", "idx_permission_grants_run", "idx_context_checkpoint_message", "idx_message_citations_tool", "idx_python_kernel_executions_run", "idx_workflow_decisions_run", "idx_revision_proposals_workflow", "idx_revision_proposals_chat", "idx_revision_proposals_message", "idx_research_timeline_workflow"} {
		if _, err := db.Exec(`DROP INDEX IF EXISTS ` + name); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"run_events_after_chat_delete", "run_events_after_workflow_delete"} {
		if _, err := db.Exec(`DROP TRIGGER IF EXISTS ` + name); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM workflow_ai_executions`).Scan(&count); err != nil || count != 0 {
		t.Fatal("collision fixture must have no AI executions", count, err)
	}
	items, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.version != 100 {
			continue
		}
		old := strings.Replace(item.sql, "'xhigh','max')", "'xhigh')", 1)
		if _, err := db.Exec("PRAGMA foreign_keys=OFF; BEGIN; " + old + "; DELETE FROM mcp_servers WHERE id='builtin-exa-search'; DELETE FROM schema_migrations WHERE version>=100; COMMIT; PRAGMA foreign_keys=ON;"); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatal("MAX migration missing")
}

func TestWorkflowReasoningMaxUpgradePreservesHistoryAndBindings(t *testing.T) {
	ctx := context.Background()
	db, path := workflowReasoningHistory(t)
	if err := insertReasoningExecution(db, "max", 7, "max", "completed"); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("old MAX failure not reproduced: %v", err)
	}
	oldSchema := reasoningSchema(t, db)
	before := map[string][][]any{}
	for _, table := range []string{"workflow_ai_executions", "workflow_ai_chat_runs", "workflow_steps", "workflow_runs", "research_tasks", "runs", "messages", "schema_migrations"} {
		before[table] = databaseRows(t, db, table)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(oldSchema, "'xhigh')", "'xhigh','max')", 1); reasoningSchema(t, db) != want {
		t.Fatal("migration changed more than the supported reasoning values")
	}
	for table, rows := range before {
		after := databaseRows(t, db, table)
		if table == "schema_migrations" {
			after = after[:len(rows)]
		}
		if !reflect.DeepEqual(rows, after) {
			t.Fatalf("old %s rows changed", table)
		}
	}
	assertDatabaseHealthy(t, db)
	var enabled int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatal("foreign keys not restored", err)
	}
	for i, level := range modelcap.AllReasoningLevels() {
		if err := insertReasoningExecution(db, "new-"+string(level), 7+i, string(level), "completed"); err != nil {
			t.Fatalf("%s rejected: %v", level, err)
		}
	}
	if err := insertReasoningExecution(db, "invalid", 20, "unknown", "completed"); err == nil {
		t.Fatal("unknown level accepted")
	}
	if err := insertReasoningExecution(db, "duplicate", 7, "max", "completed"); err == nil {
		t.Fatal("duplicate attempt accepted")
	}
	for _, query := range []string{
		`UPDATE workflow_ai_executions SET prompt_sha256='bad' WHERE id='e0'`,
		`UPDATE workflow_ai_executions SET status='running' WHERE id='e2'`,
		`UPDATE workflow_ai_executions SET workflow_step_id='missing' WHERE id='e0'`,
		`INSERT INTO workflow_ai_chat_runs VALUES ('e0','other-chat','2026-10-09')`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Fatalf("existing constraint/trigger lost: %s", query)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if !reflect.DeepEqual(before["workflow_ai_chat_runs"], databaseRows(t, reopened.DB(), "workflow_ai_chat_runs")) {
		t.Fatal("reopen lost chat binding")
	}
	if _, err := reopened.DB().Exec(`DELETE FROM workflow_ai_executions WHERE id='e2'`); err != nil {
		t.Fatal(err)
	}
	if len(databaseRows(t, reopened.DB(), "workflow_ai_chat_runs")) != 0 || len(databaseRows(t, reopened.DB(), "runs")) != 2 {
		t.Fatal("cascade semantics changed")
	}
	assertDatabaseHealthy(t, reopened.DB())
}

func TestWorkflowReasoningMaxMigrationFailureRollsBack(t *testing.T) {
	db, _ := workflowReasoningHistory(t)
	before := map[string][][]any{}
	for _, table := range []string{"workflow_ai_executions", "workflow_ai_chat_runs", "schema_migrations", "sqlite_schema"} {
		before[table] = databaseRows(t, db, table)
	}
	items, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var item migration
	for _, candidate := range items {
		if candidate.version == 100 {
			item = candidate
		}
	}
	item.sql += "\nINSERT INTO nonexistent_reasoning_fixture VALUES (1);"
	if err := applyMigrationWithForeignKeysDisabled(context.Background(), db, item); err == nil {
		t.Fatal("injected failure accepted")
	}
	for table, rows := range before {
		if !reflect.DeepEqual(rows, databaseRows(t, db, table)) {
			t.Fatalf("failed migration changed %s", table)
		}
	}
	var enabled int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatal("rollback left foreign keys disabled", err)
	}
	assertDatabaseHealthy(t, db)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal("valid migration could not retry", err)
	}
}
