package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
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
