package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wangh00/SciAide/internal/app/resource"
)

type ModelResourceRepository struct{ db *sql.DB }

func NewModelResourceRepository(db *sql.DB) *ModelResourceRepository {
	return &ModelResourceRepository{db: db}
}
func (r *ModelResourceRepository) GetSession(ctx context.Context, runID string) (resource.Session, bool, error) {
	var s resource.Session
	var scope, facts, factsHash, created string
	err := r.db.QueryRowContext(ctx, `SELECT scope_sha256,scope_json,facts_json,facts_sha256,created_at FROM model_resource_sessions WHERE run_id=?`, runID).Scan(&s.ScopeHash, &scope, &facts, &factsHash, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return s, false, nil
	}
	if err != nil {
		return s, false, err
	}
	s.Facts = json.RawMessage(facts)
	if !json.Valid(s.Facts) || resource.Hash(s.Facts) != factsHash {
		return s, false, fmt.Errorf("resource facts integrity mismatch")
	}
	if err = json.Unmarshal([]byte(scope), &s.Scope); err != nil {
		return s, false, err
	}
	s.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return s, false, err
	}
	if s.Scope.RunID != runID || s.ScopeHash != s.Scope.Hash() {
		return s, false, fmt.Errorf("resource session integrity mismatch")
	}
	return s, true, nil
}
func (r *ModelResourceRepository) CreateSession(ctx context.Context, s resource.Session, actions []resource.Action) error {
	if s.ScopeHash != s.Scope.Hash() || s.Scope.RunID == "" || !json.Valid(s.Facts) {
		return fmt.Errorf("invalid resource session")
	}
	scope, err := json.Marshal(s.Scope)
	if err != nil {
		return err
	}
	if len(scope) > 512*1024 || len(s.Facts) > 512*1024 {
		return fmt.Errorf("resource session exceeds storage bound")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A concurrent preparer cannot overwrite an existing immutable session.
	_, err = tx.ExecContext(ctx, `INSERT INTO model_resource_sessions(run_id,scope_sha256,scope_json,facts_json,facts_sha256,created_at) VALUES(?,?,?,?,?,?)`, s.Scope.RunID, s.ScopeHash, string(scope), string(s.Facts), resource.Hash(s.Facts), s.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if err = putResourceActions(ctx, tx, s.Scope, actions); err != nil {
		return err
	}
	return tx.Commit()
}
func (r *ModelResourceRepository) PutActions(ctx context.Context, scope resource.Scope, actions []resource.Action) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var hash string
	if err = tx.QueryRowContext(ctx, `SELECT scope_sha256 FROM model_resource_sessions WHERE run_id=?`, scope.RunID).Scan(&hash); err != nil {
		return err
	}
	if hash != scope.Hash() {
		return fmt.Errorf("resource session scope changed")
	}
	if err = putResourceActions(ctx, tx, scope, actions); err != nil {
		return err
	}
	return tx.Commit()
}
func putResourceActions(ctx context.Context, tx *sql.Tx, scope resource.Scope, actions []resource.Action) error {
	if len(actions) > 128 {
		return fmt.Errorf("resource action batch exceeds bound")
	}
	for _, a := range actions {
		if err := a.Validate(scope); err != nil {
			return err
		}
		raw, err := json.Marshal(a.Seed)
		if err != nil {
			return err
		}
		if len(raw) > 512*1024 {
			return fmt.Errorf("resource action exceeds storage bound")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO model_resource_actions(run_id,action_id,scope_sha256,seed_json,is_root,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT(run_id,action_id) DO UPDATE SET created_at=excluded.created_at`, a.RunID, a.ID, a.ScopeHash, string(raw), a.Root, a.CreatedAt.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
	}
	return nil
}
func scanResourceAction(row interface{ Scan(...any) error }) (resource.Action, error) {
	var a resource.Action
	var seed, created string
	err := row.Scan(&a.RunID, &a.ID, &a.ScopeHash, &seed, &a.Root, &created)
	if err != nil {
		return a, err
	}
	if err = json.Unmarshal([]byte(seed), &a.Seed); err != nil {
		return a, err
	}
	a.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	return a, err
}
func (r *ModelResourceRepository) GetAction(ctx context.Context, runID, id string) (resource.Action, bool, error) {
	a, err := scanResourceAction(r.db.QueryRowContext(ctx, `SELECT run_id,action_id,scope_sha256,seed_json,is_root,created_at FROM model_resource_actions WHERE run_id=? AND action_id=?`, runID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, false, nil
	}
	return a, err == nil, err
}
func (r *ModelResourceRepository) ListActions(ctx context.Context, runID string, limit int) ([]resource.Action, error) {
	if limit < 1 || limit > resource.MaxSessionActions+1 {
		return nil, fmt.Errorf("invalid resource menu limit")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT run_id,action_id,scope_sha256,seed_json,is_root,created_at FROM model_resource_actions WHERE run_id=? ORDER BY is_root DESC,created_at DESC,action_id LIMIT ?`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []resource.Action{}
	for rows.Next() {
		a, err := scanResourceAction(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}
