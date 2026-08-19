CREATE TABLE conversation_context_checkpoints (
    id TEXT PRIMARY KEY NOT NULL,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL CHECK (revision > 0),
    through_message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    summary_text TEXT NOT NULL CHECK (length(trim(summary_text)) > 0),
    checkpoint_sha256 TEXT NOT NULL CHECK (length(checkpoint_sha256) = 64),
    source_message_count INTEGER NOT NULL CHECK (source_message_count > 0),
    source_estimated_tokens INTEGER NOT NULL CHECK (source_estimated_tokens > 0),
    model_profile_id TEXT NOT NULL,
    model_id TEXT NOT NULL,
    api_protocol TEXT NOT NULL
        CHECK (api_protocol IN ('openai_chat_completions', 'openai_responses', 'anthropic_messages')),
    created_at TEXT NOT NULL,
    UNIQUE (conversation_id, revision)
);

CREATE INDEX idx_context_checkpoints_latest
    ON conversation_context_checkpoints(conversation_id, revision DESC);
