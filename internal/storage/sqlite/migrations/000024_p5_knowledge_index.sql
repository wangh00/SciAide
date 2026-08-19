CREATE UNIQUE INDEX idx_attachments_id_project
    ON attachments(id, project_id);

CREATE TABLE knowledge_index_versions (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    version_number INTEGER NOT NULL CHECK (version_number > 0),
    schema_version INTEGER NOT NULL CHECK (schema_version > 0),
    parser_schema_version INTEGER NOT NULL CHECK (parser_schema_version > 0),
    chunking_version TEXT NOT NULL CHECK (length(trim(chunking_version)) > 0),
    search_kind TEXT NOT NULL CHECK (search_kind IN ('lexical_v1')),
    storage_relative_path TEXT NOT NULL CHECK (length(trim(storage_relative_path)) > 0),
    status TEXT NOT NULL CHECK (status IN ('building','ready','retired','failed')),
    error_message TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    activated_at TEXT,
    updated_at TEXT NOT NULL,
    UNIQUE(project_id, version_number),
    UNIQUE(id, project_id)
);

CREATE UNIQUE INDEX idx_knowledge_index_one_ready
    ON knowledge_index_versions(project_id)
    WHERE status = 'ready';

CREATE UNIQUE INDEX idx_knowledge_index_one_building
    ON knowledge_index_versions(project_id)
    WHERE status = 'building';

CREATE TABLE knowledge_documents (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    attachment_id TEXT NOT NULL,
    index_version_id TEXT NOT NULL,
    title TEXT NOT NULL CHECK (length(trim(title)) > 0),
    attachment_sha256 TEXT NOT NULL CHECK (length(attachment_sha256) = 64),
    status TEXT NOT NULL CHECK (status IN ('pending','indexing','ready','failed')),
    parser_schema_version INTEGER NOT NULL CHECK (parser_schema_version > 0),
    chunking_version TEXT NOT NULL CHECK (length(trim(chunking_version)) > 0),
    chunk_count INTEGER NOT NULL DEFAULT 0 CHECK (chunk_count >= 0),
    error_message TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    indexed_at TEXT,
    updated_at TEXT NOT NULL,
    UNIQUE(project_id, attachment_id),
    UNIQUE(id, project_id),
    FOREIGN KEY (attachment_id, project_id)
        REFERENCES attachments(id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (index_version_id, project_id)
        REFERENCES knowledge_index_versions(id, project_id) ON DELETE CASCADE
);

CREATE INDEX idx_knowledge_documents_project_status
    ON knowledge_documents(project_id, status, updated_at, id);

CREATE TABLE knowledge_import_jobs (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    document_id TEXT NOT NULL,
    attachment_id TEXT NOT NULL,
    index_version_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('queued','running','completed','failed','cancelled')),
    stage TEXT NOT NULL CHECK (stage IN ('queued','loading','chunking','indexing','completed','failed','cancelled')),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    error_message TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    updated_at TEXT NOT NULL,
    UNIQUE(id, project_id),
    FOREIGN KEY (document_id, project_id)
        REFERENCES knowledge_documents(id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (attachment_id, project_id)
        REFERENCES attachments(id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (index_version_id, project_id)
        REFERENCES knowledge_index_versions(id, project_id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_knowledge_jobs_one_active_document
    ON knowledge_import_jobs(document_id)
    WHERE status IN ('queued','running');

CREATE INDEX idx_knowledge_jobs_queue
    ON knowledge_import_jobs(status, created_at, id);
