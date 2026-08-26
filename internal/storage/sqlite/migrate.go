package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	version  int
	name     string
	sql      string
	checksum string
}

func Migrate(ctx context.Context, db *sql.DB) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	if len(migrations) == 0 {
		return fmt.Errorf("no embedded database migrations")
	}
	return migrateToVersion(ctx, db, migrations[len(migrations)-1].version)
}

func migrateToVersion(ctx context.Context, db *sql.DB, targetVersion int) error {
	if _, err := db.ExecContext(ctx, `
        CREATE TABLE IF NOT EXISTS schema_migrations (
            version INTEGER PRIMARY KEY NOT NULL,
            name TEXT NOT NULL,
            checksum TEXT NOT NULL,
            applied_at TEXT NOT NULL
        )`); err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}

	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	if len(migrations) == 0 || targetVersion <= 0 || targetVersion > migrations[len(migrations)-1].version {
		return fmt.Errorf("unsupported database migration target %d", targetVersion)
	}
	for _, item := range migrations {
		if item.version > targetVersion {
			break
		}
		var existingChecksum string
		err := db.QueryRowContext(ctx,
			"SELECT checksum FROM schema_migrations WHERE version = ?", item.version,
		).Scan(&existingChecksum)
		switch {
		case err == nil:
			if !migrationChecksumAccepted(item, existingChecksum) {
				return fmt.Errorf("migration %d checksum changed", item.version)
			}
			continue
		case err != sql.ErrNoRows:
			return fmt.Errorf("read migration %d: %w", item.version, err)
		}
		if migrationNeedsForeignKeysDisabled(item) {
			if err := applyMigrationWithForeignKeysDisabled(ctx, db, item); err != nil {
				return err
			}
			continue
		}
		if err := applyMigrationTransaction(ctx, db, item, false); err != nil {
			return err
		}
	}
	return nil
}

func migrationChecksumAccepted(item migration, recorded string) bool {
	if recorded == item.checksum {
		return true
	}
	// P0 shipped migration 000001 with one or two trailing LF bytes depending
	// on the Windows checkout. Only the two known equivalent hashes are valid.
	if item.version == 1 && legacyBaselineChecksum(recorded) && legacyBaselineChecksum(item.checksum) {
		return true
	}
	// An early 0.3.0 build applied migration 43 before its retired-Skill
	// predicate was narrowed. The two known forms create the same schema.
	if item.version == 43 && visionFallbackMigrationChecksum(recorded) && visionFallbackMigrationChecksum(item.checksum) {
		return true
	}
	// Early P7.2 development builds embedded migration 54 with one extra
	// trailing LF. Both byte sequences create the same tables and index.
	return item.version == 54 && pythonEnvironmentMigrationChecksum(recorded) && pythonEnvironmentMigrationChecksum(item.checksum)
}

type transactionBeginner interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

func migrationNeedsForeignKeysDisabled(item migration) bool {
	return strings.Contains(item.sql, "-- sciaide:foreign_keys_off")
}

func applyMigrationWithForeignKeysDisabled(ctx context.Context, db *sql.DB, item migration) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open migration %d connection: %w", item.version, err)
	}
	defer conn.Close()
	var foreignKeys int
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("read migration %d foreign key state: %w", item.version, err)
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("disable foreign keys for migration %d: %w", item.version, err)
	}
	applyErr := applyMigrationTransaction(ctx, conn, item, true)
	restoreValue := "OFF"
	if foreignKeys != 0 {
		restoreValue = "ON"
	}
	_, restoreErr := conn.ExecContext(ctx, "PRAGMA foreign_keys="+restoreValue)
	if applyErr != nil {
		return applyErr
	}
	if restoreErr != nil {
		return fmt.Errorf("restore foreign keys after migration %d: %w", item.version, restoreErr)
	}
	var restored int
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&restored); err != nil || restored != foreignKeys {
		return fmt.Errorf("foreign key state was not restored after migration %d", item.version)
	}
	return nil
}

func applyMigrationTransaction(ctx context.Context, beginner transactionBeginner, item migration, validateForeignKeys bool) error {
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", item.version, err)
	}
	if _, err := tx.ExecContext(ctx, item.sql); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("apply migration %d: %w", item.version, err)
	}
	if validateForeignKeys {
		rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("validate migration %d foreign keys: %w", item.version, err)
		}
		if rows.Next() {
			var table, parent string
			var rowID sql.NullInt64
			var foreignKeyID int
			scanErr := rows.Scan(&table, &rowID, &parent, &foreignKeyID)
			_ = rows.Close()
			_ = tx.Rollback()
			if scanErr != nil {
				return fmt.Errorf("read migration %d foreign key violation: %w", item.version, scanErr)
			}
			return fmt.Errorf("migration %d violates foreign key %s[%d] -> %s", item.version, table, foreignKeyID, parent)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			_ = tx.Rollback()
			return fmt.Errorf("validate migration %d foreign keys: %w", item.version, err)
		}
		_ = rows.Close()
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))",
		item.version, item.name, item.checksum,
	); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("record migration %d: %w", item.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", item.version, err)
	}
	return nil
}

func legacyBaselineChecksum(value string) bool {
	switch value {
	case "e9a66fd9fe954e369fb43f68be6a764ed35cbdbbc142bb6c5ec490954e69f3db",
		"ef8938a8fc66c530015ada08c0db37f3300abc1bdcd4166e8142021345287c7f":
		return true
	default:
		return false
	}
}

func visionFallbackMigrationChecksum(value string) bool {
	switch value {
	case "c5fe0fa7881cf9100223617b2e3b41f9eaca65f86cd92deef770993bdd72a13e",
		"c66f00f44c6a5f3fdcec62b9c52491a63b7a17b885c23c585aac014c6ede63f6":
		return true
	default:
		return false
	}
}

func pythonEnvironmentMigrationChecksum(value string) bool {
	switch value {
	case "59aac83bba4ebcc4bf7d17a757bd93be29910948e7828fb6031e4914b8e72ea2",
		"1fd30be2634c1e35925f63947bc54b2f26cb9f7bfeac4dc80171525ca331d3d9":
		return true
	default:
		return false
	}
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	items := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("invalid migration name %q", entry.Name())
		}
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("parse migration version %q: %w", entry.Name(), err)
		}
		contents, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		hash := sha256.Sum256(contents)
		items = append(items, migration{
			version:  version,
			name:     entry.Name(),
			sql:      string(contents),
			checksum: hex.EncodeToString(hash[:]),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].version < items[j].version })
	for i := 1; i < len(items); i++ {
		if items[i-1].version == items[i].version {
			return nil, fmt.Errorf("duplicate migration version %d", items[i].version)
		}
	}
	return items, nil
}

// CurrentSchemaVersion is the latest embedded database migration version.
func CurrentSchemaVersion() (int, error) {
	items, err := loadMigrations()
	if err != nil {
		return 0, err
	}
	if len(items) == 0 {
		return 0, fmt.Errorf("no embedded database migrations")
	}
	return items[len(items)-1].version, nil
}
