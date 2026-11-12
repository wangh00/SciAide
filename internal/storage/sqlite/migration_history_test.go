package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/project"
)

var releasedWorkbenchHashes = map[int]string{
	95: "0c5f17f877a0ece08721d4b4642b81bc21c190d6ad6c1fa5591d20672a992c93",
	96: "0b18353c32c6315544efba77ef19fb13bcf244a3495716d45ccbd0144be4f2de",
	97: "e017fd9ccdc2350f2ae4a811e7c3d0525cd15eb37bd9b67758ebe229b46c86cf",
	98: "45238222dd08bc15f043b5b53bff05baa8f1318468b7364828338a9650c7d1bd",
}

func releasedWorkbenchSQL(item migration) string {
	if item.version != 98 {
		return strings.TrimSuffix(item.sql, "\n") + "\r\n"
	}
	lines := strings.SplitAfter(item.sql, "\n")
	for i := 0; i < 51; i++ {
		lines[i] = strings.TrimSuffix(lines[i], "\n") + "\r\n"
	}
	return strings.Join(lines, "")
}

func TestRetiredWorkbenchMigrationChecksumPairsAreExact(t *testing.T) {
	items, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		released, ok := releasedWorkbenchHashes[item.version]
		if !ok {
			continue
		}
		digest := sha256.Sum256([]byte(releasedWorkbenchSQL(item)))
		if hex.EncodeToString(digest[:]) != released {
			t.Fatalf("historical SQL bytes changed at %d", item.version)
		}
		if !migrationChecksumAccepted(item, released) {
			t.Fatalf("released checksum rejected at %d", item.version)
		}
		if migrationChecksumAccepted(item, strings.Repeat("0", 64)) || migrationChecksumAccepted(migration{version: item.version, checksum: strings.Repeat("0", 64)}, released) {
			t.Fatalf("unknown SQL accepted at %d", item.version)
		}
	}
	if migrationChecksumAccepted(migration{version: 95, checksum: releasedWorkbenchHashes[95]}, "148926e35e64fca45b5188a40fb1321c96928f5720f99e1bfaa3f0baec0cc25b") {
		t.Fatal("question migration confused with workbench")
	}
}

func seedRetiredWorkbench(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
	 INSERT INTO projects(id,name,description,workspace_path,workspace_kind,created_at,updated_at) VALUES ('p','History','','C:/fixture','external','2026-10-01','2026-10-01');
	 INSERT INTO workbench_tasks VALUES ('wt','p',1,0,'2026-10-01','2026-10-01');
	 INSERT INTO workbench_contracts VALUES ('wt',1,'{"taskId":"wt","goal":"keep historical bytes"}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',NULL,'2026-10-01');
	 INSERT INTO workbench_attempts VALUES ('wa','wt',1,'completed','historical-result','2026-10-01','2026-10-01');
	 INSERT INTO workbench_objects VALUES ('wa','step','report','report','1','{"markdown":"keep"}','{}','2026-10-01');
	 INSERT INTO workbench_execution_results VALUES ('wa','ready','keep','2026-10-01',NULL);
	 INSERT INTO workbench_jobs VALUES ('wj','wt','completed',1,'{"taskId":"wt","attemptId":"wa"}','2026-10-01');
	`)
	if err != nil {
		t.Fatal(err)
	}
}

func databaseRows(t *testing.T, db *sql.DB, table string) [][]any {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM ` + quoteIdentifier(table) + ` ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out [][]any
	for rows.Next() {
		v := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range v {
			ptrs[i] = &v[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertDatabaseHealthy(t *testing.T, db *sql.DB) {
	t.Helper()
	var check string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&check); err != nil || check != "ok" {
		t.Fatal(check, err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestReleasedWorkbenchHistoriesUpgradeAndReopen(t *testing.T) {
	for _, version := range []int{94, 95, 96, 97, 98} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "history.db")
			db, err := openDatabase(ctx, path, false)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err = migrateToVersion(ctx, db, 94); err != nil {
				t.Fatal(err)
			}
			items, _ := loadMigrations()
			for _, item := range items {
				if item.version < 95 || item.version > version {
					continue
				}
				item.sql = releasedWorkbenchSQL(item)
				item.checksum = releasedWorkbenchHashes[item.version]
				if err := applyMigrationTransaction(ctx, db, item, false); err != nil {
					t.Fatal(err)
				}
			}
			if version >= 95 { // data is populated before the lineage-rebuild upgrade.
				if version >= 97 {
					seedRetiredWorkbench(t, db)
				}
			}
			history := databaseRows(t, db, "schema_migrations")
			var before [][]any
			if version >= 97 {
				before = databaseRows(t, db, "workbench_objects")
			}
			if err := Migrate(ctx, db); err != nil {
				t.Fatal(err)
			}
			after := databaseRows(t, db, "schema_migrations")
			if !reflect.DeepEqual(history, after[:len(history)]) {
				t.Fatal("existing migration history rewritten")
			}
			if version >= 97 && !reflect.DeepEqual(before, databaseRows(t, db, "workbench_objects")) {
				t.Fatal("historical objects changed")
			}
			assertDatabaseHealthy(t, db)
			var trigger int
			if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='research_timeline_registration_insert' AND type='trigger'`).Scan(&trigger); err != nil || trigger != 1 {
				t.Fatal("lost timeline trigger", err)
			}
			currentVersion, err := CurrentSchemaVersion()
			if err != nil {
				t.Fatal(err)
			}
			if err := validateArchiveSchemaForVersion(ctx, db, currentVersion); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			store, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if !reflect.DeepEqual(after, databaseRows(t, store.DB(), "schema_migrations")) {
				t.Fatal("reopen changed history")
			}
		})
	}
}

func TestQuestion95CollisionRepairPreservesHistoryAndPendingAnswer(t *testing.T) {
	ctx := context.Background()
	store, run, call := questionStorageFixture(t)
	db := store.DB()
	restoreQuestionCollisionHistory(t, db)
	if _, err := NewRunRepository(db).WaitForUserInput(ctx, run.ID, call.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version BETWEEN 95 AND 98; UPDATE schema_migrations SET version=95,name='000095_conversation_user_input.sql' WHERE version=99; DROP TABLE workbench_jobs; DROP TABLE workbench_execution_results; DROP TABLE workbench_objects; DROP TABLE workbench_attempts; DROP TABLE workbench_contracts; DROP TABLE workbench_tasks`); err != nil {
		t.Fatal(err)
	}
	before := databaseRows(t, db, "runs")
	var checksum, at string
	if err := db.QueryRow(`SELECT checksum,applied_at FROM schema_migrations WHERE version=95`).Scan(&checksum, &at); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, databaseRows(t, db, "runs")) {
		t.Fatal("pending question/run changed")
	}
	var afterChecksum, afterAt string
	if err := db.QueryRow(`SELECT checksum,applied_at FROM schema_migrations WHERE version=99`).Scan(&afterChecksum, &afterAt); err != nil || checksum != afterChecksum || at != afterAt {
		t.Fatal("question history lost", err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	assertDatabaseHealthy(t, db)
}

func TestQuestion95UnknownChecksumCannotBeRepaired(t *testing.T) {
	ctx := context.Background()
	store, _, _ := questionStorageFixture(t)
	db := store.DB()
	restoreQuestionCollisionHistory(t, db)
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version BETWEEN 95 AND 98; UPDATE schema_migrations SET version=95,name='000095_conversation_user_input.sql',checksum='unknown' WHERE version=99`); err != nil {
		t.Fatal(err)
	}
	before := databaseRows(t, db, "schema_migrations")
	if err := Migrate(ctx, db); err == nil {
		t.Fatal("unknown migration was accepted")
	}
	if !reflect.DeepEqual(before, databaseRows(t, db, "schema_migrations")) {
		t.Fatal("failed repair changed history")
	}
}

func TestQuestion95RepairFailureRollsBackAllHistoryAndSchema(t *testing.T) {
	ctx := context.Background()
	store, _, _ := questionStorageFixture(t)
	db := store.DB()
	restoreQuestionCollisionHistory(t, db)
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version BETWEEN 95 AND 98; UPDATE schema_migrations SET version=95,name='000095_conversation_user_input.sql' WHERE version=99; DROP TABLE workbench_jobs; DROP TABLE workbench_execution_results; DROP TABLE workbench_objects; DROP TABLE workbench_attempts; DROP TABLE workbench_contracts; DROP TABLE workbench_tasks`); err != nil {
		t.Fatal(err)
	}
	before := databaseRows(t, db, "schema_migrations")
	items, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for i := range items {
		if items[i].version == 97 {
			items[i].sql = "INVALID SQL;"
		}
	}
	if err := repairUserInputMigrationCollision(ctx, db, items); err == nil {
		t.Fatal("simulated migration failure was ignored")
	}
	if !reflect.DeepEqual(before, databaseRows(t, db, "schema_migrations")) {
		t.Fatal("failed repair partially rewrote history")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name LIKE 'workbench_%'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed repair left partial schema", count, err)
	}
}

func TestRetiredWorkbenchArchiveRewriteAndMergePreserveSnapshots(t *testing.T) {
	ctx := context.Background()
	source, err := Open(ctx, filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	seedRetiredWorkbench(t, source.DB())
	path := filepath.Join(t.TempDir(), "staging.db")
	if err := BackupTo(ctx, source.DB(), path); err != nil {
		t.Fatal(err)
	}
	repo := NewProjectArchiveRepository(source.DB())
	restored := project.Project{ID: "restored", Name: "Restored", WorkspacePath: t.TempDir(), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	_, err = repo.RewriteSnapshot(ctx, path, restored)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := OpenExisting(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	assertDatabaseHealthy(t, staged)
	var body, hash string
	if err := staged.QueryRow(`SELECT snapshot_json,snapshot_hash FROM workbench_contracts`).Scan(&body, &hash); err != nil || body != `{"taskId":"wt","goal":"keep historical bytes"}` || hash != strings.Repeat("a", 64) {
		t.Fatal("immutable historical snapshot changed", err)
	}
	staged.Close()
	if _, err := repo.MergeSnapshot(ctx, path, nil); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"workbench_tasks", "workbench_contracts", "workbench_attempts", "workbench_objects", "workbench_execution_results", "workbench_jobs"} {
		var count int
		if err := source.DB().QueryRow(`SELECT count(*) FROM ` + quoteIdentifier(table)).Scan(&count); err != nil || count != 2 {
			t.Fatalf("%s historical rows lost: %d %v", table, count, err)
		}
	}
	assertDatabaseHealthy(t, source.DB())
}

// Opt-in regression against a consistent user DB copy; never opens or writes
// the production database. The environment variable must point to a backup.
func TestStartupHistoryCopyUpgradePreservesAllExistingRows(t *testing.T) {
	source := os.Getenv("SCIAIDE_TEST_HISTORY_COPY")
	if source == "" {
		t.Skip("set SCIAIDE_TEST_HISTORY_COPY to an isolated SQLite backup")
	}
	ctx := context.Background()
	original, err := OpenExisting(ctx, source, true)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	path := filepath.Join(t.TempDir(), "upgraded.db")
	if err := BackupTo(ctx, original, path); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := original.Query(`SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		before := databaseRows(t, original, table)
		after := databaseRows(t, store.DB(), table)
		if table == "schema_migrations" {
			after = after[:len(before)]
		}
		if table == "runs" && len(after) > 0 && len(before) > 0 && len(after[0]) != len(before[0]) {
			for i := range after {
				if after[i][len(after[i])-1] != "" {
					t.Fatal("unexpected pending marker")
				}
				after[i] = after[i][:len(after[i])-1]
			}
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("existing %s content changed", table)
		}
	}
	assertDatabaseHealthy(t, store.DB())
	version, err := CurrentSchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateArchiveSchemaForVersion(ctx, store.DB(), version); err != nil {
		t.Fatal(err)
	}
	t.Logf("historical backup -> %d, %d tables preserved byte-for-byte", version, len(tables))
}
