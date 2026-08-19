CREATE TABLE installed_skills (
    skill_id TEXT NOT NULL,
    skill_version TEXT NOT NULL,
    manifest_json TEXT NOT NULL CHECK (json_valid(manifest_json) AND json_type(manifest_json) = 'object'),
    package_rel_path TEXT NOT NULL UNIQUE CHECK (length(trim(package_rel_path)) BETWEEN 3 AND 256),
    manifest_hash TEXT NOT NULL CHECK (length(manifest_hash) = 64),
    content_hash TEXT NOT NULL CHECK (length(content_hash) = 64),
    package_hash TEXT NOT NULL CHECK (length(package_hash) = 64),
    integrity_status TEXT NOT NULL CHECK (integrity_status IN ('valid','invalid','missing')),
    integrity_error TEXT NOT NULL DEFAULT '',
    installed_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (skill_id, skill_version)
);

CREATE INDEX idx_installed_skills_integrity
    ON installed_skills(integrity_status, skill_id, skill_version);

CREATE TABLE project_skills (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    skill_id TEXT NOT NULL,
    skill_version TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0,1)),
    priority INTEGER NOT NULL DEFAULT 100 CHECK (priority BETWEEN 0 AND 1000),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (project_id, skill_id),
    FOREIGN KEY (skill_id, skill_version)
        REFERENCES installed_skills(skill_id, skill_version) ON DELETE RESTRICT
);

CREATE INDEX idx_project_skills_enabled
    ON project_skills(project_id, enabled, priority, skill_id);

-- P4.3 will write this provenance immediately before a Run loads Skill
-- instructions. It intentionally does not reference installed_skills so old
-- Run provenance survives a later uninstall.
CREATE TABLE run_skills (
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    skill_id TEXT NOT NULL,
    skill_version TEXT NOT NULL,
    content_hash TEXT NOT NULL CHECK (length(content_hash) = 64),
    package_hash TEXT NOT NULL CHECK (length(package_hash) = 64),
    created_at TEXT NOT NULL,
    PRIMARY KEY (run_id, ordinal),
    UNIQUE (run_id, skill_id)
);
