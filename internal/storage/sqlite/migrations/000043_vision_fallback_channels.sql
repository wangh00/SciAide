CREATE TABLE vision_fallback_channels (
    id TEXT PRIMARY KEY NOT NULL CHECK (length(trim(id)) BETWEEN 1 AND 64),
    name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 120),
    base_url TEXT NOT NULL CHECK (length(trim(base_url)) BETWEEN 8 AND 2048),
    model_id TEXT NOT NULL CHECK (length(trim(model_id)) BETWEEN 1 AND 255),
    api_protocol TEXT NOT NULL CHECK (api_protocol IN ('openai_chat_completions','openai_responses','anthropic_messages')),
    secret_ref TEXT NOT NULL UNIQUE CHECK (length(trim(secret_ref)) BETWEEN 12 AND 255),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    priority INTEGER NOT NULL DEFAULT 100 CHECK (priority BETWEEN 0 AND 1000),
    timeout_seconds INTEGER NOT NULL DEFAULT 60 CHECK (timeout_seconds BETWEEN 5 AND 600),
    max_tokens INTEGER NOT NULL DEFAULT 4096 CHECK (max_tokens BETWEEN 1 AND 32000),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX idx_vision_fallback_channels_order
    ON vision_fallback_channels(enabled, priority, created_at, id);

-- hello-multimodal was a temporary host mechanism packaged as a Skill.
-- Historical Run snapshots are intentionally retained in run_skills.
CREATE TABLE retired_builtin_skill_packages (
    package_rel_path TEXT PRIMARY KEY NOT NULL,
    retired_at TEXT NOT NULL
);

INSERT INTO retired_builtin_skill_packages(package_rel_path, retired_at)
SELECT installed_skills.package_rel_path, strftime('%Y-%m-%dT%H:%M:%fZ','now')
FROM installed_skills
JOIN skill_package_sources
  ON skill_package_sources.skill_id = installed_skills.skill_id
 AND skill_package_sources.skill_version = installed_skills.skill_version
WHERE installed_skills.skill_id = 'hello-multimodal'
  AND skill_package_sources.source_kind = 'builtin';

DELETE FROM project_skills
WHERE skill_id = 'hello-multimodal'
  AND EXISTS (
      SELECT 1
      FROM skill_package_sources
      WHERE skill_package_sources.skill_id = project_skills.skill_id
        AND skill_package_sources.skill_version = project_skills.skill_version
        AND skill_package_sources.source_kind = 'builtin'
  );

DELETE FROM installed_skills
WHERE skill_id = 'hello-multimodal'
  AND EXISTS (
      SELECT 1
      FROM skill_package_sources
      WHERE skill_package_sources.skill_id = installed_skills.skill_id
        AND skill_package_sources.skill_version = installed_skills.skill_version
        AND skill_package_sources.source_kind = 'builtin'
  );
