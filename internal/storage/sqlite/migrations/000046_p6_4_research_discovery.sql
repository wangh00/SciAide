CREATE TABLE research_queries (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    query_text TEXT NOT NULL CHECK (length(trim(query_text)) BETWEEN 1 AND 2000),
    query_key TEXT NOT NULL CHECK (length(query_key) = 64 AND lower(query_key) = query_key),
    source_ids_json TEXT NOT NULL CHECK (
        length(source_ids_json) BETWEEN 2 AND 4096
        AND json_valid(source_ids_json)
        AND json_type(source_ids_json) = 'array'
    ),
    limit_per_source INTEGER NOT NULL CHECK (limit_per_source BETWEEN 1 AND 50),
    source_statuses_json TEXT NOT NULL CHECK (
        length(source_statuses_json) BETWEEN 2 AND 65536
        AND json_valid(source_statuses_json)
        AND json_type(source_statuses_json) = 'array'
    ),
    partial INTEGER NOT NULL CHECK (partial IN (0,1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (project_id, query_key)
);

CREATE TABLE research_source_records (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    source_id TEXT NOT NULL CHECK (length(trim(source_id)) BETWEEN 1 AND 64),
    source_record_id TEXT NOT NULL CHECK (length(trim(source_record_id)) BETWEEN 1 AND 512),
    work_json TEXT NOT NULL CHECK (
        length(work_json) BETWEEN 2 AND 262144
        AND json_valid(work_json)
        AND json_type(work_json) = 'object'
    ),
    raw_snapshot_json TEXT NOT NULL DEFAULT '{}' CHECK (
        length(raw_snapshot_json) BETWEEN 2 AND 131072
        AND json_valid(raw_snapshot_json)
        AND json_type(raw_snapshot_json) = 'object'
    ),
    first_seen_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (project_id, source_id, source_record_id)
);

CREATE TABLE research_candidates (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    candidate_key TEXT NOT NULL CHECK (length(candidate_key) = 64 AND lower(candidate_key) = candidate_key),
    review_status TEXT NOT NULL DEFAULT 'pending' CHECK (review_status IN ('pending','included','excluded')),
    exclusion_reason TEXT NOT NULL DEFAULT '' CHECK (length(exclusion_reason) <= 2000),
    note TEXT NOT NULL DEFAULT '' CHECK (length(note) <= 20000),
    import_status TEXT NOT NULL DEFAULT 'not_imported' CHECK (import_status IN ('not_imported','importing','imported','failed')),
    import_kind TEXT NOT NULL DEFAULT '' CHECK (import_kind IN ('','full_text','metadata_abstract')),
    attachment_id TEXT REFERENCES attachments(id) ON DELETE SET NULL,
    import_error TEXT NOT NULL DEFAULT '' CHECK (length(import_error) <= 4000),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (project_id, candidate_key)
);

CREATE TABLE research_candidate_records (
    candidate_id TEXT NOT NULL REFERENCES research_candidates(id) ON DELETE CASCADE,
    source_record_id TEXT NOT NULL REFERENCES research_source_records(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    PRIMARY KEY (candidate_id, source_record_id),
    UNIQUE (source_record_id),
    UNIQUE (candidate_id, ordinal)
);

CREATE TABLE research_candidate_aliases (
    candidate_id TEXT NOT NULL REFERENCES research_candidates(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    alias_kind TEXT NOT NULL CHECK (alias_kind IN ('doi','pmid','arxiv','openalex','title_year')),
    alias_value TEXT NOT NULL CHECK (length(trim(alias_value)) BETWEEN 1 AND 2048),
    created_at TEXT NOT NULL,
    PRIMARY KEY (candidate_id, alias_kind, alias_value)
);

CREATE TABLE research_query_records (
    query_id TEXT NOT NULL REFERENCES research_queries(id) ON DELETE CASCADE,
    source_record_id TEXT NOT NULL REFERENCES research_source_records(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    PRIMARY KEY (query_id, source_record_id),
    UNIQUE (query_id, ordinal)
);

CREATE INDEX idx_research_queries_project_updated
    ON research_queries(project_id, updated_at DESC, id);
CREATE INDEX idx_research_source_records_project_source
    ON research_source_records(project_id, source_id, updated_at DESC, id);
CREATE INDEX idx_research_candidates_project_review
    ON research_candidates(project_id, review_status, updated_at DESC, id);
CREATE INDEX idx_research_candidates_project_import
    ON research_candidates(project_id, import_status, updated_at DESC, id);
CREATE INDEX idx_research_candidate_alias_lookup
    ON research_candidate_aliases(project_id, alias_kind, alias_value, candidate_id);
CREATE INDEX idx_research_query_records_source
    ON research_query_records(source_record_id, query_id);

CREATE TRIGGER research_source_record_identity_immutable
BEFORE UPDATE OF id,project_id,source_id,source_record_id,first_seen_at ON research_source_records
BEGIN
    SELECT RAISE(ABORT, 'research source record identity is immutable');
END;

CREATE TRIGGER research_candidate_identity_immutable
BEFORE UPDATE OF id,project_id,candidate_key,created_at ON research_candidates
BEGIN
    SELECT RAISE(ABORT, 'research candidate identity is immutable');
END;
