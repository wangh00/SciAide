-- Preserve the distinction between a user's file and a literature material
-- downloaded by the research pipeline without changing resource ownership.
ALTER TABLE attachments ADD COLUMN source_kind TEXT NOT NULL DEFAULT 'unknown'
    CHECK (source_kind IN ('unknown','user_import','research_import','conversation_upload'));

CREATE INDEX idx_attachments_source_kind
    ON attachments(project_id, scope_kind, research_task_id, source_kind, created_at);
