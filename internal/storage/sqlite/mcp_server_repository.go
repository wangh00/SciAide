package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wangh00/SciAide/internal/app/mcpserver"
)

type MCPServerRepository struct{ db *sql.DB }

func NewMCPServerRepository(db *sql.DB) *MCPServerRepository { return &MCPServerRepository{db: db} }

func (r *MCPServerRepository) Save(ctx context.Context, value mcpserver.Server) error {
	var conflictingID string
	err := r.db.QueryRowContext(ctx, `SELECT id FROM mcp_servers WHERE namespace=? AND id<>?`, value.Namespace, value.ID).Scan(&conflictingID)
	if err == nil {
		return fmt.Errorf("MCP namespace %q is already used by another server", value.Namespace)
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("check MCP namespace: %w", err)
	}
	args, err := json.Marshal(value.Args)
	if err != nil {
		return err
	}
	headers, err := json.Marshal(value.Headers)
	if err != nil {
		return err
	}
	env, err := json.Marshal(value.Env)
	if err != nil {
		return err
	}
	secretEnv, err := json.Marshal(value.SecretEnv)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO mcp_servers(id,name,namespace,transport,command,args_json,working_dir,url,headers_json,env_json,secret_env_json,enabled,auto_start,trust,timeout_seconds,status,protocol_version,server_version,tool_count,resource_count,prompt_count,last_error,last_connected_at,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,namespace=excluded.namespace,transport=excluded.transport,command=excluded.command,args_json=excluded.args_json,working_dir=excluded.working_dir,url=excluded.url,headers_json=excluded.headers_json,env_json=excluded.env_json,secret_env_json=excluded.secret_env_json,enabled=excluded.enabled,auto_start=excluded.auto_start,trust=excluded.trust,timeout_seconds=excluded.timeout_seconds,status=excluded.status,updated_at=excluded.updated_at`,
		value.ID, value.Name, value.Namespace, value.Transport, value.Command, string(args), value.WorkingDir, value.URL, string(headers), string(env), string(secretEnv), value.Enabled, value.AutoStart, value.Trust, value.TimeoutSeconds, value.Status, value.ProtocolVersion, value.ServerVersion, value.ToolCount, value.ResourceCount, value.PromptCount, value.LastError, nullableTime(value.LastConnectedAt), formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert MCP server: %w", err)
	}
	return nil
}

func (r *MCPServerRepository) Get(ctx context.Context, id string) (mcpserver.Server, error) {
	return scanMCPServer(r.db.QueryRowContext(ctx, mcpServerSelect+` WHERE id=?`, id))
}

func (r *MCPServerRepository) List(ctx context.Context) ([]mcpserver.Server, error) {
	rows, err := r.db.QueryContext(ctx, mcpServerSelect+` ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]mcpserver.Server, 0)
	for rows.Next() {
		value, err := scanMCPServer(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *MCPServerRepository) NamespaceOwner(ctx context.Context, namespace string) (string, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `SELECT id FROM mcp_servers WHERE namespace=?`, namespace).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("check MCP namespace: %w", err)
	}
	return id, nil
}

func (r *MCPServerRepository) Delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM mcp_servers WHERE id=?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("MCP server not found")
	}
	return nil
}

func (r *MCPServerRepository) UpdateRuntime(ctx context.Context, id string, status mcpserver.Status, protocolVersion, serverVersion string, toolCount, resourceCount, promptCount int, lastError string, connectedAt *time.Time, updatedAt time.Time) error {
	result, err := r.db.ExecContext(ctx, `UPDATE mcp_servers SET status=?,protocol_version=?,server_version=?,tool_count=?,resource_count=?,prompt_count=?,last_error=?,last_connected_at=?,updated_at=? WHERE id=?`, status, protocolVersion, serverVersion, toolCount, resourceCount, promptCount, lastError, nullableTime(connectedAt), formatTime(updatedAt), id)
	if err != nil {
		return fmt.Errorf("update MCP runtime: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("MCP server not found")
	}
	return nil
}

func scanMCPServer(row rowScanner) (mcpserver.Server, error) {
	var value mcpserver.Server
	var args, headers, env, secretEnv, created, updated string
	var lastConnectedNullable sql.NullString
	if err := row.Scan(&value.ID, &value.Name, &value.Namespace, &value.Transport, &value.Command, &args, &value.WorkingDir, &value.URL, &headers, &env, &secretEnv, &value.Enabled, &value.AutoStart, &value.Trust, &value.TimeoutSeconds, &value.Status, &value.ProtocolVersion, &value.ServerVersion, &value.ToolCount, &value.ResourceCount, &value.PromptCount, &value.LastError, &lastConnectedNullable, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return value, fmt.Errorf("MCP server not found")
		}
		return value, err
	}
	if err := json.Unmarshal([]byte(args), &value.Args); err != nil {
		return value, err
	}
	if err := json.Unmarshal([]byte(headers), &value.Headers); err != nil {
		return value, err
	}
	if err := json.Unmarshal([]byte(env), &value.Env); err != nil {
		return value, err
	}
	if err := json.Unmarshal([]byte(secretEnv), &value.SecretEnv); err != nil {
		return value, err
	}
	var err error
	value.CreatedAt, err = parseTime(created)
	if err != nil {
		return value, err
	}
	value.UpdatedAt, err = parseTime(updated)
	if err != nil {
		return value, err
	}
	if lastConnectedNullable.Valid {
		at, err := parseTime(lastConnectedNullable.String)
		if err != nil {
			return value, err
		}
		value.LastConnectedAt = &at
	}
	return value, nil
}

const mcpServerSelect = `SELECT id,name,namespace,transport,command,args_json,working_dir,url,headers_json,env_json,secret_env_json,enabled,auto_start,trust,timeout_seconds,status,protocol_version,server_version,tool_count,resource_count,prompt_count,last_error,last_connected_at,created_at,updated_at FROM mcp_servers`
