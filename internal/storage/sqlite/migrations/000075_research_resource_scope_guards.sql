-- Resource scope is checked again inside SQLite. Application services still
-- perform the user-facing validation, but direct SQL writers must not be able
-- to attach a resource to another project's research task.

-- Attachments also have a conversation scope. Conversation ownership uses the
-- research_task_id column as an opaque "conversation:<id>" owner and is not a
-- research task relationship.
CREATE TRIGGER research_attachment_scope_guard_insert
BEFORE INSERT ON attachments
BEGIN
    SELECT CASE WHEN NEW.scope_kind='task' AND (
        trim(NEW.research_task_id)='' OR NOT EXISTS (
            SELECT 1 FROM research_tasks t
            WHERE t.id=NEW.research_task_id AND t.project_id=NEW.project_id
              AND t.status<>'archived'
        )
    ) THEN RAISE(ABORT, 'attachment task scope does not belong to project or is archived') END;
    SELECT CASE WHEN NEW.scope_kind IN ('project_shared','legacy_project')
        AND trim(NEW.research_task_id)<>''
        THEN RAISE(ABORT, 'non-task attachment cannot carry research task id') END;
    SELECT CASE WHEN NEW.scope_kind='conversation'
        AND trim(NEW.research_task_id) NOT LIKE 'conversation:%'
        THEN RAISE(ABORT, 'conversation attachment owner is invalid') END;
END;

CREATE TRIGGER research_attachment_scope_guard_update
BEFORE UPDATE ON attachments
BEGIN
    SELECT CASE WHEN NEW.scope_kind='task' AND (
        trim(NEW.research_task_id)='' OR NOT EXISTS (
            SELECT 1 FROM research_tasks t
            WHERE t.id=NEW.research_task_id AND t.project_id=NEW.project_id
              AND t.status<>'archived'
        )
    ) THEN RAISE(ABORT, 'attachment task scope does not belong to project or is archived') END;
    SELECT CASE WHEN NEW.scope_kind IN ('project_shared','legacy_project')
        AND trim(NEW.research_task_id)<>''
        THEN RAISE(ABORT, 'non-task attachment cannot carry research task id') END;
    SELECT CASE WHEN NEW.scope_kind='conversation'
        AND trim(NEW.research_task_id) NOT LIKE 'conversation:%'
        THEN RAISE(ABORT, 'conversation attachment owner is invalid') END;
END;

CREATE TRIGGER research_knowledge_document_scope_guard_insert
BEFORE INSERT ON knowledge_documents
BEGIN
    SELECT CASE WHEN NEW.scope_kind='task' AND (
        trim(NEW.research_task_id)='' OR NOT EXISTS (
            SELECT 1 FROM research_tasks t
            WHERE t.id=NEW.research_task_id AND t.project_id=NEW.project_id
              AND t.status<>'archived'
        )
    ) THEN RAISE(ABORT, 'knowledge document task scope does not belong to project or is archived') END;
    SELECT CASE WHEN NEW.scope_kind IN ('project_shared','legacy_project')
        AND trim(NEW.research_task_id)<>''
        THEN RAISE(ABORT, 'non-task knowledge document cannot carry research task id') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM attachments a
        WHERE a.id=NEW.attachment_id AND a.project_id=NEW.project_id
          AND (a.scope_kind='project_shared' OR a.scope_kind=NEW.scope_kind
               AND a.research_task_id=NEW.research_task_id)
    ) THEN RAISE(ABORT, 'knowledge document attachment is outside its resource scope') END;
END;

CREATE TRIGGER research_knowledge_document_scope_guard_update
BEFORE UPDATE ON knowledge_documents
BEGIN
    SELECT CASE WHEN NEW.scope_kind='task' AND (
        trim(NEW.research_task_id)='' OR NOT EXISTS (
            SELECT 1 FROM research_tasks t
            WHERE t.id=NEW.research_task_id AND t.project_id=NEW.project_id
              AND t.status<>'archived'
        )
    ) THEN RAISE(ABORT, 'knowledge document task scope does not belong to project or is archived') END;
    SELECT CASE WHEN NEW.scope_kind IN ('project_shared','legacy_project')
        AND trim(NEW.research_task_id)<>''
        THEN RAISE(ABORT, 'non-task knowledge document cannot carry research task id') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM attachments a
        WHERE a.id=NEW.attachment_id AND a.project_id=NEW.project_id
          AND (a.scope_kind='project_shared' OR a.scope_kind=NEW.scope_kind
               AND a.research_task_id=NEW.research_task_id)
    ) THEN RAISE(ABORT, 'knowledge document attachment is outside its resource scope') END;
END;

CREATE TRIGGER research_artifact_scope_guard_insert
BEFORE INSERT ON artifacts
BEGIN
    SELECT CASE WHEN NEW.scope_kind='task' AND (
        trim(NEW.research_task_id)='' OR NOT EXISTS (
            SELECT 1 FROM research_tasks t
            WHERE t.id=NEW.research_task_id AND t.project_id=NEW.project_id
              AND t.status<>'archived'
        )
    ) THEN RAISE(ABORT, 'artifact task scope does not belong to project or is archived') END;
    SELECT CASE WHEN NEW.scope_kind IN ('project_shared','legacy_project')
        AND trim(NEW.research_task_id)<>''
        THEN RAISE(ABORT, 'non-task artifact cannot carry research task id') END;
END;

CREATE TRIGGER research_artifact_scope_guard_update
BEFORE UPDATE ON artifacts
BEGIN
    SELECT CASE WHEN NEW.scope_kind='task' AND (
        trim(NEW.research_task_id)='' OR NOT EXISTS (
            SELECT 1 FROM research_tasks t
            WHERE t.id=NEW.research_task_id AND t.project_id=NEW.project_id
              AND t.status<>'archived'
        )
    ) THEN RAISE(ABORT, 'artifact task scope does not belong to project or is archived') END;
    SELECT CASE WHEN NEW.scope_kind IN ('project_shared','legacy_project')
        AND trim(NEW.research_task_id)<>''
        THEN RAISE(ABORT, 'non-task artifact cannot carry research task id') END;
END;

-- A newly-created research run may use its own ID as the durable task ID. The
-- AFTER INSERT trigger from 000073 materializes that task atomically. Every
-- other run (and every update) must reference an existing task in its project.
CREATE TRIGGER research_workflow_run_scope_guard_insert
BEFORE INSERT ON workflow_runs
BEGIN
    SELECT CASE WHEN trim(NEW.research_task_id)<>'' AND NOT (
        NEW.research_task_id=NEW.id OR EXISTS (
            SELECT 1 FROM research_tasks t
            WHERE t.id=NEW.research_task_id AND t.project_id=NEW.project_id
              AND t.status<>'archived'
        )
    ) THEN RAISE(ABORT, 'Workflow run task scope does not belong to project or is archived') END;
END;

CREATE TRIGGER research_workflow_run_scope_guard_update
BEFORE UPDATE ON workflow_runs
BEGIN
    SELECT CASE WHEN trim(NEW.research_task_id)<>'' AND NOT EXISTS (
        SELECT 1 FROM research_tasks t
        WHERE t.id=NEW.research_task_id AND t.project_id=NEW.project_id
          AND t.status<>'archived'
    ) THEN RAISE(ABORT, 'Workflow run task scope does not belong to project') END;
END;

-- 000073's bookkeeping triggers must not reopen an explicitly archived task.
-- Keep the immutable archive decision authoritative while still maintaining
-- latest-run metadata for active tasks.
DROP TRIGGER IF EXISTS research_task_after_workflow_run_update;
CREATE TRIGGER research_task_after_workflow_run_update
AFTER UPDATE OF status, updated_at, research_task_id ON workflow_runs
WHEN trim(NEW.research_task_id)<>''
BEGIN
    UPDATE research_tasks
    SET latest_run_id=NEW.id,
        latest_run_status=NEW.status,
        status=CASE WHEN NEW.status='completed' THEN 'completed'
                    WHEN NEW.status='failed' THEN 'failed'
                    WHEN NEW.status IN ('cancelled','interrupted') THEN 'cancelled'
                    ELSE 'active' END,
        updated_at=NEW.updated_at,
        archived_at=NULL
    WHERE id=NEW.research_task_id AND project_id=NEW.project_id AND status<>'archived';
END;

DROP TRIGGER IF EXISTS research_task_after_workflow_run_delete;
CREATE TRIGGER research_task_after_workflow_run_delete
AFTER DELETE ON workflow_runs
WHEN trim(OLD.research_task_id)<>''
BEGIN
    UPDATE research_tasks
    SET latest_run_id=COALESCE((SELECT wr.id FROM workflow_runs wr
        WHERE wr.project_id=OLD.project_id AND wr.research_task_id=OLD.research_task_id
        ORDER BY wr.updated_at DESC, wr.id DESC LIMIT 1),''),
        latest_run_status=COALESCE((SELECT wr.status FROM workflow_runs wr
        WHERE wr.project_id=OLD.project_id AND wr.research_task_id=OLD.research_task_id
        ORDER BY wr.updated_at DESC, wr.id DESC LIMIT 1),''),
        status=CASE
            WHEN EXISTS (SELECT 1 FROM workflow_runs wr WHERE wr.project_id=OLD.project_id AND wr.research_task_id=OLD.research_task_id AND wr.status NOT IN ('completed','failed','cancelled','interrupted')) THEN 'active'
            WHEN EXISTS (SELECT 1 FROM workflow_runs wr WHERE wr.project_id=OLD.project_id AND wr.research_task_id=OLD.research_task_id AND wr.status='completed') THEN 'completed'
            WHEN EXISTS (SELECT 1 FROM workflow_runs wr WHERE wr.project_id=OLD.project_id AND wr.research_task_id=OLD.research_task_id AND wr.status='failed') THEN 'failed'
            WHEN EXISTS (SELECT 1 FROM workflow_runs wr WHERE wr.project_id=OLD.project_id AND wr.research_task_id=OLD.research_task_id) THEN 'cancelled'
            ELSE 'archived' END,
        updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
        archived_at=CASE WHEN NOT EXISTS (SELECT 1 FROM workflow_runs wr WHERE wr.project_id=OLD.project_id AND wr.research_task_id=OLD.research_task_id)
                         THEN strftime('%Y-%m-%dT%H:%M:%fZ','now') ELSE archived_at END
    WHERE id=OLD.research_task_id AND project_id=OLD.project_id AND status<>'archived';
END;

CREATE TRIGGER research_candidate_task_import_scope_guard_insert
BEFORE INSERT ON research_candidate_task_imports
BEGIN
    SELECT CASE WHEN NOT EXISTS (
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

CREATE TRIGGER research_candidate_task_import_scope_guard_update
BEFORE UPDATE ON research_candidate_task_imports
BEGIN
    SELECT CASE WHEN NOT EXISTS (
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

-- Bibliography metadata is project-wide; only material/evidence snapshots are
-- task-owned. Empty research_task_id retains historical shared imports.
CREATE TRIGGER research_bibliography_material_scope_guard_insert
BEFORE INSERT ON research_bibliography_materials
BEGIN
    SELECT CASE WHEN trim(NEW.research_task_id)<>'' AND NOT EXISTS (
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

CREATE TRIGGER research_bibliography_material_scope_guard_update
BEFORE UPDATE ON research_bibliography_materials
BEGIN
    SELECT CASE WHEN trim(NEW.research_task_id)<>'' AND NOT EXISTS (
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

CREATE TRIGGER research_evidence_scope_guard_insert
BEFORE INSERT ON research_evidence_entries
BEGIN
    SELECT CASE WHEN trim(NEW.research_task_id)<>'' AND NOT EXISTS (
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

CREATE TRIGGER research_evidence_scope_guard_update
BEFORE UPDATE ON research_evidence_entries
BEGIN
    SELECT CASE WHEN trim(NEW.research_task_id)<>'' AND NOT EXISTS (
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
