CREATE TABLE process_execution_audits (
    tool_call_id TEXT PRIMARY KEY NOT NULL REFERENCES tool_calls(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    tool_name TEXT NOT NULL,
    executable_path TEXT NOT NULL,
    executable_version TEXT NOT NULL,
    executable_sha256 TEXT NOT NULL CHECK (length(executable_sha256) = 64 AND executable_sha256 = lower(executable_sha256)),
    script_path TEXT NOT NULL DEFAULT '',
    script_sha256 TEXT NOT NULL DEFAULT '' CHECK (script_sha256 = '' OR (length(script_sha256) = 64 AND script_sha256 = lower(script_sha256))),
    command_sha256 TEXT NOT NULL DEFAULT '' CHECK (command_sha256 = '' OR (length(command_sha256) = 64 AND command_sha256 = lower(command_sha256))),
    workdir TEXT NOT NULL,
    timeout_millis INTEGER NOT NULL CHECK (timeout_millis > 0),
    environment_names_json TEXT NOT NULL CHECK (json_valid(environment_names_json) AND json_type(environment_names_json) = 'array'),
    process_id INTEGER,
    state TEXT NOT NULL CHECK (state IN ('prepared','running','completed','failed')),
    termination_reason TEXT CHECK (termination_reason IS NULL OR termination_reason IN ('completed','exit_nonzero','timed_out','cancelled','app_shutdown','start_failed')),
    exit_code INTEGER,
    stdout_bytes INTEGER NOT NULL DEFAULT 0 CHECK (stdout_bytes >= 0),
    stdout_sha256 TEXT NOT NULL DEFAULT '' CHECK (stdout_sha256 = '' OR (length(stdout_sha256) = 64 AND stdout_sha256 = lower(stdout_sha256))),
    stdout_truncated INTEGER NOT NULL DEFAULT 0 CHECK (stdout_truncated IN (0,1)),
    stderr_bytes INTEGER NOT NULL DEFAULT 0 CHECK (stderr_bytes >= 0),
    stderr_sha256 TEXT NOT NULL DEFAULT '' CHECK (stderr_sha256 = '' OR (length(stderr_sha256) = 64 AND stderr_sha256 = lower(stderr_sha256))),
    stderr_truncated INTEGER NOT NULL DEFAULT 0 CHECK (stderr_truncated IN (0,1)),
    error_message TEXT NOT NULL DEFAULT '',
    prepared_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT
);

CREATE INDEX idx_process_execution_audits_project_prepared
    ON process_execution_audits(project_id, prepared_at, tool_call_id);

CREATE INDEX idx_process_execution_audits_run
    ON process_execution_audits(run_id, prepared_at, tool_call_id);
