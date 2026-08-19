CREATE TABLE attachments (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    original_name TEXT NOT NULL CHECK (length(trim(original_name)) > 0),
    mime_type TEXT NOT NULL CHECK (length(trim(mime_type)) > 0),
    document_format TEXT NOT NULL CHECK (document_format IN ('text','markdown','csv','pdf','docx','xlsx')),
    size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
    sha256 TEXT NOT NULL CHECK (length(sha256) = 64),
    storage_relative_path TEXT NOT NULL CHECK (length(trim(storage_relative_path)) > 0),
    cache_relative_path TEXT NOT NULL CHECK (length(trim(cache_relative_path)) > 0),
    status TEXT NOT NULL CHECK (status IN ('parsing','ready','failed')),
    unit_count INTEGER NOT NULL DEFAULT 0 CHECK (unit_count >= 0),
    extracted_runes INTEGER NOT NULL DEFAULT 0 CHECK (extracted_runes >= 0),
    truncated INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0,1)),
    error_message TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(project_id, sha256)
);

CREATE INDEX idx_attachments_project_created
    ON attachments(project_id, created_at, id);
