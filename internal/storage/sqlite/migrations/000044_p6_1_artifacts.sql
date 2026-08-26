CREATE TABLE artifact_blobs (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    sha256 TEXT NOT NULL CHECK (length(sha256) = 64 AND lower(sha256) = sha256),
    size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
    mime_type TEXT NOT NULL CHECK (length(trim(mime_type)) BETWEEN 1 AND 255),
    storage_relative_path TEXT NOT NULL CHECK (
        length(trim(storage_relative_path)) BETWEEN 1 AND 1024
        AND storage_relative_path NOT LIKE '/%'
        AND storage_relative_path NOT LIKE '\\%'
        AND storage_relative_path NOT LIKE '%..%'
    ),
    created_at TEXT NOT NULL,
    UNIQUE (project_id, sha256),
    UNIQUE (project_id, storage_relative_path)
);

CREATE TABLE artifacts (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 200),
    kind TEXT NOT NULL CHECK (kind IN ('document','data','image','code','other')),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','trashed')),
    current_version_id TEXT REFERENCES artifact_versions(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    trashed_at TEXT
);

CREATE TABLE artifact_versions (
    id TEXT PRIMARY KEY NOT NULL,
    artifact_id TEXT NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
    blob_id TEXT NOT NULL REFERENCES artifact_blobs(id) ON DELETE RESTRICT,
    version_number INTEGER NOT NULL CHECK (version_number > 0),
    file_name TEXT NOT NULL CHECK (length(trim(file_name)) BETWEEN 1 AND 255),
    mime_type TEXT NOT NULL CHECK (length(trim(mime_type)) BETWEEN 1 AND 255),
    size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
    sha256 TEXT NOT NULL CHECK (length(sha256) = 64 AND lower(sha256) = sha256),
    source_kind TEXT NOT NULL CHECK (source_kind IN ('assistant_message','workspace_file','tool')),
    source_key TEXT UNIQUE,
    provenance_json TEXT NOT NULL CHECK (
        length(provenance_json) BETWEEN 2 AND 1048576
        AND json_valid(provenance_json)
        AND json_type(provenance_json) = 'object'
    ),
    created_at TEXT NOT NULL,
    UNIQUE (artifact_id, version_number)
);

CREATE TABLE artifact_lineage (
    id TEXT PRIMARY KEY NOT NULL,
    artifact_version_id TEXT NOT NULL REFERENCES artifact_versions(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    relation_kind TEXT NOT NULL CHECK (relation_kind IN ('run','message','tool_call','artifact_version','workspace_file')),
    source_id_snapshot TEXT NOT NULL CHECK (length(trim(source_id_snapshot)) BETWEEN 1 AND 4096),
    source_run_id TEXT REFERENCES runs(id) ON DELETE SET NULL,
    source_message_id TEXT REFERENCES messages(id) ON DELETE SET NULL,
    source_tool_call_id TEXT REFERENCES tool_calls(id) ON DELETE SET NULL,
    source_artifact_version_id TEXT REFERENCES artifact_versions(id) ON DELETE SET NULL,
    label TEXT NOT NULL DEFAULT '',
    metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (
        length(metadata_json) BETWEEN 2 AND 262144
        AND json_valid(metadata_json)
        AND json_type(metadata_json) = 'object'
    ),
    created_at TEXT NOT NULL,
    UNIQUE (artifact_version_id, ordinal)
);

CREATE TABLE artifact_citations (
    id TEXT PRIMARY KEY NOT NULL,
    artifact_version_id TEXT NOT NULL REFERENCES artifact_versions(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    reference_key TEXT NOT NULL,
    source_run_id_snapshot TEXT NOT NULL DEFAULT '',
    source_message_id_snapshot TEXT NOT NULL DEFAULT '',
    source_tool_call_id_snapshot TEXT NOT NULL DEFAULT '',
    index_version_id TEXT NOT NULL DEFAULT '',
    document_id TEXT NOT NULL DEFAULT '',
    attachment_id TEXT NOT NULL DEFAULT '',
    chunk_id TEXT NOT NULL DEFAULT '',
    source_name TEXT NOT NULL,
    mime_type TEXT NOT NULL DEFAULT '',
    locator TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    quote_text TEXT NOT NULL,
    quote_sha256 TEXT NOT NULL CHECK (length(quote_sha256) = 64),
    source_start INTEGER NOT NULL DEFAULT 0 CHECK (source_start >= 0),
    source_end INTEGER NOT NULL DEFAULT 0 CHECK (source_end >= source_start),
    created_at TEXT NOT NULL,
    UNIQUE (artifact_version_id, ordinal),
    UNIQUE (artifact_version_id, reference_key)
);

CREATE INDEX idx_artifacts_project_status_updated
    ON artifacts(project_id, status, updated_at DESC, id);
CREATE INDEX idx_artifact_versions_artifact_version
    ON artifact_versions(artifact_id, version_number DESC);
CREATE INDEX idx_artifact_versions_blob
    ON artifact_versions(blob_id);
CREATE INDEX idx_artifact_lineage_run
    ON artifact_lineage(source_run_id) WHERE source_run_id IS NOT NULL;
CREATE INDEX idx_artifact_lineage_message
    ON artifact_lineage(source_message_id) WHERE source_message_id IS NOT NULL;
CREATE INDEX idx_artifact_lineage_tool
    ON artifact_lineage(source_tool_call_id) WHERE source_tool_call_id IS NOT NULL;

CREATE TRIGGER artifacts_current_version_owner
BEFORE UPDATE OF current_version_id ON artifacts
WHEN NEW.current_version_id IS NOT NULL
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM artifact_versions v
        WHERE v.id = NEW.current_version_id AND v.artifact_id = NEW.id
    ) THEN RAISE(ABORT, 'artifact current version does not belong to artifact') END;
END;

CREATE TRIGGER artifact_versions_immutable
BEFORE UPDATE ON artifact_versions
BEGIN
    SELECT RAISE(ABORT, 'artifact versions are immutable');
END;

CREATE TRIGGER artifact_blobs_immutable
BEFORE UPDATE ON artifact_blobs
BEGIN
    SELECT RAISE(ABORT, 'artifact blobs are immutable');
END;

CREATE TRIGGER artifact_lineage_content_immutable
BEFORE UPDATE OF id,artifact_version_id,ordinal,relation_kind,source_id_snapshot,label,metadata_json,created_at ON artifact_lineage
BEGIN
    SELECT RAISE(ABORT, 'artifact lineage is immutable');
END;

CREATE TRIGGER artifact_lineage_run_link_immutable
BEFORE UPDATE OF source_run_id ON artifact_lineage
WHEN OLD.source_run_id IS NULL OR NEW.source_run_id IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'artifact lineage source link is immutable');
END;

CREATE TRIGGER artifact_lineage_message_link_immutable
BEFORE UPDATE OF source_message_id ON artifact_lineage
WHEN OLD.source_message_id IS NULL OR NEW.source_message_id IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'artifact lineage source link is immutable');
END;

CREATE TRIGGER artifact_lineage_tool_link_immutable
BEFORE UPDATE OF source_tool_call_id ON artifact_lineage
WHEN OLD.source_tool_call_id IS NULL OR NEW.source_tool_call_id IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'artifact lineage source link is immutable');
END;

CREATE TRIGGER artifact_lineage_artifact_link_immutable
BEFORE UPDATE OF source_artifact_version_id ON artifact_lineage
WHEN OLD.source_artifact_version_id IS NULL OR NEW.source_artifact_version_id IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'artifact lineage source link is immutable');
END;

CREATE TRIGGER artifact_citations_immutable
BEFORE UPDATE ON artifact_citations
BEGIN
    SELECT RAISE(ABORT, 'artifact citations are immutable');
END;
