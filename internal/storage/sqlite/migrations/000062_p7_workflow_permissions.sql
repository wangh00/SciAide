ALTER TABLE workflow_runs
ADD COLUMN permission_mode TEXT NOT NULL DEFAULT 'plan'
CHECK (permission_mode IN ('plan','full_access'));
