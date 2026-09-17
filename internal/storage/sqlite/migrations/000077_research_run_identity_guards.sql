-- A Workflow Run is an immutable audit record. Moving it between projects or
-- research tasks would move its tool calls and resources across boundaries.
CREATE TRIGGER research_workflow_run_identity_immutable
BEFORE UPDATE OF id, project_id, research_task_id, created_at ON workflow_runs
BEGIN
    SELECT RAISE(ABORT, 'Workflow Run identity is immutable');
END;

-- The self-ID exception is only for creating a brand-new task. It must not
-- bypass the archived-task check when a replay happens to reuse an old ID.
DROP TRIGGER IF EXISTS research_workflow_run_scope_guard_insert;
CREATE TRIGGER research_workflow_run_scope_guard_insert
BEFORE INSERT ON workflow_runs
BEGIN
    SELECT CASE WHEN trim(NEW.research_task_id)<>'' AND NOT (
        (NEW.research_task_id=NEW.id AND NOT EXISTS (
            SELECT 1 FROM research_tasks t WHERE t.id=NEW.research_task_id
        )) OR EXISTS (
            SELECT 1 FROM research_tasks t
            WHERE t.id=NEW.research_task_id AND t.project_id=NEW.project_id
              AND t.status<>'archived'
        )
    ) THEN RAISE(ABORT, 'Workflow run task scope does not belong to project or is archived') END;
END;

-- Keep display metadata accurate even after an archived task's historical Run
-- is deleted. Archival itself remains authoritative and is never reopened.
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
        status=CASE WHEN status='archived' THEN 'archived'
            WHEN EXISTS (SELECT 1 FROM workflow_runs wr WHERE wr.project_id=OLD.project_id AND wr.research_task_id=OLD.research_task_id AND wr.status NOT IN ('completed','failed','cancelled','interrupted')) THEN 'active'
            WHEN EXISTS (SELECT 1 FROM workflow_runs wr WHERE wr.project_id=OLD.project_id AND wr.research_task_id=OLD.research_task_id AND wr.status='completed') THEN 'completed'
            WHEN EXISTS (SELECT 1 FROM workflow_runs wr WHERE wr.project_id=OLD.project_id AND wr.research_task_id=OLD.research_task_id AND wr.status='failed') THEN 'failed'
            WHEN EXISTS (SELECT 1 FROM workflow_runs wr WHERE wr.project_id=OLD.project_id AND wr.research_task_id=OLD.research_task_id) THEN 'cancelled'
            ELSE 'archived' END,
        updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
        archived_at=CASE WHEN status='archived' THEN archived_at
                         WHEN NOT EXISTS (SELECT 1 FROM workflow_runs wr WHERE wr.project_id=OLD.project_id AND wr.research_task_id=OLD.research_task_id)
                         THEN strftime('%Y-%m-%dT%H:%M:%fZ','now') ELSE archived_at END
    WHERE id=OLD.research_task_id AND project_id=OLD.project_id;
END;
