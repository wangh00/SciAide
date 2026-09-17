-- Foreign-key ON DELETE SET NULL clears bibliography/evidence references when
-- a knowledge document or attachment is removed. Those internal cleanup
-- updates must not be mistaken for a new write into an archived task.
-- Normal scope changes and all non-cleanup updates remain guarded.

DROP TRIGGER IF EXISTS research_candidate_task_import_scope_guard_update;
CREATE TRIGGER research_candidate_task_import_scope_guard_update
BEFORE UPDATE ON research_candidate_task_imports
BEGIN
    SELECT CASE WHEN NOT (
        OLD.project_id=NEW.project_id
        AND OLD.candidate_id=NEW.candidate_id
        AND OLD.research_task_id=NEW.research_task_id
        AND OLD.attachment_id IS NOT NULL AND NEW.attachment_id IS NULL
        AND NOT EXISTS (SELECT 1 FROM attachments a WHERE a.id=OLD.attachment_id AND a.project_id=OLD.project_id)
    ) AND NOT EXISTS (
        SELECT 1 FROM research_tasks t
        WHERE t.id=NEW.research_task_id AND t.project_id=NEW.project_id
          AND t.status<>'archived'
    ) THEN RAISE(ABORT, 'candidate import task does not belong to project or is archived') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM research_candidates c
        WHERE c.id=NEW.candidate_id AND c.project_id=NEW.project_id
    ) THEN RAISE(ABORT, 'candidate does not belong to project') END;
    SELECT CASE WHEN NEW.attachment_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM attachments a
        WHERE a.id=NEW.attachment_id AND a.project_id=NEW.project_id
          AND (a.scope_kind='project_shared' OR a.scope_kind='task' AND a.research_task_id=NEW.research_task_id)
    ) THEN RAISE(ABORT, 'candidate import attachment is outside task scope') END;
END;

DROP TRIGGER IF EXISTS research_bibliography_material_scope_guard_update;
CREATE TRIGGER research_bibliography_material_scope_guard_update
BEFORE UPDATE ON research_bibliography_materials
BEGIN
    SELECT CASE WHEN NOT (
        OLD.project_id IS NEW.project_id
        AND OLD.bibliography_id IS NEW.bibliography_id
        AND OLD.research_task_id IS NEW.research_task_id
        AND (NEW.attachment_id IS NULL OR NEW.attachment_id IS OLD.attachment_id)
        AND (NEW.knowledge_document_id IS NULL OR NEW.knowledge_document_id IS OLD.knowledge_document_id)
        AND (OLD.attachment_id IS NOT NEW.attachment_id OR OLD.knowledge_document_id IS NOT NEW.knowledge_document_id)
        AND (
            (OLD.attachment_id IS NOT NEW.attachment_id AND OLD.attachment_id IS NOT NULL
             AND NOT EXISTS (SELECT 1 FROM attachments a WHERE a.id=OLD.attachment_id AND a.project_id=OLD.project_id))
            OR (OLD.knowledge_document_id IS NOT NEW.knowledge_document_id AND OLD.knowledge_document_id IS NOT NULL
             AND NOT EXISTS (SELECT 1 FROM knowledge_documents d WHERE d.id=OLD.knowledge_document_id AND d.project_id=OLD.project_id))
        )
    ) AND trim(NEW.research_task_id)<>'' AND NOT EXISTS (
        SELECT 1 FROM research_tasks t
        WHERE t.id=NEW.research_task_id AND t.project_id=NEW.project_id
          AND t.status<>'archived'
    ) THEN RAISE(ABORT, 'bibliography material task does not belong to project or is archived') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM research_bibliographies b
        WHERE b.id=NEW.bibliography_id AND b.project_id=NEW.project_id
    ) THEN RAISE(ABORT, 'bibliography does not belong to project') END;
    SELECT CASE WHEN NEW.attachment_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM attachments a
        WHERE a.id=NEW.attachment_id AND a.project_id=NEW.project_id
          AND (a.scope_kind IN ('project_shared','legacy_project') OR trim(NEW.research_task_id)<>'' AND a.scope_kind='task' AND a.research_task_id=NEW.research_task_id)
    ) THEN RAISE(ABORT, 'bibliography material attachment is outside task scope') END;
END;

DROP TRIGGER IF EXISTS research_evidence_scope_guard_update;
CREATE TRIGGER research_evidence_scope_guard_update
BEFORE UPDATE ON research_evidence_entries
BEGIN
    SELECT CASE WHEN NOT (
        OLD.project_id IS NEW.project_id
        AND OLD.bibliography_id IS NEW.bibliography_id
        AND OLD.research_task_id IS NEW.research_task_id
        AND (NEW.attachment_id IS NULL OR NEW.attachment_id IS OLD.attachment_id)
        AND (NEW.knowledge_document_id IS NULL OR NEW.knowledge_document_id IS OLD.knowledge_document_id)
        AND (OLD.attachment_id IS NOT NEW.attachment_id OR OLD.knowledge_document_id IS NOT NEW.knowledge_document_id)
        AND (
            (OLD.attachment_id IS NOT NEW.attachment_id AND OLD.attachment_id IS NOT NULL
             AND NOT EXISTS (SELECT 1 FROM attachments a WHERE a.id=OLD.attachment_id AND a.project_id=OLD.project_id))
            OR (OLD.knowledge_document_id IS NOT NEW.knowledge_document_id AND OLD.knowledge_document_id IS NOT NULL
             AND NOT EXISTS (SELECT 1 FROM knowledge_documents d WHERE d.id=OLD.knowledge_document_id AND d.project_id=OLD.project_id))
        )
    ) AND trim(NEW.research_task_id)<>'' AND NOT EXISTS (
        SELECT 1 FROM research_tasks t
        WHERE t.id=NEW.research_task_id AND t.project_id=NEW.project_id
          AND t.status<>'archived'
    ) THEN RAISE(ABORT, 'evidence task does not belong to project or is archived') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM research_bibliographies b
        WHERE b.id=NEW.bibliography_id AND b.project_id=NEW.project_id
    ) THEN RAISE(ABORT, 'evidence bibliography does not belong to project') END;
    SELECT CASE WHEN NEW.attachment_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM attachments a
        WHERE a.id=NEW.attachment_id AND a.project_id=NEW.project_id
          AND (a.scope_kind IN ('project_shared','legacy_project') OR trim(NEW.research_task_id)<>'' AND a.scope_kind='task' AND a.research_task_id=NEW.research_task_id)
    ) THEN RAISE(ABORT, 'evidence attachment is outside task scope') END;
END;
