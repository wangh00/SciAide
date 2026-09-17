-- Research task identity is durable. It must never be moved between projects
-- or silently reopened after archival.
CREATE TRIGGER research_task_identity_immutable
BEFORE UPDATE OF id,project_id,created_at ON research_tasks
BEGIN
    SELECT RAISE(ABORT, 'research task identity is immutable');
END;

CREATE TRIGGER research_task_archive_immutable
BEFORE UPDATE OF status ON research_tasks
WHEN OLD.status='archived' AND NEW.status<>'archived'
BEGIN
    SELECT RAISE(ABORT, 'archived research task cannot be reopened');
END;

-- A workflow task can have a planning Run and one or more formal Runs. The
-- latest Run, rather than the row currently being updated, is authoritative
-- for the task's status and display pointer.
DROP TRIGGER IF EXISTS research_task_after_workflow_run_update;
CREATE TRIGGER research_task_after_workflow_run_update
AFTER UPDATE OF status, updated_at, research_task_id ON workflow_runs
WHEN trim(NEW.research_task_id)<>''
BEGIN
    UPDATE research_tasks
    SET latest_run_id=COALESCE((SELECT wr.id FROM workflow_runs wr
            WHERE wr.project_id=NEW.project_id AND wr.research_task_id=NEW.research_task_id
            ORDER BY wr.updated_at DESC, wr.id DESC LIMIT 1),''),
        latest_run_status=COALESCE((SELECT wr.status FROM workflow_runs wr
            WHERE wr.project_id=NEW.project_id AND wr.research_task_id=NEW.research_task_id
            ORDER BY wr.updated_at DESC, wr.id DESC LIMIT 1),''),
        status=CASE COALESCE((SELECT wr.status FROM workflow_runs wr
            WHERE wr.project_id=NEW.project_id AND wr.research_task_id=NEW.research_task_id
            ORDER BY wr.updated_at DESC, wr.id DESC LIMIT 1),'')
            WHEN 'completed' THEN 'completed'
            WHEN 'failed' THEN 'failed'
            WHEN 'cancelled' THEN 'cancelled'
            WHEN 'interrupted' THEN 'cancelled'
            ELSE 'active' END,
        updated_at=COALESCE((SELECT wr.updated_at FROM workflow_runs wr
            WHERE wr.project_id=NEW.project_id AND wr.research_task_id=NEW.research_task_id
            ORDER BY wr.updated_at DESC, wr.id DESC LIMIT 1),NEW.updated_at)
    WHERE id=NEW.research_task_id AND project_id=NEW.project_id AND status<>'archived';
END;

-- The insert trigger must not turn an explicitly archived task back into an
-- active one when an old creation key is replayed.
DROP TRIGGER IF EXISTS research_task_after_workflow_run_insert;
CREATE TRIGGER research_task_after_workflow_run_insert
AFTER INSERT ON workflow_runs
WHEN trim(NEW.research_task_id)<>''
BEGIN
    INSERT INTO research_tasks(
        id,project_id,title,research_question,origin_kind,status,
        latest_run_id,latest_run_status,created_at,updated_at
    ) VALUES (
        NEW.research_task_id,
        NEW.project_id,
        substr(COALESCE(NULLIF(json_extract(NEW.inputs_json,'$.starter_context.researchIdea'),''),
                        NULLIF(json_extract(NEW.inputs_json,'$.research_goal'),''),
                        NULLIF(NEW.workflow_name,''), 'Untitled research task'), 1, 200),
        substr(COALESCE(NULLIF(json_extract(NEW.inputs_json,'$.starter_context.researchIdea'),''),
                        NULLIF(json_extract(NEW.inputs_json,'$.research_goal'),''), ''), 1, 8000),
        CASE WHEN NEW.workflow_purpose='research_starter' THEN 'ai_route' ELSE 'template' END,
        CASE WHEN NEW.status='completed' THEN 'completed'
             WHEN NEW.status='failed' THEN 'failed'
             WHEN NEW.status IN ('cancelled','interrupted') THEN 'cancelled'
             ELSE 'active' END,
        NEW.id, NEW.status, NEW.created_at, NEW.updated_at
    )
    ON CONFLICT(id) DO UPDATE SET
        latest_run_id=COALESCE((SELECT wr.id FROM workflow_runs wr
            WHERE wr.project_id=excluded.project_id AND wr.research_task_id=excluded.id
            ORDER BY wr.updated_at DESC, wr.id DESC LIMIT 1),excluded.latest_run_id),
        latest_run_status=COALESCE((SELECT wr.status FROM workflow_runs wr
            WHERE wr.project_id=excluded.project_id AND wr.research_task_id=excluded.id
            ORDER BY wr.updated_at DESC, wr.id DESC LIMIT 1),excluded.latest_run_status),
        status=CASE COALESCE((SELECT wr.status FROM workflow_runs wr
            WHERE wr.project_id=excluded.project_id AND wr.research_task_id=excluded.id
            ORDER BY wr.updated_at DESC, wr.id DESC LIMIT 1),excluded.latest_run_status)
                    WHEN 'completed' THEN 'completed'
                    WHEN 'failed' THEN 'failed'
                    WHEN 'cancelled' THEN 'cancelled'
                    WHEN 'interrupted' THEN 'cancelled'
                    ELSE 'active' END,
        updated_at=COALESCE((SELECT wr.updated_at FROM workflow_runs wr
            WHERE wr.project_id=excluded.project_id AND wr.research_task_id=excluded.id
            ORDER BY wr.updated_at DESC, wr.id DESC LIMIT 1),excluded.updated_at)
    WHERE research_tasks.project_id=excluded.project_id AND research_tasks.status<>'archived';
END;
