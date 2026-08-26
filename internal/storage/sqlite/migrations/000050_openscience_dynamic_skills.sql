CREATE TABLE skill_policies (
    skill_name TEXT PRIMARY KEY NOT NULL
        CHECK (length(trim(skill_name)) BETWEEN 1 AND 64),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    updated_at TEXT NOT NULL
);

CREATE TABLE run_dynamic_skills (
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    tool_call_id TEXT NOT NULL REFERENCES tool_calls(id) ON DELETE CASCADE,
    skill_name TEXT NOT NULL CHECK (length(trim(skill_name)) BETWEEN 1 AND 64),
    origin TEXT NOT NULL CHECK (origin IN ('default','installed','user','project')),
    category TEXT NOT NULL DEFAULT '',
    content_hash TEXT NOT NULL CHECK (length(content_hash) = 64),
    package_hash TEXT NOT NULL CHECK (length(package_hash) = 64),
    instruction_snapshot TEXT NOT NULL CHECK (length(instruction_snapshot) BETWEEN 1 AND 2097152),
    loaded_at TEXT NOT NULL,
    PRIMARY KEY (run_id, ordinal),
    UNIQUE (run_id, skill_name),
    UNIQUE (tool_call_id, skill_name)
);

CREATE INDEX idx_run_dynamic_skills_project_loaded
    ON run_dynamic_skills(project_id, loaded_at, run_id, ordinal);

-- Retire only the exact legacy packages that SciAide itself installed. User
-- packages with the same name but another source remain untouched.
INSERT OR IGNORE INTO retired_builtin_skill_packages(package_rel_path, retired_at)
SELECT installed_skills.package_rel_path, strftime('%Y-%m-%dT%H:%M:%fZ','now')
FROM installed_skills
JOIN skill_package_sources
  ON skill_package_sources.skill_id=installed_skills.skill_id
 AND skill_package_sources.skill_version=installed_skills.skill_version
WHERE skill_package_sources.source_kind='builtin'
  AND ((installed_skills.skill_id='academic-writing' AND installed_skills.skill_version='1.0.0')
    OR (installed_skills.skill_id='literature-reading' AND installed_skills.skill_version IN ('1.0.0','1.1.0')));

DELETE FROM project_skills
WHERE EXISTS (
    SELECT 1 FROM skill_package_sources source
    WHERE source.skill_id=project_skills.skill_id
      AND source.skill_version=project_skills.skill_version
      AND source.source_kind='builtin'
)
AND ((skill_id='academic-writing' AND skill_version='1.0.0')
  OR (skill_id='literature-reading' AND skill_version IN ('1.0.0','1.1.0')));

DELETE FROM installed_skills
WHERE EXISTS (
    SELECT 1 FROM skill_package_sources source
    WHERE source.skill_id=installed_skills.skill_id
      AND source.skill_version=installed_skills.skill_version
      AND source.source_kind='builtin'
)
AND ((skill_id='academic-writing' AND skill_version='1.0.0')
  OR (skill_id='literature-reading' AND skill_version IN ('1.0.0','1.1.0')));
