package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	modernsqlite "modernc.org/sqlite"
)

type onlineBackuper interface {
	NewBackup(string) (*modernsqlite.Backup, error)
}

// BackupTo creates a transactionally consistent SQLite copy using the online
// backup API. The destination must not exist so callers can publish it
// atomically after their own validation and sanitization.
func BackupTo(ctx context.Context, db *sql.DB, destination string) error {
	if db == nil || destination == "" {
		return fmt.Errorf("SQLite backup source and destination are required")
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("SQLite backup destination already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect SQLite backup destination: %w", err)
	}
	connection, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open SQLite backup connection: %w", err)
	}
	defer connection.Close()
	err = connection.Raw(func(driverConnection any) (resultErr error) {
		backuper, ok := driverConnection.(onlineBackuper)
		if !ok {
			return fmt.Errorf("SQLite driver does not expose the online backup API")
		}
		backup, err := backuper.NewBackup(destination)
		if err != nil {
			return err
		}
		defer func() {
			if finishErr := backup.Finish(); resultErr == nil && finishErr != nil {
				resultErr = finishErr
			}
		}()
		for more := true; more; {
			if err := ctx.Err(); err != nil {
				return err
			}
			more, err = backup.Step(256)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("create SQLite backup: %w", err)
	}
	return nil
}
