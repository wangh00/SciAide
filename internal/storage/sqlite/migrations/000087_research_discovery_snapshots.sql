ALTER TABLE research_queries ADD COLUMN snapshot_version INTEGER NOT NULL DEFAULT 0;
CREATE TABLE research_query_candidates (
    query_id TEXT NOT NULL REFERENCES research_queries(id) ON DELETE CASCADE,
    candidate_id TEXT NOT NULL REFERENCES research_candidates(id) ON DELETE CASCADE,
    snapshot_json TEXT NOT NULL CHECK(json_valid(snapshot_json)),
    PRIMARY KEY(query_id,candidate_id)
);
CREATE INDEX research_query_candidates_candidate ON research_query_candidates(candidate_id,query_id);

-- Task identities intentionally survive task deletion, just like query origins.
CREATE TABLE research_candidate_reviews (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL,
    candidate_id TEXT NOT NULL REFERENCES research_candidates(id) ON DELETE CASCADE,
    review_status TEXT NOT NULL CHECK(review_status IN ('pending','included','excluded')),
    exclusion_reason TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL,
    PRIMARY KEY(project_id,task_id,candidate_id)
);
CREATE INDEX research_query_origins_task ON research_query_origins(task_id,query_id);
