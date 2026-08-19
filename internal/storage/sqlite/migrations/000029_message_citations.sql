CREATE TABLE message_citations (
    id TEXT PRIMARY KEY NOT NULL,
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    tool_call_id TEXT NOT NULL REFERENCES tool_calls(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    reference_key TEXT NOT NULL CHECK (reference_key GLOB '[[]K-[0-9A-F][0-9A-F][0-9A-F][0-9A-F][0-9A-F][0-9A-F][0-9A-F][0-9A-F][0-9A-F][0-9A-F][0-9A-F][0-9A-F]]'),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    index_version_id TEXT NOT NULL,
    document_id TEXT NOT NULL,
    attachment_id TEXT NOT NULL,
    chunk_id TEXT NOT NULL,
    source_name TEXT NOT NULL,
    mime_type TEXT NOT NULL DEFAULT '',
    locator TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    quote_text TEXT NOT NULL,
    quote_sha256 TEXT NOT NULL CHECK (length(quote_sha256) = 64),
    source_start INTEGER NOT NULL DEFAULT 0 CHECK (source_start >= 0),
    source_end INTEGER NOT NULL DEFAULT 0 CHECK (source_end >= source_start),
    created_at TEXT NOT NULL,
    UNIQUE(message_id, reference_key),
    UNIQUE(message_id, ordinal)
);

CREATE INDEX idx_message_citations_message
    ON message_citations(message_id, ordinal);

CREATE INDEX idx_message_citations_run
    ON message_citations(run_id, reference_key);
