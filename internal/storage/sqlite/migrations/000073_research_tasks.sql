-- Durable user-facing research task identity. Workflow runs are execution
-- records and may be removed; task-owned resources keep this stable owner.
CREATE TABLE research_tasks (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title TEXT NOT NULL CHECK (length(trim(title)) BETWEEN 1 AND 200),
    research_question TEXT NOT NULL DEFAULT '' CHECK (length(research_question) <= 8000),
    origin_kind TEXT NOT NULL CHECK (origin_kind IN ('ai_route','template')),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','completed','failed','cancelled','archived')),
    latest_run_id TEXT NOT NULL DEFAULT '',
    latest_run_status TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    archived_at TEXT,
    UNIQUE (id, project_id)
);

CREATE INDEX idx_research_tasks_project_updated
    ON research_tasks(project_id, status, updated_at DESC, id);

ALTER TABLE workflow_runs ADD COLUMN research_task_id TEXT NOT NULL DEFAULT '';

UPDATE workflow_runs
SET research_task_id = CASE
    WHEN workflow_purpose='research_starter' THEN id
    WHEN creation_key LIKE 'research-route:%:%' THEN
        substr(substr(creation_key, length('research-route:') + 1), 1,
            instr(substr(creation_key, length('research-route:') + 1), ':') - 1)
    ELSE ''
END
WHERE trim(research_task_id)='';

-- Backfill task metadata from immutable run inputs. Historical runs without a
-- research identity become one task each and remain visible as archived data.
INSERT OR IGNORE INTO research_tasks(
    id, project_id, title, research_question, origin_kind, status,
    latest_run_id, latest_run_status, created_at, updated_at
)
SELECT grouped.research_task_id,
       grouped.project_id,
       substr(COALESCE(NULLIF(grouped.question,''), NULLIF(grouped.workflow_name,''), 'Untitled research task'), 1, 200),
       substr(grouped.question, 1, 8000),
       CASE WHEN grouped.has_starter=1 THEN 'ai_route' ELSE 'template' END,
       CASE WHEN grouped.latest_status='completed' THEN 'completed'
            WHEN grouped.latest_status='failed' THEN 'failed'
            WHEN grouped.latest_status IN ('cancelled','interrupted') THEN 'cancelled'
            ELSE 'active' END,
       grouped.latest_run_id,
       grouped.latest_status,
       grouped.created_at,
       grouped.updated_at
FROM (
    SELECT base.research_task_id,
           base.project_id,
           MAX(base.workflow_name) AS workflow_name,
           COALESCE(MAX(NULLIF(json_extract(base.inputs_json,'$.starter_context.researchIdea'),'')),
                    MAX(NULLIF(json_extract(base.inputs_json,'$.research_goal'),'')), '') AS question,
           MAX(base.workflow_purpose='research_starter') AS has_starter,
           (SELECT latest.id FROM workflow_runs latest
              WHERE latest.project_id=base.project_id AND latest.research_task_id=base.research_task_id
              ORDER BY latest.updated_at DESC, latest.id DESC LIMIT 1) AS latest_run_id,
           (SELECT latest.status FROM workflow_runs latest
              WHERE latest.project_id=base.project_id AND latest.research_task_id=base.research_task_id
              ORDER BY latest.updated_at DESC, latest.id DESC LIMIT 1) AS latest_status,
           MIN(base.created_at) AS created_at,
           MAX(base.updated_at) AS updated_at
    FROM workflow_runs base
    WHERE trim(base.research_task_id)<>''
    GROUP BY base.research_task_id, base.project_id
) grouped;

CREATE INDEX idx_workflow_runs_research_task
    ON workflow_runs(project_id, research_task_id, updated_at DESC, id);

CREATE TRIGGER research_task_after_workflow_run_insert
AFTER INSERT ON workflow_runs
WHEN trim(NEW.research_task_id)<>''
BEGIN
    INSERT INTO research_tasks(
        id, project_id, title, research_question, origin_kind, status,
        latest_run_id, latest_run_status, created_at, updated_at
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
        latest_run_id=excluded.latest_run_id,
        latest_run_status=excluded.latest_run_status,
        status=excluded.status,
        updated_at=excluded.updated_at,
        archived_at=NULL;
END;

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
    WHERE id=NEW.research_task_id AND project_id=NEW.project_id;
END;

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
    WHERE id=OLD.research_task_id AND project_id=OLD.project_id;
END;
