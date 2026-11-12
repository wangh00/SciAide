-- New workbench identities deliberately do not reference legacy research_tasks.
CREATE TABLE workbench_tasks (
    id TEXT PRIMARY KEY NOT NULL CHECK (length(trim(id)) > 0),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    current_revision INTEGER NOT NULL DEFAULT 0 CHECK (current_revision >= 0),
    archived INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0,1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (project_id, id)
);
CREATE TABLE workbench_contracts (
    task_id TEXT NOT NULL REFERENCES workbench_tasks(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL CHECK (revision > 0),
    snapshot_json TEXT NOT NULL CHECK (json_valid(snapshot_json)),
    snapshot_hash TEXT NOT NULL CHECK (length(snapshot_hash) = 64),
    confirmed_at TEXT,
    created_at TEXT NOT NULL,
    PRIMARY KEY (task_id, revision)
);
CREATE TRIGGER workbench_contract_snapshot_immutable
BEFORE UPDATE OF task_id, revision, snapshot_json, snapshot_hash, created_at ON workbench_contracts
BEGIN SELECT RAISE(ABORT, 'workbench contract snapshot is immutable'); END;
CREATE TABLE workbench_attempts (
    id TEXT PRIMARY KEY NOT NULL CHECK (length(trim(id)) > 0),
    task_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('reserved','completed','superseded','cancelled')),
    result_ref TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    finished_at TEXT,
    CHECK (
        (state = 'reserved' AND result_ref = '' AND finished_at IS NULL) OR
        (state = 'completed' AND length(trim(result_ref)) > 0 AND finished_at IS NOT NULL) OR
        (state IN ('superseded','cancelled') AND result_ref = '' AND finished_at IS NOT NULL)
    ),
    FOREIGN KEY (task_id, revision) REFERENCES workbench_contracts(task_id, revision) ON DELETE CASCADE
);
CREATE UNIQUE INDEX workbench_one_active_attempt ON workbench_attempts(task_id) WHERE state = 'reserved';
CREATE INDEX workbench_tasks_project ON workbench_tasks(project_id, updated_at);
CREATE TRIGGER workbench_attempt_identity_immutable
BEFORE UPDATE OF id, task_id, revision, created_at ON workbench_attempts
BEGIN SELECT RAISE(ABORT, 'workbench attempt identity is immutable'); END;
CREATE TRIGGER workbench_attempt_terminal_immutable
BEFORE UPDATE ON workbench_attempts WHEN OLD.state != 'reserved'
BEGIN SELECT RAISE(ABORT, 'workbench terminal attempt is immutable'); END;
