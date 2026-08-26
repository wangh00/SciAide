CREATE TABLE run_skill_routing (
    run_id TEXT PRIMARY KEY NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    prompt_snapshot TEXT NOT NULL
        CHECK (length(prompt_snapshot) BETWEEN 1 AND 262144),
    prompt_hash TEXT NOT NULL
        CHECK (length(prompt_hash) = 64 AND prompt_hash = lower(prompt_hash)),
    created_at TEXT NOT NULL
);

CREATE INDEX idx_run_skill_routing_project_created
    ON run_skill_routing(project_id, created_at, run_id);
