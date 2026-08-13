ALTER TABLE projects ADD COLUMN workspace_path TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN workspace_kind TEXT NOT NULL DEFAULT 'managed'
    CHECK (workspace_kind IN ('managed', 'external'));
CREATE UNIQUE INDEX idx_projects_workspace_path
    ON projects(workspace_path) WHERE length(workspace_path) > 0;

CREATE TABLE model_profile_models (
    profile_id TEXT NOT NULL REFERENCES model_profiles(id) ON DELETE CASCADE,
    model_id TEXT NOT NULL CHECK (length(trim(model_id)) > 0),
    owned_by TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, model_id)
);

INSERT INTO model_profile_models(profile_id, model_id, enabled, is_default, created_at, updated_at)
SELECT id, model_id, enabled, 1, created_at, updated_at FROM model_profiles;

ALTER TABLE runs ADD COLUMN model_id TEXT NOT NULL DEFAULT '';
UPDATE runs
SET model_id = COALESCE((SELECT model_id FROM model_profiles WHERE model_profiles.id = runs.model_profile_id), '')
WHERE model_id = '';
