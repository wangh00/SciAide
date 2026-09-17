-- Bibliography evidence is immutable per imported attachment. The same work
-- may be materialized independently by multiple research tasks, so the
-- attachment identity is part of the uniqueness boundary.
-- sciaide:foreign_keys_off
CREATE TABLE research_bibliography_materials_v71 (
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
    UNIQUE (bibliography_id, attachment_sha256_snapshot, attachment_id_snapshot)
);

INSERT INTO research_bibliography_materials_v71
SELECT id,bibliography_id,project_id,attachment_id,knowledge_document_id,attachment_id_snapshot,
       knowledge_document_id_snapshot,attachment_sha256_snapshot,import_kind,evidence_level,created_at,updated_at
FROM research_bibliography_materials;
DROP TABLE research_bibliography_materials;
ALTER TABLE research_bibliography_materials_v71 RENAME TO research_bibliography_materials;

CREATE UNIQUE INDEX idx_research_bibliography_material_attachment
    ON research_bibliography_materials(project_id, attachment_id)
    WHERE attachment_id IS NOT NULL;
CREATE INDEX idx_research_bibliography_material_document
    ON research_bibliography_materials(project_id, knowledge_document_id)
    WHERE knowledge_document_id IS NOT NULL;
CREATE INDEX idx_research_bibliography_material_scope
    ON research_bibliography_materials(project_id, bibliography_id, attachment_sha256_snapshot);
