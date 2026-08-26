CREATE TABLE artifact_exports (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    artifact_version_id TEXT NOT NULL REFERENCES artifact_versions(id) ON DELETE CASCADE,
    blob_id TEXT NOT NULL REFERENCES artifact_blobs(id) ON DELETE RESTRICT,
    export_format TEXT NOT NULL CHECK (export_format IN ('docx','pdf')),
    citation_style TEXT NOT NULL CHECK (citation_style IN ('gb_t_7714_2015','apa_7')),
    generator_version TEXT NOT NULL CHECK (length(trim(generator_version)) BETWEEN 1 AND 64),
    file_name TEXT NOT NULL CHECK (length(trim(file_name)) BETWEEN 1 AND 255),
    mime_type TEXT NOT NULL CHECK (length(trim(mime_type)) BETWEEN 1 AND 255),
    size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
    sha256 TEXT NOT NULL CHECK (length(sha256) = 64 AND lower(sha256) = sha256),
    source_sha256 TEXT NOT NULL CHECK (length(source_sha256) = 64 AND lower(source_sha256) = source_sha256),
    created_at TEXT NOT NULL,
    UNIQUE (artifact_version_id, export_format, citation_style, generator_version)
);

CREATE INDEX idx_artifact_exports_version_created
    ON artifact_exports(artifact_version_id, created_at DESC, id);
CREATE INDEX idx_artifact_exports_blob
    ON artifact_exports(blob_id);

CREATE TRIGGER artifact_exports_immutable
BEFORE UPDATE ON artifact_exports
BEGIN
    SELECT RAISE(ABORT, 'artifact exports are immutable');
END;
