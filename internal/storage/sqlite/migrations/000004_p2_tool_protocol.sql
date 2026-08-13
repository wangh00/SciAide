-- SQLite cannot extend a CHECK constraint in place, so rebuild runs before
-- tool_calls adds its foreign key.
ALTER TABLE runs RENAME TO runs_p1;
CREATE TABLE runs (
    id TEXT PRIMARY KEY NOT NULL,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    user_message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE RESTRICT,
    assistant_message_id TEXT REFERENCES messages(id) ON DELETE SET NULL,
    model_profile_id TEXT NOT NULL REFERENCES model_profiles(id) ON DELETE RESTRICT,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'waiting_approval', 'completed', 'failed', 'cancelled', 'interrupted')),
    error_code TEXT NOT NULL DEFAULT '', error_message TEXT NOT NULL DEFAULT '',
    input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    finish_reason TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
    started_at TEXT, completed_at TEXT, updated_at TEXT NOT NULL,
    model_id TEXT NOT NULL DEFAULT ''
);
INSERT INTO runs SELECT id, conversation_id, user_message_id, assistant_message_id, model_profile_id, status, error_code, error_message, input_tokens, output_tokens, finish_reason, created_at, started_at, completed_at, updated_at, model_id FROM runs_p1;
DROP TABLE runs_p1;
CREATE INDEX idx_runs_conversation_created ON runs(conversation_id, created_at DESC);
CREATE UNIQUE INDEX idx_runs_one_active_per_conversation
    ON runs(conversation_id) WHERE status IN ('queued', 'running', 'waiting_approval');

CREATE TABLE tool_calls (
    id TEXT PRIMARY KEY NOT NULL,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    provider_call_id TEXT NOT NULL CHECK (length(trim(provider_call_id)) > 0),
    tool_name TEXT NOT NULL CHECK (length(trim(tool_name)) > 0),
    tool_version TEXT NOT NULL CHECK (length(trim(tool_version)) > 0),
    arguments_json TEXT NOT NULL CHECK (json_valid(arguments_json) AND json_type(arguments_json) = 'object'),
    status TEXT NOT NULL CHECK (status IN ('pending', 'awaiting_approval', 'running', 'completed', 'failed', 'denied', 'cancelled', 'interrupted')),
    risk TEXT NOT NULL CHECK (risk IN ('low', 'moderate', 'high', 'destructive')),
    permissions_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(permissions_json) AND json_type(permissions_json) = 'array'),
    idempotent INTEGER NOT NULL DEFAULT 0 CHECK (idempotent IN (0, 1)),
    idempotency_key TEXT,
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    updated_at TEXT NOT NULL,
    UNIQUE (run_id, provider_call_id)
);

CREATE UNIQUE INDEX idx_tool_calls_idempotency_key
    ON tool_calls(run_id, idempotency_key) WHERE idempotency_key IS NOT NULL AND length(idempotency_key) > 0;
CREATE INDEX idx_tool_calls_run_created ON tool_calls(run_id, created_at, id);
CREATE INDEX idx_tool_calls_active ON tool_calls(status) WHERE status IN ('pending', 'awaiting_approval', 'running');

CREATE TABLE tool_results (
    tool_call_id TEXT PRIMARY KEY NOT NULL REFERENCES tool_calls(id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK (status IN ('success', 'error', 'denied', 'cancelled')),
    text_content TEXT NOT NULL DEFAULT '',
    structured_json TEXT CHECK (structured_json IS NULL OR json_valid(structured_json)),
    artifacts_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(artifacts_json) AND json_type(artifacts_json) = 'array'),
    citations_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(citations_json) AND json_type(citations_json) = 'array'),
    truncated INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0, 1)),
    meta_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(meta_json) AND json_type(meta_json) = 'object'),
    created_at TEXT NOT NULL
);
