ALTER TABLE workflows ADD COLUMN purpose TEXT NOT NULL DEFAULT 'user_plan'
    CHECK (purpose IN ('user_plan','research_starter'));

CREATE INDEX idx_workflows_project_purpose_updated
    ON workflows(project_id, purpose, updated_at DESC, id);

ALTER TABLE workflow_runs ADD COLUMN workflow_name TEXT NOT NULL DEFAULT ''
    CHECK (length(workflow_name) <= 120);

ALTER TABLE workflow_runs ADD COLUMN workflow_purpose TEXT NOT NULL DEFAULT 'user_plan'
    CHECK (workflow_purpose IN ('user_plan','research_starter'));

UPDATE workflow_runs
SET workflow_name=COALESCE((
    SELECT name FROM workflows WHERE workflows.id=workflow_runs.workflow_id
), '科研任务'),
workflow_purpose=COALESCE((
    SELECT purpose FROM workflows WHERE workflows.id=workflow_runs.workflow_id
), 'user_plan');
