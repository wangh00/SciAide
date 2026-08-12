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
	for _, item := range migrations {
		var existingChecksum string
		err := db.QueryRowContext(ctx,
			"SELECT checksum FROM schema_migrations WHERE version = ?", item.version,
		).Scan(&existingChecksum)
		switch {
		case err == nil:
			if existingChecksum != item.checksum {
				// P0 shipped migration 000001 with one or two trailing LF bytes
				// depending on the Windows checkout that built the binary. The SQL
				// is byte-for-byte equivalent after trimming trailing newlines.
				// Accept only these recorded legacy hashes; all other mutations fail.
				if item.version == 1 && legacyBaselineChecksum(existingChecksum) && legacyBaselineChecksum(item.checksum) {
					continue
				}
				return fmt.Errorf("migration %d checksum changed", item.version)
			}
			continue
		case err != sql.ErrNoRows:
			return fmt.Errorf("read migration %d: %w", item.version, err)
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", item.version, err)
		}
		if _, err := tx.ExecContext(ctx, item.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", item.version, err)
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
