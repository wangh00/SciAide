CREATE TABLE conversations (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title TEXT NOT NULL CHECK (length(trim(title)) > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX idx_conversations_project_updated
    ON conversations(project_id, updated_at DESC);

CREATE TABLE messages (
    id TEXT PRIMARY KEY NOT NULL,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    run_id TEXT,
    role TEXT NOT NULL CHECK (role IN ('system', 'user', 'assistant', 'tool')),
    status TEXT NOT NULL CHECK (status IN ('complete', 'streaming', 'incomplete', 'failed')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX idx_messages_conversation_created
    ON messages(conversation_id, created_at, id);

CREATE TABLE message_parts (
    id TEXT PRIMARY KEY NOT NULL,
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    part_type TEXT NOT NULL CHECK (part_type IN ('text', 'media', 'tool_call', 'tool_result')),
    text_content TEXT NOT NULL DEFAULT '',
    payload_json TEXT CHECK (payload_json IS NULL OR json_valid(payload_json)),
    created_at TEXT NOT NULL,
    UNIQUE (message_id, ordinal)
);

CREATE TABLE model_profiles (
    id TEXT PRIMARY KEY NOT NULL,
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    provider_type TEXT NOT NULL CHECK (provider_type IN ('openai_compatible')),
    base_url TEXT NOT NULL CHECK (length(trim(base_url)) > 0),
    model_id TEXT NOT NULL CHECK (length(trim(model_id)) > 0),
    secret_ref TEXT NOT NULL UNIQUE,
    timeout_seconds INTEGER NOT NULL DEFAULT 60 CHECK (timeout_seconds BETWEEN 5 AND 600),
    temperature REAL,
    max_output_tokens INTEGER CHECK (max_output_tokens IS NULL OR max_output_tokens > 0),
    custom_headers_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(custom_headers_json)),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX idx_model_profiles_one_default
    ON model_profiles(is_default) WHERE is_default = 1;

CREATE TABLE runs (
    id TEXT PRIMARY KEY NOT NULL,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    user_message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE RESTRICT,
    assistant_message_id TEXT REFERENCES messages(id) ON DELETE SET NULL,
    model_profile_id TEXT NOT NULL REFERENCES model_profiles(id) ON DELETE RESTRICT,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'completed', 'failed', 'cancelled', 'interrupted')),
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    finish_reason TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    updated_at TEXT NOT NULL
);

CREATE INDEX idx_runs_conversation_created
    ON runs(conversation_id, created_at DESC);
CREATE UNIQUE INDEX idx_runs_one_active_per_conversation
    ON runs(conversation_id) WHERE status IN ('queued', 'running');

CREATE TABLE settings (
    key TEXT PRIMARY KEY NOT NULL,
    value_json TEXT NOT NULL CHECK (json_valid(value_json)),
    updated_at TEXT NOT NULL
);
