CREATE TABLE workbench_jobs (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL REFERENCES workbench_tasks(id) ON DELETE CASCADE,
 state TEXT NOT NULL CHECK(state IN ('running','completed','failed','cancelled','interrupted')),
 sequence INTEGER NOT NULL,
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
 updated_at TEXT NOT NULL
);
CREATE INDEX workbench_jobs_task ON workbench_jobs(task_id,sequence DESC);
