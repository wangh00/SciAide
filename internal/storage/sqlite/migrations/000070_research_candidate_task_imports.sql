-- A discovered candidate is project-wide metadata. Materialization is owned
-- by a research task and must not be stored on the shared candidate row.
CREATE TABLE research_candidate_task_imports (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    candidate_id TEXT NOT NULL REFERENCES research_candidates(id) ON DELETE CASCADE,
    research_task_id TEXT NOT NULL CHECK (length(trim(research_task_id)) BETWEEN 1 AND 128),
    import_status TEXT NOT NULL DEFAULT 'not_imported' CHECK (import_status IN ('not_imported','importing','imported','failed')),
    import_kind TEXT NOT NULL DEFAULT '' CHECK (import_kind IN ('','full_text','metadata_abstract')),
    attachment_id TEXT REFERENCES attachments(id) ON DELETE SET NULL,
    import_error TEXT NOT NULL DEFAULT '' CHECK (length(import_error) <= 4000),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (project_id, candidate_id, research_task_id)
);

CREATE INDEX idx_research_candidate_task_imports_task
    ON research_candidate_task_imports(project_id, research_task_id, updated_at DESC, id);
CREATE INDEX idx_research_candidate_task_imports_attachment
    ON research_candidate_task_imports(project_id, attachment_id)
    WHERE attachment_id IS NOT NULL;

CREATE TRIGGER research_candidate_task_import_identity_immutable
BEFORE UPDATE OF id,project_id,candidate_id,research_task_id,created_at ON research_candidate_task_imports
BEGIN
    SELECT RAISE(ABORT, 'research candidate task import identity is immutable');
END;
