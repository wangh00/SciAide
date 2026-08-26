package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wangh00/SciAide/internal/app/pythonenv"
)

type PythonEnvironmentRepository struct{ db *sql.DB }

func NewPythonEnvironmentRepository(db *sql.DB) *PythonEnvironmentRepository {
	return &PythonEnvironmentRepository{db: db}
}

func (r *PythonEnvironmentRepository) Get(ctx context.Context, projectID string) (pythonenv.Environment, error) {
	row := r.db.QueryRowContext(ctx, pythonEnvironmentSelect+` WHERE project_id=?`, projectID)
	return scanPythonEnvironment(row)
}

const pythonEnvironmentSelect = `SELECT id,project_id,state,base_executable_path,base_executable_version,base_executable_sha256,
		architecture,implementation,environment_python_path,environment_fingerprint,lock_json,freeze_sha256,
		created_at,updated_at,last_verified_at,error_message,environment_kind FROM python_environments`

type pythonEnvironmentScanner interface{ Scan(...any) error }

func scanPythonEnvironment(row pythonEnvironmentScanner) (pythonenv.Environment, error) {
	var value pythonenv.Environment
	var state, lockJSON, createdAt, updatedAt string
	var verifiedAt sql.NullString
	if err := row.Scan(&value.ID, &value.ProjectID, &state, &value.BaseExecutablePath, &value.BaseExecutableVersion,
		&value.BaseExecutableSHA256, &value.Architecture, &value.Implementation, &value.EnvironmentPythonPath,
		&value.EnvironmentFingerprint, &lockJSON, &value.FreezeSHA256, &createdAt, &updatedAt, &verifiedAt, &value.ErrorMessage, &value.Kind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return pythonenv.Environment{}, pythonenv.ErrEnvironmentNotFound
		}
		return pythonenv.Environment{}, fmt.Errorf("read Python environment: %w", err)
	}
	value.State = pythonenv.State(state)
	if err := json.Unmarshal([]byte(lockJSON), &value.Lock); err != nil {
		return pythonenv.Environment{}, fmt.Errorf("decode Python environment lock: %w", err)
	}
	var err error
	if value.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return pythonenv.Environment{}, err
	}
	if value.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return pythonenv.Environment{}, err
	}
	if verifiedAt.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, verifiedAt.String)
		if parseErr != nil {
			return pythonenv.Environment{}, parseErr
		}
		value.LastVerifiedAt = &parsed
	}
	return value, nil
}

func (r *PythonEnvironmentRepository) List(ctx context.Context) ([]pythonenv.Environment, error) {
	rows, err := r.db.QueryContext(ctx, pythonEnvironmentSelect+` ORDER BY project_id`)
	if err != nil {
		return nil, fmt.Errorf("list Python environments: %w", err)
	}
	defer rows.Close()
	result := make([]pythonenv.Environment, 0)
	for rows.Next() {
		value, err := scanPythonEnvironment(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (r *PythonEnvironmentRepository) ListRunningOperations(ctx context.Context) ([]pythonenv.Operation, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,project_id,environment_id,kind,state,request_json,before_fingerprint,
		after_fingerprint,started_at,completed_at,error_message FROM python_environment_operations WHERE state='running' ORDER BY started_at,id`)
	if err != nil {
		return nil, fmt.Errorf("list running Python environment operations: %w", err)
	}
	defer rows.Close()
	result := make([]pythonenv.Operation, 0)
	for rows.Next() {
		var value pythonenv.Operation
		var started string
		var completed sql.NullString
		if err := rows.Scan(&value.ID, &value.ProjectID, &value.EnvironmentID, &value.Kind, &value.State, &value.RequestJSON,
			&value.BeforeFingerprint, &value.AfterFingerprint, &started, &completed, &value.ErrorMessage); err != nil {
			return nil, err
		}
		parsed, err := time.Parse(time.RFC3339Nano, started)
		if err != nil {
			return nil, err
		}
		value.StartedAt = parsed
		if completed.Valid {
			parsed, err := time.Parse(time.RFC3339Nano, completed.String)
			if err != nil {
				return nil, err
			}
			value.CompletedAt = &parsed
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (r *PythonEnvironmentRepository) Save(ctx context.Context, value pythonenv.Environment) error {
	return savePythonEnvironment(ctx, r.db, value)
}

type pythonEnvironmentExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func savePythonEnvironment(ctx context.Context, execer pythonEnvironmentExecer, value pythonenv.Environment) error {
	lockJSON, err := json.Marshal(value.Lock)
	if err != nil {
		return err
	}
	var verified any
	if value.LastVerifiedAt != nil {
		verified = value.LastVerifiedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err = execer.ExecContext(ctx, `INSERT INTO python_environments(
		id,project_id,state,base_executable_path,base_executable_version,base_executable_sha256,architecture,implementation,
		environment_python_path,environment_fingerprint,lock_json,freeze_sha256,created_at,updated_at,last_verified_at,error_message,environment_kind
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id) DO UPDATE SET
		state=excluded.state,base_executable_path=excluded.base_executable_path,base_executable_version=excluded.base_executable_version,
		base_executable_sha256=excluded.base_executable_sha256,architecture=excluded.architecture,implementation=excluded.implementation,
		environment_python_path=excluded.environment_python_path,environment_fingerprint=excluded.environment_fingerprint,
		lock_json=excluded.lock_json,freeze_sha256=excluded.freeze_sha256,updated_at=excluded.updated_at,
		last_verified_at=excluded.last_verified_at,error_message=excluded.error_message,environment_kind=excluded.environment_kind`,
		value.ID, value.ProjectID, value.State, value.BaseExecutablePath, value.BaseExecutableVersion, value.BaseExecutableSHA256,
		value.Architecture, value.Implementation, value.EnvironmentPythonPath, value.EnvironmentFingerprint, string(lockJSON),
		value.FreezeSHA256, formatTime(value.CreatedAt), formatTime(value.UpdatedAt), verified, value.ErrorMessage, value.Kind)
	if err != nil {
		return fmt.Errorf("save Python environment: %w", err)
	}
	return nil
}

func (r *PythonEnvironmentRepository) Delete(ctx context.Context, projectID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM python_environments WHERE project_id=?`, projectID)
	return err
}

func (r *PythonEnvironmentRepository) CreateOperation(ctx context.Context, value pythonenv.Operation) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO python_environment_operations(
		id,project_id,environment_id,kind,state,request_json,before_fingerprint,after_fingerprint,started_at,completed_at,error_message
	) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, value.ID, value.ProjectID, value.EnvironmentID, value.Kind, value.State, value.RequestJSON,
		value.BeforeFingerprint, value.AfterFingerprint, formatTime(value.StartedAt), nil, value.ErrorMessage)
	if err != nil {
		return fmt.Errorf("create Python environment operation: %w", err)
	}
	return nil
}

func (r *PythonEnvironmentRepository) FinishOperation(ctx context.Context, operationID, state, afterFingerprint, errorMessage string, completedAt time.Time) error {
	return finishPythonEnvironmentOperation(ctx, r.db, operationID, state, afterFingerprint, errorMessage, completedAt)
}

func finishPythonEnvironmentOperation(ctx context.Context, execer pythonEnvironmentExecer, operationID, state, afterFingerprint, errorMessage string, completedAt time.Time) error {
	result, err := execer.ExecContext(ctx, `UPDATE python_environment_operations SET state=?,after_fingerprint=?,completed_at=?,error_message=? WHERE id=? AND state='running'`,
		state, afterFingerprint, formatTime(completedAt), errorMessage, operationID)
	if err != nil {
		return fmt.Errorf("finish Python environment operation: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return fmt.Errorf("Python environment operation transition conflict")
	}
	return nil
}

func (r *PythonEnvironmentRepository) CompleteOperation(ctx context.Context, value pythonenv.Environment, operationID, state, afterFingerprint, errorMessage string, completedAt time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Python environment completion: %w", err)
	}
	defer tx.Rollback()
	if err := savePythonEnvironment(ctx, tx, value); err != nil {
		return err
	}
	if err := finishPythonEnvironmentOperation(ctx, tx, operationID, state, afterFingerprint, errorMessage, completedAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Python environment completion: %w", err)
	}
	return nil
}
