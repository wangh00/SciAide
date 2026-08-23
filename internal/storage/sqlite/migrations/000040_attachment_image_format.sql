-- sciaide:foreign_keys_off
-- SQLite cannot extend a CHECK constraint in place. Foreign keys are disabled
-- on the migration connection while the parent table is replaced, then
-- foreign_key_check runs inside the transaction before it can commit.
CREATE TABLE attachments_v40 (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    original_name TEXT NOT NULL CHECK (length(trim(original_name)) > 0),
    mime_type TEXT NOT NULL CHECK (length(trim(mime_type)) > 0),
    document_format TEXT NOT NULL CHECK (document_format IN ('text','markdown','csv','pdf','docx','xlsx','image')),
    size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
    sha256 TEXT NOT NULL CHECK (length(sha256) = 64),
    storage_relative_path TEXT NOT NULL CHECK (length(trim(storage_relative_path)) > 0),
    cache_relative_path TEXT NOT NULL CHECK (length(trim(cache_relative_path)) > 0),
    status TEXT NOT NULL CHECK (status IN ('parsing','ready','failed')),
    unit_count INTEGER NOT NULL DEFAULT 0 CHECK (unit_count >= 0),
    extracted_runes INTEGER NOT NULL DEFAULT 0 CHECK (extracted_runes >= 0),
    truncated INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0,1)),
    parse_metadata_json TEXT NOT NULL DEFAULT '{}'
        CHECK (json_valid(parse_metadata_json) AND json_type(parse_metadata_json) = 'object'),
    error_message TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(project_id, sha256)
);

INSERT INTO attachments_v40(
    id,project_id,original_name,mime_type,document_format,size_bytes,sha256,
    storage_relative_path,cache_relative_path,status,unit_count,extracted_runes,
    truncated,parse_metadata_json,error_message,created_at,updated_at
)
SELECT
    id,project_id,original_name,mime_type,document_format,size_bytes,sha256,
    storage_relative_path,cache_relative_path,status,unit_count,extracted_runes,
    truncated,parse_metadata_json,error_message,created_at,updated_at
FROM attachments;

DROP TABLE attachments;
ALTER TABLE attachments_v40 RENAME TO attachments;

CREATE INDEX idx_attachments_project_created
    ON attachments(project_id, created_at, id);
CREATE UNIQUE INDEX idx_attachments_id_project
    ON attachments(id, project_id);
