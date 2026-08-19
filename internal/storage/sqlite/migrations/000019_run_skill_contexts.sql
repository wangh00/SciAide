-- One immutable Skill snapshot per Run. The full selected instructions are
-- retained here so approval continuation and process-level replay never read
-- a newer package or project selection by accident.
CREATE TABLE run_skill_contexts (
    run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    snapshot_json TEXT NOT NULL
        CHECK (length(snapshot_json) BETWEEN 2 AND 524288)
        CHECK (json_valid(snapshot_json) AND json_type(snapshot_json) = 'object'),
    snapshot_hash TEXT NOT NULL CHECK (length(snapshot_hash) = 64),
    created_at TEXT NOT NULL,
    CHECK (json_extract(snapshot_json, '$.schemaVersion') = schema_version),
    CHECK (json_extract(snapshot_json, '$.runId') = run_id),
    CHECK (json_extract(snapshot_json, '$.projectId') = project_id)
);

CREATE INDEX idx_run_skill_contexts_project_created
    ON run_skill_contexts(project_id, created_at, run_id);
