-- Preserve every schema-1 snapshot byte-for-byte while allowing new Runs to
-- persist the schema-2 coordination decision trail.
CREATE TABLE run_skill_contexts_v2 (
    run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    schema_version INTEGER NOT NULL CHECK (schema_version IN (1,2)),
    snapshot_json TEXT NOT NULL
        CHECK (length(snapshot_json) BETWEEN 2 AND 524288)
        CHECK (json_valid(snapshot_json) AND json_type(snapshot_json) = 'object'),
    snapshot_hash TEXT NOT NULL CHECK (length(snapshot_hash) = 64),
    created_at TEXT NOT NULL,
    CHECK (json_extract(snapshot_json, '$.schemaVersion') = schema_version),
    CHECK (json_extract(snapshot_json, '$.runId') = run_id),
    CHECK (json_extract(snapshot_json, '$.projectId') = project_id)
);

INSERT INTO run_skill_contexts_v2(run_id,project_id,schema_version,snapshot_json,snapshot_hash,created_at)
SELECT run_id,project_id,schema_version,snapshot_json,snapshot_hash,created_at
FROM run_skill_contexts;

DROP TABLE run_skill_contexts;
ALTER TABLE run_skill_contexts_v2 RENAME TO run_skill_contexts;

CREATE INDEX idx_run_skill_contexts_project_created
    ON run_skill_contexts(project_id, created_at, run_id);
