package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, path string) (*Store, error) {
	db, err := openDatabase(ctx, path, false)
	if err != nil {
		return nil, err
	}
	if err := Migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// OpenExisting opens an existing SQLite database without applying SciAide
// migrations. Archive validation uses read-only mode; isolated restore staging
// uses writable mode only after the archive version has been verified.
func OpenExisting(ctx context.Context, path string, readOnly bool) (*sql.DB, error) {
	return openDatabase(ctx, path, readOnly)
}

func openDatabase(ctx context.Context, path string, readOnly bool) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	uriPath := filepath.ToSlash(abs)
	if filepath.VolumeName(abs) != "" && uriPath[0] != '/' {
		// A Windows drive path must be encoded as file:///C:/... rather than
		// file://C:/..., where C: would incorrectly become the URI host.
		uriPath = "/" + uriPath
	}
	query := url.Values{
		"_pragma": []string{
			"foreign_keys(1)",
			"busy_timeout(5000)",
		},
	}
	if readOnly {
		query.Set("mode", "ro")
	} else {
		query["_pragma"] = append(query["_pragma"], "journal_mode(WAL)")
	}
	dsn := (&url.URL{
		Scheme:   "file",
		Path:     uriPath,
		RawQuery: query.Encode(),
	}).String()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A single writer connection is predictable for a local desktop app. Revisit
	// this through an ADR if profiling shows a need for a larger pool.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return db, nil
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Close() error { return s.db.Close() }
