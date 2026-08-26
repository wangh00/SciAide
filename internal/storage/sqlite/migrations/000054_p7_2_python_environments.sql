CREATE TABLE python_environments (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL UNIQUE,
    state TEXT NOT NULL CHECK (state IN ('absent','creating','ready','broken','deleting')),
    base_executable_path TEXT NOT NULL DEFAULT '',
    base_executable_version TEXT NOT NULL DEFAULT '',
    base_executable_sha256 TEXT NOT NULL DEFAULT '',
    architecture TEXT NOT NULL DEFAULT '',
    implementation TEXT NOT NULL DEFAULT '',
    environment_python_path TEXT NOT NULL DEFAULT '',
    environment_fingerprint TEXT NOT NULL DEFAULT '',
    lock_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(lock_json)),
    freeze_sha256 TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    last_verified_at TEXT,
    error_message TEXT NOT NULL DEFAULT '',
    FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE TABLE python_environment_operations (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('create','rebuild','install','delete','verify')),
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

CREATE INDEX idx_python_environment_operations_project_started
ON python_environment_operations(project_id, started_at DESC, id DESC);
