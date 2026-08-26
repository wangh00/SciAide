CREATE TABLE run_skill_routing_audits (
    run_id TEXT PRIMARY KEY NOT NULL REFERENCES run_skill_routing(run_id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    catalog_skill_count INTEGER NOT NULL CHECK (catalog_skill_count >= 0),
    enabled_skill_count INTEGER NOT NULL CHECK (enabled_skill_count >= 0 AND enabled_skill_count <= catalog_skill_count),
    current_input_hash TEXT NOT NULL CHECK (length(current_input_hash) = 64 AND current_input_hash = lower(current_input_hash)),
    recent_input_hash TEXT NOT NULL CHECK (length(recent_input_hash) = 64 AND recent_input_hash = lower(recent_input_hash)),
    candidate_hash TEXT NOT NULL CHECK (length(candidate_hash) = 64 AND candidate_hash = lower(candidate_hash)),
    created_at TEXT NOT NULL
);

CREATE TABLE run_skill_routing_candidates (
    run_id TEXT NOT NULL REFERENCES run_skill_routing_audits(run_id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal < 20),
    skill_name TEXT NOT NULL CHECK (length(trim(skill_name)) BETWEEN 1 AND 64),
    origin TEXT NOT NULL CHECK (origin IN ('default','installed','user','project')),
    category TEXT NOT NULL DEFAULT '',
    total_score INTEGER NOT NULL,
    current_score INTEGER NOT NULL CHECK (current_score >= 0),
    recent_score INTEGER NOT NULL CHECK (recent_score >= 0),
    current_negation_penalty INTEGER NOT NULL CHECK (current_negation_penalty >= 0),
    recent_negation_penalty INTEGER NOT NULL CHECK (recent_negation_penalty >= 0),
    continuity_bonus INTEGER NOT NULL CHECK (continuity_bonus >= 0),
    continuity_run_id TEXT CHECK (continuity_run_id IS NULL OR length(trim(continuity_run_id)) BETWEEN 1 AND 128),
    shortlisted INTEGER NOT NULL CHECK (shortlisted IN (0,1)),
    explicitly_invoked INTEGER NOT NULL CHECK (explicitly_invoked IN (0,1)),
    PRIMARY KEY (run_id, ordinal),
    UNIQUE (run_id, skill_name)
);

CREATE INDEX idx_run_skill_routing_audits_project_created
    ON run_skill_routing_audits(project_id, created_at, run_id);

CREATE INDEX idx_run_skill_routing_candidates_skill
    ON run_skill_routing_candidates(skill_name, run_id, ordinal);
