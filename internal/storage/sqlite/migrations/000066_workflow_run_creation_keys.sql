ALTER TABLE workflow_runs ADD COLUMN creation_key TEXT NOT NULL DEFAULT ''
    CHECK (length(creation_key) <= 300);

CREATE UNIQUE INDEX idx_workflow_runs_project_creation_key
    ON workflow_runs(project_id, creation_key)
    WHERE length(creation_key) > 0;
