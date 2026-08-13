package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

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
	var modelTurns int
	if err := db.QueryRowContext(ctx, `SELECT model_turns FROM runs WHERE id='run'`).Scan(&modelTurns); err != nil || modelTurns != 0 {
		t.Fatalf("model turn checkpoint = %d, %v", modelTurns, err)
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
}
