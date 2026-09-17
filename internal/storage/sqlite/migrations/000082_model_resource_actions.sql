-- Ephemeral capabilities tied to a Chat Run, never a project-wide bearer token.
-- Deleting the run removes the catalog and all issued actions.
CREATE TABLE model_resource_sessions (
 run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
 scope_sha256 TEXT NOT NULL CHECK(length(scope_sha256)=64),
 scope_json TEXT NOT NULL CHECK(json_valid(scope_json)),
 facts_json TEXT NOT NULL CHECK(json_valid(facts_json)),
 facts_sha256 TEXT NOT NULL CHECK(length(facts_sha256)=64),
 created_at TEXT NOT NULL
);
CREATE TABLE model_resource_actions (
 run_id TEXT NOT NULL REFERENCES model_resource_sessions(run_id) ON DELETE CASCADE,
 action_id TEXT NOT NULL,
 scope_sha256 TEXT NOT NULL CHECK(length(scope_sha256)=64),
 seed_json TEXT NOT NULL CHECK(json_valid(seed_json)),
 is_root INTEGER NOT NULL CHECK(is_root IN (0,1)),
 created_at TEXT NOT NULL,
 PRIMARY KEY(run_id,action_id)
);
CREATE INDEX idx_model_resource_actions_recent ON model_resource_actions(run_id,created_at DESC);
