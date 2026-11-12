CREATE TABLE workbench_objects (
 attempt_id TEXT NOT NULL REFERENCES workbench_attempts(id) ON DELETE CASCADE,
 step_key TEXT NOT NULL,
 output_name TEXT NOT NULL,
 kind TEXT NOT NULL,
 schema_version TEXT NOT NULL,
 body_json TEXT NOT NULL CHECK(json_valid(body_json)),
 checks_json TEXT NOT NULL CHECK(json_valid(checks_json)),
 created_at TEXT NOT NULL,
 PRIMARY KEY(attempt_id,step_key,output_name)
);
CREATE TRIGGER workbench_objects_immutable BEFORE UPDATE ON workbench_objects
BEGIN SELECT RAISE(ABORT,'workbench object versions are immutable'); END;
CREATE TABLE workbench_execution_results (
 attempt_id TEXT PRIMARY KEY REFERENCES workbench_attempts(id) ON DELETE CASCADE,
 status TEXT NOT NULL CHECK(status IN ('ready','needs_work','failed','cancelled','interrupted')),
 message TEXT NOT NULL,
 created_at TEXT NOT NULL,
 registered_at TEXT
);
CREATE TRIGGER workbench_result_immutable BEFORE UPDATE OF attempt_id,status,message,created_at ON workbench_execution_results
BEGIN SELECT RAISE(ABORT,'workbench execution result is immutable'); END;
