CREATE TABLE python_kernel_executions (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL,
    tool_call_id TEXT NOT NULL UNIQUE,
    run_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    environment_fingerprint TEXT NOT NULL,
    kernel_id TEXT NOT NULL,
    execution_id TEXT NOT NULL UNIQUE,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    status TEXT NOT NULL CHECK (status IN ('success','error','cancelled','timed_out','failed')),
    code_sha256 TEXT NOT NULL CHECK (length(code_sha256) = 64),
    input_sha256_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(input_sha256_json)),
    output_sha256_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(output_sha256_json)),
    stdout_truncated INTEGER NOT NULL DEFAULT 0 CHECK (stdout_truncated IN (0,1)),
    stderr_truncated INTEGER NOT NULL DEFAULT 0 CHECK (stderr_truncated IN (0,1)),
    exception_type TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    completed_at TEXT NOT NULL,
    FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY(tool_call_id) REFERENCES tool_calls(id) ON DELETE CASCADE,
    FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
    FOREIGN KEY(environment_id) REFERENCES python_environments(id) ON DELETE CASCADE
);

CREATE INDEX idx_python_kernel_executions_project_completed
ON python_kernel_executions(project_id, completed_at DESC, id DESC);
