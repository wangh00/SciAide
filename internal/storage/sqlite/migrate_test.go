package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/project"
)

func TestWorkspacePythonEnvironmentMigrationPreservesLegacyRecords(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "python-workspace-upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrateToVersion(ctx, db, 60); err != nil {
		t.Fatal(err)
	}
	const at = "2026-08-26T00:00:00Z"
	if _, err := db.ExecContext(ctx, `INSERT INTO projects(id,name,description,workspace_path,workspace_kind,created_at,updated_at) VALUES ('project','Project','','C:/workspace','external',?,?)`, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO python_environments(id,project_id,state,environment_python_path,lock_json,created_at,updated_at) VALUES ('environment','project','ready','C:/Users/test/.sciaide/data/python-envs/project/venv/Scripts/python.exe','[]',?,?)`, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO python_environment_operations(id,project_id,environment_id,kind,state,request_json,started_at,completed_at) VALUES ('operation','project','environment','verify','completed','{}',?,?)`, at, at); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var kind, operationKind string
	if err := db.QueryRowContext(ctx, `SELECT environment_kind FROM python_environments WHERE id='environment'`).Scan(&kind); err != nil || kind != "legacy_managed" {
		t.Fatalf("migrated environment kind = %q, %v", kind, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT kind FROM python_environment_operations WHERE id='operation'`).Scan(&operationKind); err != nil || operationKind != "verify" {
		t.Fatalf("preserved operation kind = %q, %v", operationKind, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO python_environment_operations(id,project_id,environment_id,kind,state,request_json,started_at) VALUES ('bind','project','environment','bind','running','{}',?)`, at); err != nil {
		t.Fatalf("bind operation was rejected: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE python_environments SET environment_kind='invalid' WHERE id='environment'`); err == nil {
		t.Fatal("invalid Python environment kind was accepted")
	}
}

func TestCandidateSelectionMigrationPreservesWorkflowStepsAndDecisions(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "candidate-selection-upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY NOT NULL, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations {
		if item.version >= 60 {
			break
		}
		if migrationNeedsForeignKeysDisabled(item) {
			if err := applyMigrationWithForeignKeysDisabled(ctx, db, item); err != nil {
				t.Fatalf("apply migration %d: %v", item.version, err)
			}
		} else if err := applyMigrationTransaction(ctx, db, item, false); err != nil {
			t.Fatalf("apply migration %d: %v", item.version, err)
		}
	}
	const at = "2026-08-25T00:00:00Z"
	statements := []string{
		`INSERT INTO projects(id,name,description,workspace_path,workspace_kind,created_at,updated_at) VALUES ('project','Project','','C:/fixture','external','` + at + `','` + at + `')`,
		`INSERT INTO workflows(id,project_id,name,description,current_version_id,version,created_at,updated_at) VALUES ('workflow','project','Flow','',NULL,1,'` + at + `','` + at + `')`,
		`INSERT INTO workflow_versions(id,workflow_id,version_number,definition_json,definition_sha256,compilation_json,compilation_sha256,created_at) VALUES ('version','workflow',1,'{}','` + strings.Repeat("a", 64) + `','{}','` + strings.Repeat("b", 64) + `','` + at + `')`,
		`UPDATE workflows SET current_version_id='version' WHERE id='workflow'`,
		`INSERT INTO workflow_runs(id,project_id,workflow_id,workflow_version_id,status,inputs_json,inputs_sha256,compilation_json,compilation_sha256,outputs_json,current_step_ordinal,created_at,updated_at) VALUES ('run','project','workflow','version','completed','{}','` + strings.Repeat("c", 64) + `','{}','` + strings.Repeat("b", 64) + `','{}',1,'` + at + `','` + at + `')`,
		`INSERT INTO workflow_steps(id,workflow_run_id,node_id,ordinal,node_kind,status,attempt,input_json,input_sha256,output_json,idempotency_key,updated_at) VALUES ('step','run','confirm',0,'human_confirmation','completed',1,'{"context":"x"}','` + strings.Repeat("d", 64) + `','{"approved":true}','key','` + at + `')`,
		`INSERT INTO workflow_human_decisions(id,workflow_run_id,workflow_step_id,decision_kind,attempt,approved,note,context_json,created_at) VALUES ('decision','run','step','node_confirmation',1,1,'kept','{}','` + at + `')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("insert migration fixture: %v", err)
		}
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var kind, note string
	if err := db.QueryRowContext(ctx, `SELECT node_kind FROM workflow_steps WHERE id='step'`).Scan(&kind); err != nil || kind != "human_confirmation" {
		t.Fatalf("preserved step = %q, %v", kind, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT note FROM workflow_human_decisions WHERE id='decision'`).Scan(&note); err != nil || note != "kept" {
		t.Fatalf("preserved decision = %q, %v", note, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE workflow_steps SET node_kind='candidate_selection' WHERE id='step'`); err != nil {
		t.Fatalf("candidate_selection remains rejected after migration: %v", err)
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("candidate selection migration left a foreign key violation")
	}
}

func TestSkillCoordinationMigrationPreservesLegacySnapshotBytes(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "skill-context-v1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY NOT NULL, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations {
		if item.version >= 48 {
			break
		}
		if migrationNeedsForeignKeysDisabled(item) {
			if err := applyMigrationWithForeignKeysDisabled(ctx, db, item); err != nil {
				t.Fatalf("apply migration %d: %v", item.version, err)
			}
			continue
		}
		if err := applyMigrationTransaction(ctx, db, item, false); err != nil {
			t.Fatalf("apply migration %d: %v", item.version, err)
		}
	}
	const at = "2026-08-24T00:00:00Z"
	for _, statement := range []string{
		`INSERT INTO projects(id,name,description,workspace_path,workspace_kind,created_at,updated_at) VALUES ('legacy-project','Legacy','','C:/fixture','external','` + at + `','` + at + `')`,
		`INSERT INTO model_profiles(id,name,provider_type,base_url,model_id,secret_ref,timeout_seconds,custom_headers_json,enabled,is_default,created_at,updated_at) VALUES ('legacy-profile','Legacy','openai_compatible','https://example.test/v1','legacy-model','legacy-secret',60,'{}',1,1,'` + at + `','` + at + `')`,
		`INSERT INTO conversations(id,project_id,title,created_at,updated_at) VALUES ('legacy-conversation','legacy-project','Legacy','` + at + `','` + at + `')`,
		`INSERT INTO messages(id,conversation_id,run_id,role,status,created_at,updated_at) VALUES ('legacy-user','legacy-conversation','legacy-run','user','complete','` + at + `','` + at + `')`,
		`INSERT INTO messages(id,conversation_id,run_id,role,status,created_at,updated_at) VALUES ('legacy-assistant','legacy-conversation','legacy-run','assistant','complete','` + at + `','` + at + `')`,
		`INSERT INTO runs(id,conversation_id,user_message_id,assistant_message_id,model_profile_id,status,created_at,updated_at,model_id) VALUES ('legacy-run','legacy-conversation','legacy-user','legacy-assistant','legacy-profile','completed','` + at + `','` + at + `','legacy-model')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("insert legacy fixture: %v", err)
		}
	}
	snapshot := []byte(`{"schemaVersion":1,"runId":"legacy-run","projectId":"legacy-project","contextWindowTokens":200000,"catalogBudgetTokens":4000,"instructionBudgetTokens":40000,"catalog":[],"catalogOmitted":0,"skippedSuggestions":0,"selectionNotices":[],"skills":[],"createdAt":"2026-08-24T00:00:00Z"}`)
	digest := sha256.Sum256(snapshot)
	hash := hex.EncodeToString(digest[:])
	if _, err := db.ExecContext(ctx, `INSERT INTO run_skill_contexts(run_id,project_id,schema_version,snapshot_json,snapshot_hash,created_at) VALUES ('legacy-run','legacy-project',1,?,?,?)`, string(snapshot), hash, at); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var gotJSON, gotHash string
	var schemaVersion int
	if err := db.QueryRowContext(ctx, `SELECT schema_version,snapshot_json,snapshot_hash FROM run_skill_contexts WHERE run_id='legacy-run'`).Scan(&schemaVersion, &gotJSON, &gotHash); err != nil {
		t.Fatal(err)
	}
	if schemaVersion != 1 || gotJSON != string(snapshot) || gotHash != hash {
		t.Fatalf("legacy snapshot changed: schema=%d json=%q hash=%q", schemaVersion, gotJSON, gotHash)
	}
}

func TestMCPToolTimeoutMigrationUpgradesOnlyHistoricalDefault(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "mcp-timeout-upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY NOT NULL, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations {
		if item.version >= 38 {
			break
		}
		if _, err := db.ExecContext(ctx, item.sql); err != nil {
			t.Fatalf("apply migration %d: %v", item.version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES (?,?,?,'2026-08-21T00:00:00Z')`, item.version, item.name, item.checksum); err != nil {
			t.Fatal(err)
		}
	}
	for _, fixture := range []struct {
		id, name, namespace string
		timeout             int
	}{{"default", "Default", "default", 30}, {"custom", "Custom", "custom", 45}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO mcp_servers(id,name,namespace,transport,command,timeout_seconds,created_at,updated_at) VALUES (?,?,?,'stdio','node',?,'2026-08-21T00:00:00Z','2026-08-21T00:00:00Z')`, fixture.id, fixture.name, fixture.namespace, fixture.timeout); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]int{"default": 300, "custom": 45} {
		var got int
		if err := db.QueryRowContext(ctx, `SELECT timeout_seconds FROM mcp_servers WHERE id=?`, id).Scan(&got); err != nil || got != want {
			t.Fatalf("%s timeout = %d, %v; want %d", id, got, err, want)
		}
	}
}

func TestModelRequestUsageMigrationDeduplicatesSnapshotsAndPreservesDistinctRequests(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "usage-upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY NOT NULL, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations {
		if item.version >= 34 {
			break
		}
		if _, err := db.ExecContext(ctx, item.sql); err != nil {
			t.Fatalf("apply migration %d: %v", item.version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES (?,?,?,'2026-01-01T00:00:00Z')`, item.version, item.name, item.checksum); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	createdProject, err := project.NewService(NewProjectRepository(db), filepath.Join(root, "workspaces"), filepath.Join(root, "trash")).Create(ctx, "Usage migration", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 12, 42, 20, 0, time.UTC)
	profile := modelprofile.Profile{ID: "usage-profile", Name: "fixture", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, SecretRef: "secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, IsDefault: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(db).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	createdConversation, err := conversation.NewService(NewConversationRepository(db)).Create(ctx, createdProject.ID, "usage")
	if err != nil {
		t.Fatal(err)
	}
	run := chat.Run{ID: "usage-run", ConversationID: createdConversation.ID, UserMessageID: "usage-user", AssistantMessageID: "usage-assistant", ModelProfileID: profile.ID, ModelID: profile.ModelID, Status: chat.RunRunning, InputTokens: 26400, FreshInputTokens: 13600, OutputTokens: 244, CachedInputTokens: 12800, CacheReportedTurns: 2, CacheReportedFreshInputTokens: 13600, CacheHitTurns: 1, ModelTurns: 1, CreatedAt: now, StartedAt: &now, UpdatedAt: now}
	user := conversation.Message{ID: run.UserMessageID, ConversationID: run.ConversationID, RunID: run.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "usage-user-part", MessageID: run.UserMessageID, Type: "text", CreatedAt: now}}}
	assistant := conversation.Message{ID: run.AssistantMessageID, ConversationID: run.ConversationID, RunID: run.ID, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "usage-assistant-part", MessageID: run.AssistantMessageID, Type: "text", CreatedAt: now}}}
	if err := NewRunRepository(db).CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	for index, fixture := range []struct {
		timestamp string
		payload   string
	}{
		{"2026-08-20T12:42:20.1000000Z", `{"inputTokens":13200,"freshInputTokens":13200,"outputTokens":122,"reasoningTokens":0,"cachedInputTokens":0,"cacheWriteTokens":0,"cacheDetailsReported":true}`},
		{"2026-08-20T12:42:20.1015000Z", `{"inputTokens":13200,"freshInputTokens":400,"outputTokens":122,"reasoningTokens":0,"cachedInputTokens":12800,"cacheWriteTokens":0,"cacheDetailsReported":true}`},
		{"2026-08-20T12:42:20.6000000Z", `{"inputTokens":13200,"freshInputTokens":13200,"outputTokens":122,"reasoningTokens":0,"cachedInputTokens":0,"cacheWriteTokens":0,"cacheDetailsReported":true}`},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO run_events(event_id,version,aggregate_id,aggregate_type,sequence,event_type,timestamp,payload_json) VALUES (?,?,?,?,?,?,?,?)`,
			fmt.Sprintf("usage-event-%d", index), 1, run.ID, "run", index+1, "usage.updated", fixture.timestamp, fixture.payload); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range migrations {
		if item.version < 34 || item.version > 36 {
			continue
		}
		if _, err := db.ExecContext(ctx, item.sql); err != nil {
			t.Fatalf("apply migration %d: %v", item.version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES (?,?,?,'2026-08-21T00:00:00Z')`, item.version, item.name, item.checksum); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO model_request_usage(
		id,run_id,turn_index,request_kind,model_profile_id,model_id,api_protocol,
		input_tokens,fresh_input_tokens,output_tokens,reasoning_tokens,cached_input_tokens,cache_write_tokens,cache_details_reported,
		started_at,completed_at,duration_millis
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"current-request", run.ID, 2, "conversation", profile.ID, profile.ModelID, "openai_chat_completions",
		7, 7, 3, 0, 0, 0, 0, "2026-08-20T12:42:20.7000000Z", "2026-08-20T12:42:20.7000000Z", 0); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var requestCount, input, fresh, output, cached int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*),SUM(input_tokens),SUM(fresh_input_tokens),SUM(output_tokens),SUM(cached_input_tokens) FROM model_request_usage WHERE run_id=?`, run.ID).Scan(&requestCount, &input, &fresh, &output, &cached); err != nil {
		t.Fatal(err)
	}
	if requestCount != 3 || input != 26407 || fresh != 13607 || output != 247 || cached != 12800 {
		t.Fatalf("deduplicated usage = count:%d input:%d fresh:%d output:%d cached:%d", requestCount, input, fresh, output, cached)
	}
	var currentTurn, repairedTurn int
	if err := db.QueryRowContext(ctx, `SELECT turn_index FROM model_request_usage WHERE id='current-request'`).Scan(&currentTurn); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT turn_index FROM model_request_usage WHERE id='usage-event-2'`).Scan(&repairedTurn); err != nil {
		t.Fatal(err)
	}
	if currentTurn != 2 || repairedTurn != 3 {
		t.Fatalf("preserved request turns = current:%d repaired:%d", currentTurn, repairedTurn)
	}
	loaded, err := NewRunRepository(db).Get(ctx, run.ID)
	if err != nil || loaded.InputTokens != 26407 || loaded.FreshInputTokens != 13607 || loaded.OutputTokens != 247 || loaded.CachedInputTokens != 12800 || loaded.CacheReportedTurns != 2 {
		t.Fatalf("rebuilt run = %#v, %v", loaded, err)
	}
}

func TestRequestOutcomeMigrationBackfillsProvenFailedTurn(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "request-outcome-upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY NOT NULL, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations {
		if item.version > 35 {
			break
		}
		if _, err := db.ExecContext(ctx, item.sql); err != nil {
			t.Fatalf("apply migration %d: %v", item.version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES (?,?,?,'2026-08-21T00:00:00Z')`, item.version, item.name, item.checksum); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	createdProject, err := project.NewService(NewProjectRepository(db), filepath.Join(root, "workspaces"), filepath.Join(root, "trash")).Create(ctx, "Outcome migration", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 21, 11, 55, 29, 0, time.UTC)
	profile := modelprofile.Profile{ID: "outcome-profile", Name: "fixture", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, SecretRef: "secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, IsDefault: true, CreatedAt: now, UpdatedAt: now}
	if err := NewModelProfileRepository(db).Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	createdConversation, err := conversation.NewService(NewConversationRepository(db)).Create(ctx, createdProject.ID, "outcome")
	if err != nil {
		t.Fatal(err)
	}
	run := chat.Run{ID: "outcome-run", ConversationID: createdConversation.ID, UserMessageID: "outcome-user", AssistantMessageID: "outcome-assistant", ModelProfileID: profile.ID, ModelID: profile.ModelID, Status: chat.RunRunning, ModelTurns: 1, CreatedAt: now, StartedAt: &now, UpdatedAt: now}
	user := conversation.Message{ID: run.UserMessageID, ConversationID: run.ConversationID, RunID: run.ID, Role: conversation.RoleUser, Status: conversation.MessageComplete, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "outcome-user-part", MessageID: run.UserMessageID, Type: "text", CreatedAt: now}}}
	assistant := conversation.Message{ID: run.AssistantMessageID, ConversationID: run.ConversationID, RunID: run.ID, Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, CreatedAt: now, UpdatedAt: now, Parts: []conversation.MessagePart{{ID: "outcome-assistant-part", MessageID: run.AssistantMessageID, Type: "text", CreatedAt: now}}}
	if err := NewRunRepository(db).CreateWithMessages(ctx, run, user, assistant); err != nil {
		t.Fatal(err)
	}
	completed := now.Add(66 * time.Second)
	if _, err := db.ExecContext(ctx, `INSERT INTO model_request_usage(id,run_id,turn_index,request_kind,model_profile_id,model_id,api_protocol,input_tokens,fresh_input_tokens,output_tokens,reasoning_tokens,cached_input_tokens,cache_write_tokens,cache_details_reported,started_at,completed_at,duration_millis) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"outcome-request", run.ID, 1, "conversation", profile.ID, profile.ModelID, "openai_chat_completions", 0, 0, 0, 0, 0, 0, 0, formatTime(now), formatTime(completed), 66_000); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO model_turn_journal(run_id,turn_index,status,draft_text,finish_reason,provider_item_count,started_at,completed_at,updated_at) VALUES (?,1,'failed','','',0,?,?,?)`, run.ID, formatTime(now), formatTime(completed), formatTime(completed)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE runs SET status='failed',error_code='MODEL_RESPONSE_INCOMPLETE',error_message='模型响应没有完整结束。',error_details='missing terminal event',completed_at=?,updated_at=? WHERE id=?`, formatTime(completed), formatTime(completed), run.ID); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var status int
	var code, message string
	if err := db.QueryRowContext(ctx, `SELECT status_code,error_code,error_message FROM model_request_usage WHERE id='outcome-request'`).Scan(&status, &code, &message); err != nil {
		t.Fatal(err)
	}
	if status != 502 || code != "MODEL_RESPONSE_INCOMPLETE" || !strings.Contains(message, "missing terminal event") {
		t.Fatalf("backfilled outcome = status:%d code:%q message:%q", status, code, message)
	}
}

func TestLegacyBaselineChecksumIsNarrowlyAccepted(t *testing.T) {
	accepted := []string{
		"e9a66fd9fe954e369fb43f68be6a764ed35cbdbbc142bb6c5ec490954e69f3db",
		"ef8938a8fc66c530015ada08c0db37f3300abc1bdcd4166e8142021345287c7f",
	}
	for _, value := range accepted {
		if !legacyBaselineChecksum(value) {
			t.Fatalf("legacyBaselineChecksum(%q) = false", value)
		}
	}
	if legacyBaselineChecksum("changed") {
		t.Fatal("unrecognized checksum was accepted")
	}
}

func TestVisionFallbackMigrationChecksumsAreNarrow(t *testing.T) {
	for _, value := range []string{
		"c5fe0fa7881cf9100223617b2e3b41f9eaca65f86cd92deef770993bdd72a13e",
		"c66f00f44c6a5f3fdcec62b9c52491a63b7a17b885c23c585aac014c6ede63f6",
	} {
		if !visionFallbackMigrationChecksum(value) {
			t.Fatalf("visionFallbackMigrationChecksum(%q) = false", value)
		}
	}
	if visionFallbackMigrationChecksum("changed") {
		t.Fatal("unrecognized vision fallback migration checksum was accepted")
	}
}

func TestMigrateAcceptsEarlyVisionFallbackMigrationChecksum(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "legacy-vision-checksum.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const earlyChecksum = "c5fe0fa7881cf9100223617b2e3b41f9eaca65f86cd92deef770993bdd72a13e"
	if _, err := store.DB().ExecContext(ctx, `UPDATE schema_migrations SET checksum=? WHERE version=43`, earlyChecksum); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, store.DB()); err != nil {
		t.Fatalf("Migrate() rejected the released migration 43 checksum: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE schema_migrations SET checksum='changed' WHERE version=43`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, store.DB()); err == nil || !strings.Contains(err.Error(), "migration 43 checksum changed") {
		t.Fatalf("Migrate() accepted an unknown migration 43 checksum: %v", err)
	}
}

func TestPythonEnvironmentMigrationChecksumsAreNarrow(t *testing.T) {
	for _, value := range []string{
		"59aac83bba4ebcc4bf7d17a757bd93be29910948e7828fb6031e4914b8e72ea2",
		"1fd30be2634c1e35925f63947bc54b2f26cb9f7bfeac4dc80171525ca331d3d9",
	} {
		if !pythonEnvironmentMigrationChecksum(value) {
			t.Fatalf("pythonEnvironmentMigrationChecksum(%q) = false", value)
		}
	}
	if pythonEnvironmentMigrationChecksum("changed") {
		t.Fatal("unrecognized Python environment migration checksum was accepted")
	}
}

func TestMigrateAcceptsEarlyPythonEnvironmentMigrationChecksum(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "legacy-python-environment-checksum.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const earlyChecksum = "59aac83bba4ebcc4bf7d17a757bd93be29910948e7828fb6031e4914b8e72ea2"
	if _, err := store.DB().ExecContext(ctx, `UPDATE schema_migrations SET checksum=? WHERE version=54`, earlyChecksum); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, store.DB()); err != nil {
		t.Fatalf("Migrate() rejected the released migration 54 checksum: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE schema_migrations SET checksum='changed' WHERE version=54`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, store.DB()); err == nil || !strings.Contains(err.Error(), "migration 54 checksum changed") {
		t.Fatalf("Migrate() accepted an unknown migration 54 checksum: %v", err)
	}
}

func TestP2MigrationPreservesExistingRuns(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY NOT NULL, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations {
		if item.version > 4 {
			break
		}
		if _, err := db.ExecContext(ctx, item.sql); err != nil {
			t.Fatalf("apply fixture migration %d: %v", item.version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, '2026-01-01T00:00:00Z')`, item.version, item.name, item.checksum); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO projects(id,name,description,workspace_path,workspace_kind,created_at,updated_at) VALUES ('project','P2','','C:/fixture','external','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO conversations(id,project_id,title,created_at,updated_at) VALUES ('conversation','project','upgrade','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO model_profiles(id,name,provider_type,base_url,model_id,secret_ref,timeout_seconds,custom_headers_json,enabled,is_default,created_at,updated_at) VALUES ('profile','fixture','openai_compatible','https://example.test/v1','model','secret',60,'{}',1,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO messages(id,conversation_id,run_id,role,status,created_at,updated_at) VALUES ('user','conversation','run','user','complete','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO messages(id,conversation_id,run_id,role,status,created_at,updated_at) VALUES ('assistant','conversation','run','assistant','streaming','2026-01-01T00:00:01Z','2026-01-01T00:00:01Z')`,
		`INSERT INTO runs(id,conversation_id,user_message_id,assistant_message_id,model_profile_id,status,created_at,updated_at,model_id) VALUES ('run','conversation','user','assistant','profile','running','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','model')`,
		`INSERT INTO tool_calls(id,run_id,provider_call_id,tool_name,tool_version,arguments_json,status,risk,permissions_json,idempotent,created_at,updated_at) VALUES ('call','run','provider','builtin.workspace.read','1','{}','awaiting_approval','low','[{"kind":"workspace.read","resource":"paper.md"}]',1,'2026-01-01T00:00:02Z','2026-01-01T00:00:02Z')`,
		`INSERT INTO tool_results(tool_call_id,status,text_content,artifacts_json,citations_json,truncated,meta_json,created_at) VALUES ('call','success','preserved','[]','[]',0,'{}','2026-01-01T00:00:03Z')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("insert upgrade fixture: %v", err)
		}
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate() P2 upgrade: %v", err)
	}
	var status, modelID string
	if err := db.QueryRowContext(ctx, `SELECT status, model_id FROM runs WHERE id='run'`).Scan(&status, &modelID); err != nil {
		t.Fatal(err)
	}
	if status != "running" || modelID != "model" {
		t.Fatalf("preserved run = (%q,%q)", status, modelID)
	}
	var conversationProfileID, conversationModelID string
	if err := db.QueryRowContext(ctx, `SELECT model_profile_id, model_id FROM conversations WHERE id='conversation'`).Scan(&conversationProfileID, &conversationModelID); err != nil {
		t.Fatal(err)
	}
	if conversationProfileID != "profile" || conversationModelID != "model" {
		t.Fatalf("backfilled conversation model selection = (%q,%q)", conversationProfileID, conversationModelID)
	}
	var modelTurns int
	if err := db.QueryRowContext(ctx, `SELECT model_turns FROM runs WHERE id='run'`).Scan(&modelTurns); err != nil || modelTurns != 0 {
		t.Fatalf("model turn checkpoint = %d, %v", modelTurns, err)
	}
	var permissionMode string
	if err := db.QueryRowContext(ctx, `SELECT permission_mode FROM runs WHERE id='run'`).Scan(&permissionMode); err != nil || permissionMode != "plan" {
		t.Fatalf("run permission mode = %q, %v", permissionMode, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT permission_mode FROM conversations WHERE id='conversation'`).Scan(&permissionMode); err != nil || permissionMode != "plan" {
		t.Fatalf("conversation permission mode = %q, %v", permissionMode, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE runs SET status='waiting_approval' WHERE id='run'`); err != nil {
		t.Fatalf("waiting_approval rejected after migration: %v", err)
	}
	var tables int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('tool_calls','tool_results')`).Scan(&tables); err != nil || tables != 2 {
		t.Fatalf("tool tables = %d, %v", tables, err)
	}
	var resultText string
	if err := db.QueryRowContext(ctx, `SELECT text_content FROM tool_results WHERE tool_call_id='call'`).Scan(&resultText); err != nil || resultText != "preserved" {
		t.Fatalf("P2.1 tool result = %q, %v", resultText, err)
	}
	var modelContext string
	var modelContextVersion int
	if err := db.QueryRowContext(ctx, `SELECT model_context_text, model_context_version FROM tool_results WHERE tool_call_id='call'`).Scan(&modelContext, &modelContextVersion); err != nil || modelContext != "" || modelContextVersion != 0 {
		t.Fatalf("legacy model context defaults = (%q, %d), %v", modelContext, modelContextVersion, err)
	}
}

func TestProtocolMigrationDefaultsLegacyRows(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "protocol-upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY NOT NULL, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations {
		if item.version >= 13 {
			break
		}
		if _, err := db.ExecContext(ctx, item.sql); err != nil {
			t.Fatalf("apply migration %d: %v", item.version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES (?,?,?,'2026-01-01T00:00:00Z')`, item.version, item.name, item.checksum); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO projects(id,name,description,workspace_path,workspace_kind,created_at,updated_at) VALUES ('project','Protocol','','C:/fixture','external','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO conversations(id,project_id,title,created_at,updated_at) VALUES ('conversation','project','upgrade','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO model_profiles(id,name,provider_type,base_url,model_id,secret_ref,timeout_seconds,custom_headers_json,enabled,is_default,created_at,updated_at) VALUES ('profile','fixture','openai_compatible','https://example.test/v1','model','secret',60,'{}',1,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO messages(id,conversation_id,run_id,role,status,created_at,updated_at) VALUES ('user','conversation','run','user','complete','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO messages(id,conversation_id,run_id,role,status,created_at,updated_at) VALUES ('assistant','conversation','run','assistant','streaming','2026-01-01T00:00:01Z','2026-01-01T00:00:01Z')`,
		`INSERT INTO runs(id,conversation_id,user_message_id,assistant_message_id,model_profile_id,status,created_at,updated_at,model_id) VALUES ('run','conversation','user','assistant','profile','running','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','model')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var profileProtocol, runProtocol string
	if err := db.QueryRowContext(ctx, `SELECT api_protocol FROM model_profiles WHERE id='profile'`).Scan(&profileProtocol); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT api_protocol FROM runs WHERE id='run'`).Scan(&runProtocol); err != nil {
		t.Fatal(err)
	}
	if profileProtocol != "openai_chat_completions" || runProtocol != "openai_chat_completions" {
		t.Fatalf("legacy defaults = (%q,%q)", profileProtocol, runProtocol)
	}
	if _, err := db.ExecContext(ctx, `UPDATE model_profiles SET api_protocol='invalid' WHERE id='profile'`); err == nil {
		t.Fatal("protocol check accepted invalid profile value")
	}
}

func TestBuiltinSkillSourceMigrationPreservesExistingProvenance(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "skill-source-upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY NOT NULL, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations {
		if item.version > 19 {
			break
		}
		if _, err := db.ExecContext(ctx, item.sql); err != nil {
			t.Fatalf("apply migration %d: %v", item.version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES (?,?,?,'2026-01-01T00:00:00Z')`, item.version, item.name, item.checksum); err != nil {
			t.Fatal(err)
		}
	}
	hash := strings.Repeat("a", 64)
	if _, err := db.ExecContext(ctx, `INSERT INTO installed_skills(skill_id,skill_version,manifest_json,package_rel_path,manifest_hash,content_hash,package_hash,integrity_status,integrity_error,installed_at,updated_at) VALUES ('fixture-skill','1.0.0','{}','fixture-skill/1.0.0',?,?,?,'valid','','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, hash, hash, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO skill_package_sources(skill_id,skill_version,source_kind,source_name,source_hash,archive_rel_path,installed_at,updated_at) VALUES ('fixture-skill','1.0.0','folder','fixture',?,'packages/fixture-skill/1.0.0/source.zip','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, hash); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var kind, name, storedHash string
	if err := db.QueryRowContext(ctx, `SELECT source_kind,source_name,source_hash FROM skill_package_sources WHERE skill_id='fixture-skill'`).Scan(&kind, &name, &storedHash); err != nil {
		t.Fatal(err)
	}
	if kind != "folder" || name != "fixture" || storedHash != hash {
		t.Fatalf("migrated source = (%q,%q,%q)", kind, name, storedHash)
	}
	if _, err := db.ExecContext(ctx, `UPDATE skill_package_sources SET source_kind='builtin' WHERE skill_id='fixture-skill'`); err != nil {
		t.Fatalf("builtin source kind was rejected: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE skill_package_sources SET source_kind='invalid' WHERE skill_id='fixture-skill'`); err == nil {
		t.Fatal("invalid source kind was accepted")
	}
}

func TestReasoningObservationMigrationUpgradesV13Database(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reasoning-observation-upgrade.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY NOT NULL, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations {
		if item.version > 13 {
			break
		}
		if _, err := db.ExecContext(ctx, item.sql); err != nil {
			t.Fatalf("apply migration %d: %v", item.version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES (?,?,?,'2026-01-01T00:00:00Z')`, item.version, item.name, item.checksum); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO model_profiles(id,name,provider_type,api_protocol,base_url,model_id,secret_ref,timeout_seconds,custom_headers_json,enabled,is_default,created_at,updated_at) VALUES ('profile','preserved','openai_compatible','openai_responses','https://example.test/v1','reasoning-model','secret',60,'{}',1,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO model_profile_models(profile_id,model_id,owned_by,enabled,is_default,reasoning_levels_json,reasoning_capability_source,created_at,updated_at) VALUES ('profile','reasoning-model','fixture',1,1,'["low","high"]','manual','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() v13 database: %v", err)
	}
	defer store.Close()

	profile, err := NewModelProfileRepository(store.DB()).Get(ctx, "profile")
	if err != nil {
		t.Fatalf("read migrated profile: %v", err)
	}
	if profile.Name != "preserved" || profile.APIProtocol != "openai_responses" || profile.ModelID != "reasoning-model" {
		t.Fatalf("profile was not preserved: %#v", profile)
	}
	if len(profile.Models) != 1 {
		t.Fatalf("migrated models = %d, want 1", len(profile.Models))
	}
	model := profile.Models[0]
	if len(model.ReasoningLevels) != 2 || model.ReasoningCapabilitySource != "manual" {
		t.Fatalf("existing reasoning capability was not preserved: %#v", model)
	}
	if len(model.ReasoningVerifiedLevels) != 0 || len(model.ReasoningRejectedLevels) != 0 || model.ReasoningControlUnsupported || model.ReasoningLastRequestedLevel != "" || model.ReasoningLastResolvedLevel != "" || model.ReasoningWireMode != "" {
		t.Fatalf("v14 defaults are invalid: %#v", model)
	}
	var applied int
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version=14`).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("migration 14 applied = %d, %v", applied, err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version=15`).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("migration 15 applied = %d, %v", applied, err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version=16`).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("migration 16 applied = %d, %v", applied, err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version=17`).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("migration 17 applied = %d, %v", applied, err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version=18`).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("migration 18 applied = %d, %v", applied, err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version=19`).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("migration 19 applied = %d, %v", applied, err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version=20`).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("migration 20 applied = %d, %v", applied, err)
	}
	var tableName string
	if err := store.DB().QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='provider_turn_items'`).Scan(&tableName); err != nil || tableName != "provider_turn_items" {
		t.Fatalf("provider_turn_items table = %q, %v", tableName, err)
	}
}

func TestP52MigrationPreservesP51IndexVersion(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "p5-2-upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY NOT NULL, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations {
		if item.version > 24 {
			break
		}
		if _, err := db.ExecContext(ctx, item.sql); err != nil {
			t.Fatalf("apply migration %d: %v", item.version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES (?,?,?,'2026-08-19T00:00:00Z')`, item.version, item.name, item.checksum); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO projects(id,name,description,workspace_path,workspace_kind,created_at,updated_at) VALUES ('project','P5.1','','C:/fixture','external','2026-08-19T00:00:00Z','2026-08-19T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO knowledge_index_versions(id,project_id,version_number,schema_version,parser_schema_version,chunking_version,search_kind,storage_relative_path,status,error_message,created_at,activated_at,updated_at) VALUES ('index-v1','project',1,1,1,'unit-v1','lexical_v1','cache/knowledge/index-v1.db','ready','','2026-08-19T00:00:00Z','2026-08-19T00:00:00Z','2026-08-19T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var engine, status, embeddingModel, fingerprint, hybridStrategy string
	var dimensions int
	if err := db.QueryRowContext(ctx, `SELECT retrieval_engine,status,embedding_model,embedding_dimensions,embedding_config_fingerprint,hybrid_strategy FROM knowledge_index_versions WHERE id='index-v1'`).Scan(&engine, &status, &embeddingModel, &dimensions, &fingerprint, &hybridStrategy); err != nil {
		t.Fatal(err)
	}
	if engine != "lexical_scan_v1" || status != "ready" || embeddingModel != "" || dimensions != 0 || fingerprint != "" || hybridStrategy != "bm25_only_v1" {
		t.Fatalf("migrated P5.1 index = engine %q, status %q, embedding %q/%d, strategy %q", engine, status, embeddingModel, dimensions, hybridStrategy)
	}
	var embeddingEnabled int
	if err := db.QueryRowContext(ctx, `SELECT enabled FROM knowledge_embedding_config WHERE id=1`).Scan(&embeddingEnabled); err != nil || embeddingEnabled != 0 {
		t.Fatalf("default Embedding config = %d, %v", embeddingEnabled, err)
	}
}

func TestAttachmentImageFormatMigrationPreservesKnowledgeReferences(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "attachment-image-upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY NOT NULL, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations {
		if item.version >= 40 {
			break
		}
		if _, err := db.ExecContext(ctx, item.sql); err != nil {
			t.Fatalf("apply migration %d: %v", item.version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES (?,?,?,'2026-08-21T00:00:00Z')`, item.version, item.name, item.checksum); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO projects(id,name,description,workspace_path,workspace_kind,created_at,updated_at)
		VALUES ('project','Migration','','C:/fixture','external','2026-08-21T00:00:00Z','2026-08-21T00:00:00Z');
		INSERT INTO attachments(id,project_id,original_name,mime_type,document_format,size_bytes,sha256,storage_relative_path,cache_relative_path,status,unit_count,extracted_runes,truncated,error_message,created_at,updated_at,parse_metadata_json)
		VALUES ('attachment','project','paper.pdf','application/pdf','pdf',10,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','attachments/paper.pdf','cache/paper.json','ready',1,10,0,'','2026-08-21T00:00:00Z','2026-08-21T00:00:00Z','{}');
		INSERT INTO knowledge_index_versions(id,project_id,version_number,schema_version,parser_schema_version,chunking_version,search_kind,storage_relative_path,status,error_message,created_at,activated_at,updated_at)
		VALUES ('index','project',1,1,1,'unit-v1','lexical_v1','cache/knowledge/index-v1.db','ready','','2026-08-21T00:00:00Z','2026-08-21T00:00:00Z','2026-08-21T00:00:00Z');
		INSERT INTO knowledge_documents(id,project_id,attachment_id,index_version_id,title,attachment_sha256,status,parser_schema_version,chunking_version,chunk_count,error_message,created_at,indexed_at,updated_at)
		VALUES ('document','project','attachment','index','Paper','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','ready',1,'unit-v1',1,'','2026-08-21T00:00:00Z','2026-08-21T00:00:00Z','2026-08-21T00:00:00Z');
		INSERT INTO knowledge_import_jobs(id,project_id,document_id,attachment_id,index_version_id,status,stage,attempt_count,error_message,created_at,started_at,completed_at,updated_at)
		VALUES ('job','project','document','attachment','index','completed','completed',1,'','2026-08-21T00:00:00Z','2026-08-21T00:00:00Z','2026-08-21T00:00:00Z','2026-08-21T00:00:00Z');
	`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	for table, id := range map[string]string{"attachments": "attachment", "knowledge_documents": "document", "knowledge_import_jobs": "job"} {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE id=?", id).Scan(&count); err != nil || count != 1 {
			t.Fatalf("preserved %s = %d, %v", table, count, err)
		}
	}
	var foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, %v", foreignKeys, err)
	}
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		_ = rows.Close()
		t.Fatal("migration left a foreign key violation")
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO attachments(id,project_id,original_name,mime_type,document_format,size_bytes,sha256,storage_relative_path,cache_relative_path,status,parse_metadata_json,created_at,updated_at) VALUES ('image','project','figure.jpg','image/webp','image',30,'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','attachments/figure.jpg','cache/figure.json','ready','{"width":"1","height":"1"}','2026-08-21T00:00:00Z','2026-08-21T00:00:00Z')`); err != nil {
		t.Fatalf("insert image after migration: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM attachments WHERE id='attachment'`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"knowledge_documents", "knowledge_import_jobs"} {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("cascade %s = %d, %v", table, count, err)
		}
	}
}

func TestRepairImageAttachmentStatusMigration(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "repair-image-status.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY NOT NULL, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations {
		if item.version > 40 {
			break
		}
		if migrationNeedsForeignKeysDisabled(item) {
			if err := applyMigrationWithForeignKeysDisabled(ctx, db, item); err != nil {
				t.Fatalf("apply migration %d: %v", item.version, err)
			}
			continue
		}
		if _, err := db.ExecContext(ctx, item.sql); err != nil {
			t.Fatalf("apply migration %d: %v", item.version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES (?,?,?,'2026-08-21T00:00:00Z')`, item.version, item.name, item.checksum); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO projects(id,name,description,workspace_path,workspace_kind,created_at,updated_at)
		VALUES ('project','Images','','C:/fixture','external','2026-08-21T00:00:00Z','2026-08-21T00:00:00Z');
		INSERT INTO attachments(id,project_id,original_name,mime_type,document_format,size_bytes,sha256,storage_relative_path,cache_relative_path,status,unit_count,extracted_runes,truncated,parse_metadata_json,error_message,created_at,updated_at)
		VALUES ('image','project','figure.jpg','image/webp','image',30,'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc','attachments/figure.jpg','cache/figure.json','failed',0,0,0,'{"width":"1","height":"1"}','unsupported document format "image"','2026-08-21T00:00:00Z','2026-08-21T00:00:00Z');
	`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var status, errorMessage string
	if err := db.QueryRowContext(ctx, `SELECT status,error_message FROM attachments WHERE id='image'`).Scan(&status, &errorMessage); err != nil || status != "ready" || errorMessage != "" {
		t.Fatalf("repaired image = status %q error %q, %v", status, errorMessage, err)
	}
}

func TestVisionFallbackMigrationRetiresLegacyBuiltinSkill(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "vision-fallback-migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY NOT NULL, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations {
		if item.version > 42 {
			break
		}
		if migrationNeedsForeignKeysDisabled(item) {
			if err := applyMigrationWithForeignKeysDisabled(ctx, db, item); err != nil {
				t.Fatalf("apply migration %d: %v", item.version, err)
			}
			continue
		}
		if _, err := db.ExecContext(ctx, item.sql); err != nil {
			t.Fatalf("apply migration %d: %v", item.version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES (?,?,?,'2026-08-21T00:00:00Z')`, item.version, item.name, item.checksum); err != nil {
			t.Fatal(err)
		}
	}
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := db.ExecContext(ctx, `
		INSERT INTO projects(id,name,description,workspace_path,workspace_kind,created_at,updated_at)
		VALUES ('project','Legacy','','C:/fixture','external','2026-08-21T00:00:00Z','2026-08-21T00:00:00Z');
		INSERT INTO installed_skills(skill_id,skill_version,manifest_json,package_rel_path,manifest_hash,content_hash,package_hash,integrity_status,integrity_error,installed_at,updated_at)
		VALUES ('hello-multimodal','0.3.3','{}','hello-multimodal/0.3.3',?,?,?,'valid','','2026-08-21T00:00:00Z','2026-08-21T00:00:00Z');
		INSERT INTO skill_package_sources(skill_id,skill_version,source_kind,source_name,source_hash,archive_rel_path,installed_at,updated_at)
		VALUES ('hello-multimodal','0.3.3','builtin','hello-multimodal@0.3.3',?,'packages/hello-multimodal/0.3.3/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.zip','2026-08-21T00:00:00Z','2026-08-21T00:00:00Z');
		INSERT INTO project_skills(project_id,skill_id,skill_version,enabled,priority,created_at,updated_at)
		VALUES ('project','hello-multimodal','0.3.3',1,5,'2026-08-21T00:00:00Z','2026-08-21T00:00:00Z');
		INSERT INTO installed_skills(skill_id,skill_version,manifest_json,package_rel_path,manifest_hash,content_hash,package_hash,integrity_status,integrity_error,installed_at,updated_at)
		VALUES ('hello-multimodal','9.0.0','{}','hello-multimodal/9.0.0',?,?,?,'valid','','2026-08-21T00:00:00Z','2026-08-21T00:00:00Z');
		INSERT INTO skill_package_sources(skill_id,skill_version,source_kind,source_name,source_hash,archive_rel_path,installed_at,updated_at)
		VALUES ('hello-multimodal','9.0.0','folder','user-installed',?,'packages/hello-multimodal/9.0.0/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.zip','2026-08-21T00:00:00Z','2026-08-21T00:00:00Z');
	`, hash, hash, hash, hash, hash, hash, hash, hash); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var projectLinks int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM project_skills WHERE skill_id='hello-multimodal'`).Scan(&projectLinks); err != nil || projectLinks != 0 {
		t.Fatalf("legacy project links = %d, %v", projectLinks, err)
	}
	var remainingVersion, remainingSource string
	if err := db.QueryRowContext(ctx, `
		SELECT installed_skills.skill_version, skill_package_sources.source_kind
		FROM installed_skills
		JOIN skill_package_sources USING(skill_id, skill_version)
		WHERE installed_skills.skill_id='hello-multimodal'
	`).Scan(&remainingVersion, &remainingSource); err != nil || remainingVersion != "9.0.0" || remainingSource != "folder" {
		t.Fatalf("preserved user Skill = (%q,%q), %v", remainingVersion, remainingSource, err)
	}
	var retiredPath string
	if err := db.QueryRowContext(ctx, `SELECT package_rel_path FROM retired_builtin_skill_packages`).Scan(&retiredPath); err != nil || retiredPath != "hello-multimodal/0.3.3" {
		t.Fatalf("retired package = %q, %v", retiredPath, err)
	}
}
