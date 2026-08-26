CREATE TABLE research_bibliographies (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    candidate_id TEXT NOT NULL REFERENCES research_candidates(id) ON DELETE CASCADE,
    revision_number INTEGER NOT NULL DEFAULT 1 CHECK (revision_number > 0),
    canonical_json TEXT NOT NULL CHECK (
        length(canonical_json) BETWEEN 2 AND 262144
        AND json_valid(canonical_json)
        AND json_type(canonical_json) = 'object'
    ),
    selected_sources_json TEXT NOT NULL DEFAULT '{}' CHECK (
        length(selected_sources_json) BETWEEN 2 AND 65536
        AND json_valid(selected_sources_json)
        AND json_type(selected_sources_json) = 'object'
    ),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (project_id, candidate_id),
    UNIQUE (id, project_id)
);

CREATE TABLE research_bibliography_field_sources (
    id TEXT PRIMARY KEY NOT NULL,
    bibliography_id TEXT NOT NULL REFERENCES research_bibliographies(id) ON DELETE CASCADE,
    field_name TEXT NOT NULL CHECK (field_name IN (
        'authors','year','title','published','container_title','volume','issue','pages',
        'publisher','doi','pmid','pmcid','arxiv','openalex','url','work_type','language'
    )),
    source_record_id TEXT REFERENCES research_source_records(id) ON DELETE SET NULL,
    source_id_snapshot TEXT NOT NULL CHECK (length(trim(source_id_snapshot)) BETWEEN 1 AND 64),
    source_record_id_snapshot TEXT NOT NULL CHECK (length(trim(source_record_id_snapshot)) BETWEEN 1 AND 512),
    value_json TEXT NOT NULL CHECK (
        length(value_json) BETWEEN 1 AND 131072
        AND json_valid(value_json)
    ),
    value_sha256 TEXT NOT NULL CHECK (length(value_sha256) = 64 AND lower(value_sha256) = value_sha256),
    observed_at TEXT NOT NULL,
    UNIQUE (bibliography_id, field_name, source_id_snapshot, source_record_id_snapshot, value_sha256)
);

CREATE TABLE research_bibliography_revisions (
    id TEXT PRIMARY KEY NOT NULL,
    bibliography_id TEXT NOT NULL REFERENCES research_bibliographies(id) ON DELETE CASCADE,
    revision_number INTEGER NOT NULL CHECK (revision_number > 1),
    field_name TEXT NOT NULL CHECK (field_name IN (
        'authors','year','title','published','container_title','volume','issue','pages',
        'publisher','doi','pmid','pmcid','arxiv','openalex','url','work_type','language'
    )),
    previous_json TEXT NOT NULL CHECK (length(previous_json) BETWEEN 1 AND 131072 AND json_valid(previous_json)),
    next_json TEXT NOT NULL CHECK (length(next_json) BETWEEN 1 AND 131072 AND json_valid(next_json)),
    source_kind TEXT NOT NULL CHECK (source_kind IN ('user_edit','source_selection')),
    source_record_id_snapshot TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '' CHECK (length(reason) <= 2000),
    created_at TEXT NOT NULL,
    UNIQUE (bibliography_id, revision_number, field_name)
);

CREATE TABLE research_bibliography_materials (
    id TEXT PRIMARY KEY NOT NULL,
    bibliography_id TEXT NOT NULL REFERENCES research_bibliographies(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    attachment_id TEXT REFERENCES attachments(id) ON DELETE SET NULL,
    knowledge_document_id TEXT REFERENCES knowledge_documents(id) ON DELETE SET NULL,
    attachment_id_snapshot TEXT NOT NULL,
    knowledge_document_id_snapshot TEXT NOT NULL DEFAULT '',
    attachment_sha256_snapshot TEXT NOT NULL CHECK (length(attachment_sha256_snapshot) = 64),
    import_kind TEXT NOT NULL CHECK (import_kind IN ('full_text','metadata_abstract')),
    evidence_level TEXT NOT NULL CHECK (evidence_level IN ('full_text','metadata_abstract')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (bibliography_id, attachment_sha256_snapshot)
);

CREATE UNIQUE INDEX idx_research_bibliography_material_attachment
    ON research_bibliography_materials(project_id, attachment_id)
    WHERE attachment_id IS NOT NULL;

CREATE INDEX idx_research_bibliography_material_document
    ON research_bibliography_materials(project_id, knowledge_document_id)
    WHERE knowledge_document_id IS NOT NULL;

CREATE TABLE research_evidence_entries (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    bibliography_id TEXT NOT NULL REFERENCES research_bibliographies(id) ON DELETE CASCADE,
    field_kind TEXT NOT NULL CHECK (field_kind IN (
        'research_question','method','sample_dataset','finding','limitation','note'
    )),
    content TEXT NOT NULL CHECK (length(trim(content)) BETWEEN 1 AND 20000),
    provenance TEXT NOT NULL CHECK (provenance IN ('user','model')),
    review_status TEXT NOT NULL CHECK (review_status IN ('pending','verified','rejected')),
    evidence_level TEXT NOT NULL CHECK (evidence_level IN ('none','full_text','metadata_abstract')),
    attachment_id TEXT REFERENCES attachments(id) ON DELETE SET NULL,
    knowledge_document_id TEXT REFERENCES knowledge_documents(id) ON DELETE SET NULL,
    attachment_id_snapshot TEXT NOT NULL DEFAULT '',
    knowledge_document_id_snapshot TEXT NOT NULL DEFAULT '',
    index_version_id_snapshot TEXT NOT NULL DEFAULT '',
    chunk_id_snapshot TEXT NOT NULL DEFAULT '',
    source_name_snapshot TEXT NOT NULL DEFAULT '',
    locator_snapshot TEXT NOT NULL DEFAULT '',
    quote_text_snapshot TEXT NOT NULL DEFAULT '' CHECK (length(quote_text_snapshot) <= 64000),
    quote_sha256 TEXT NOT NULL DEFAULT '' CHECK (quote_sha256 = '' OR length(quote_sha256) = 64),
    source_start INTEGER NOT NULL DEFAULT 0 CHECK (source_start >= 0),
    source_end INTEGER NOT NULL DEFAULT 0 CHECK (source_end >= source_start),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX idx_research_bibliographies_project_updated
    ON research_bibliographies(project_id, updated_at DESC, id);
CREATE INDEX idx_research_bibliography_fields_bibliography
    ON research_bibliography_field_sources(bibliography_id, field_name, observed_at, id);
CREATE INDEX idx_research_bibliography_revisions_bibliography
    ON research_bibliography_revisions(bibliography_id, revision_number, field_name);
CREATE INDEX idx_research_evidence_bibliography
    ON research_evidence_entries(bibliography_id, field_kind, created_at, id);

ALTER TABLE message_citations
    ADD COLUMN bibliography_id_snapshot TEXT NOT NULL DEFAULT '';
ALTER TABLE message_citations
    ADD COLUMN bibliography_snapshot_json TEXT NOT NULL DEFAULT '{}' CHECK (
        length(bibliography_snapshot_json) BETWEEN 2 AND 262144
        AND json_valid(bibliography_snapshot_json)
        AND json_type(bibliography_snapshot_json) = 'object'
    );
ALTER TABLE message_citations
    ADD COLUMN evidence_level TEXT NOT NULL DEFAULT '' CHECK (evidence_level IN ('','full_text','metadata_abstract'));

ALTER TABLE artifact_citations
    ADD COLUMN bibliography_id_snapshot TEXT NOT NULL DEFAULT '';
ALTER TABLE artifact_citations
    ADD COLUMN bibliography_snapshot_json TEXT NOT NULL DEFAULT '{}' CHECK (
        length(bibliography_snapshot_json) BETWEEN 2 AND 262144
        AND json_valid(bibliography_snapshot_json)
        AND json_type(bibliography_snapshot_json) = 'object'
    );
ALTER TABLE artifact_citations
    ADD COLUMN evidence_level TEXT NOT NULL DEFAULT '' CHECK (evidence_level IN ('','full_text','metadata_abstract'));

CREATE TRIGGER research_bibliography_field_sources_immutable
BEFORE UPDATE ON research_bibliography_field_sources
BEGIN
    SELECT RAISE(ABORT, 'research bibliography field sources are immutable');
END;

CREATE TRIGGER research_bibliography_revisions_immutable
BEFORE UPDATE ON research_bibliography_revisions
BEGIN
    SELECT RAISE(ABORT, 'research bibliography revisions are immutable');
END;

CREATE TRIGGER research_bibliography_identity_immutable
BEFORE UPDATE OF id,project_id,candidate_id,created_at ON research_bibliographies
BEGIN
    SELECT RAISE(ABORT, 'research bibliography identity is immutable');
END;
