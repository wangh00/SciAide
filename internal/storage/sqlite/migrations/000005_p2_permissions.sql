CREATE TABLE approvals (
    id TEXT PRIMARY KEY NOT NULL,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    tool_call_id TEXT NOT NULL REFERENCES tool_calls(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    tool_name TEXT NOT NULL CHECK (length(trim(tool_name)) > 0),
    tool_version TEXT NOT NULL CHECK (length(trim(tool_version)) > 0),
    permission_kind TEXT NOT NULL CHECK (permission_kind IN ('tool.invoke', 'workspace.read', 'workspace.write', 'filesystem.external', 'network.domain', 'process.execute', 'destructive', 'secret.use')),
    resource TEXT NOT NULL DEFAULT '',
    risk TEXT NOT NULL CHECK (risk IN ('low', 'moderate', 'high', 'destructive')),
    status TEXT NOT NULL CHECK (status IN ('pending', 'granted', 'denied', 'expired')),
    requested_scope TEXT NOT NULL CHECK (requested_scope IN ('call', 'run', 'project')),
    resolved_scope TEXT CHECK (resolved_scope IS NULL OR resolved_scope IN ('call', 'run', 'project')),
    reason TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    resolved_at TEXT,
    UNIQUE (tool_call_id, permission_kind, resource),
    CHECK ((status = 'pending' AND resolved_scope IS NULL AND resolved_at IS NULL)
        OR (status <> 'pending' AND resolved_scope IS NOT NULL AND resolved_at IS NOT NULL)),
    CHECK (requested_scope <> 'call' OR resolved_scope IS NULL OR resolved_scope = 'call'),
    CHECK (requested_scope <> 'run' OR resolved_scope IS NULL OR resolved_scope IN ('call', 'run'))
);

CREATE INDEX idx_approvals_run_created ON approvals(run_id, created_at, id);
CREATE UNIQUE INDEX idx_approvals_one_pending_per_call
    ON approvals(tool_call_id) WHERE status = 'pending';

CREATE TABLE permission_grants (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    run_id TEXT REFERENCES runs(id) ON DELETE CASCADE,
    tool_name TEXT NOT NULL CHECK (length(trim(tool_name)) > 0),
    permission_kind TEXT NOT NULL CHECK (permission_kind IN ('tool.invoke', 'workspace.read', 'workspace.write', 'network.domain')),
    resource TEXT NOT NULL DEFAULT '',
    scope TEXT NOT NULL CHECK (scope IN ('run', 'project')),
    granted_by TEXT NOT NULL CHECK (granted_by IN ('user')),
    created_at TEXT NOT NULL,
    expires_at TEXT,
    revoked_at TEXT,
    CHECK ((scope = 'run' AND run_id IS NOT NULL) OR (scope = 'project' AND run_id IS NULL)),
    CHECK (expires_at IS NULL OR expires_at > created_at),
    CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);

CREATE INDEX idx_permission_grants_lookup
    ON permission_grants(project_id, tool_name, permission_kind, resource, scope, revoked_at);
CREATE UNIQUE INDEX idx_permission_grants_active_unique
    ON permission_grants(project_id, COALESCE(run_id, ''), tool_name, permission_kind, resource, scope)
    WHERE revoked_at IS NULL;
