-- Resource ownership is explicit. Existing rows cannot be safely attributed to
-- a historical task, so they remain visible only as legacy project material.
ALTER TABLE attachments ADD COLUMN scope_kind TEXT NOT NULL DEFAULT 'legacy_project'
    CHECK (scope_kind IN ('task','project_shared','legacy_project'));
ALTER TABLE attachments ADD COLUMN research_task_id TEXT NOT NULL DEFAULT '';

ALTER TABLE knowledge_documents ADD COLUMN scope_kind TEXT NOT NULL DEFAULT 'legacy_project'
    CHECK (scope_kind IN ('task','project_shared','legacy_project'));
ALTER TABLE knowledge_documents ADD COLUMN research_task_id TEXT NOT NULL DEFAULT '';

ALTER TABLE artifacts ADD COLUMN scope_kind TEXT NOT NULL DEFAULT 'legacy_project'
    CHECK (scope_kind IN ('task','project_shared','legacy_project'));
ALTER TABLE artifacts ADD COLUMN research_task_id TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_attachments_scope ON attachments(project_id, scope_kind, research_task_id, created_at);
CREATE INDEX idx_knowledge_documents_scope ON knowledge_documents(project_id, scope_kind, research_task_id, updated_at);
CREATE INDEX idx_artifacts_scope ON artifacts(project_id, scope_kind, research_task_id, updated_at);
