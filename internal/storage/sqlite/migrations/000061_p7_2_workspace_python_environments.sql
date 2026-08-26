-- sciaide:foreign_keys_off

ALTER TABLE python_environments
ADD COLUMN environment_kind TEXT NOT NULL DEFAULT 'legacy_managed'
CHECK (environment_kind IN ('legacy_managed','workspace_managed','external'));

CREATE TABLE python_environment_operations_p7_2_workspace (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('create','rebuild','bind','install','delete','verify')),
    state TEXT NOT NULL CHECK (state IN ('running','completed','failed','cancelled')),
    request_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(request_json)),
    before_fingerprint TEXT NOT NULL DEFAULT '',
    after_fingerprint TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    completed_at TEXT,
    error_message TEXT NOT NULL DEFAULT '',
    FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY(environment_id) REFERENCES python_environments(id) ON DELETE CASCADE
);

INSERT INTO python_environment_operations_p7_2_workspace(
    id,project_id,environment_id,kind,state,request_json,before_fingerprint,
    after_fingerprint,started_at,completed_at,error_message
)
SELECT id,project_id,environment_id,kind,state,request_json,before_fingerprint,
    after_fingerprint,started_at,completed_at,error_message
FROM python_environment_operations;

DROP TABLE python_environment_operations;
ALTER TABLE python_environment_operations_p7_2_workspace RENAME TO python_environment_operations;

CREATE INDEX idx_python_environment_operations_project_started
ON python_environment_operations(project_id, started_at DESC, id DESC);
