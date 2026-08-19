CREATE TABLE skill_package_sources_v2 (
    skill_id TEXT NOT NULL,
    skill_version TEXT NOT NULL,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('folder','zip','builtin')),
    source_name TEXT NOT NULL CHECK (length(trim(source_name)) BETWEEN 1 AND 255),
    source_hash TEXT NOT NULL CHECK (length(source_hash) = 64),
    archive_rel_path TEXT NOT NULL UNIQUE CHECK (length(trim(archive_rel_path)) BETWEEN 5 AND 512),
    installed_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (skill_id, skill_version),
    FOREIGN KEY (skill_id, skill_version)
        REFERENCES installed_skills(skill_id, skill_version) ON DELETE CASCADE
);

INSERT INTO skill_package_sources_v2(
    skill_id,skill_version,source_kind,source_name,source_hash,archive_rel_path,installed_at,updated_at
)
SELECT skill_id,skill_version,source_kind,source_name,source_hash,archive_rel_path,installed_at,updated_at
FROM skill_package_sources;

DROP TABLE skill_package_sources;
ALTER TABLE skill_package_sources_v2 RENAME TO skill_package_sources;

CREATE INDEX idx_skill_package_sources_hash
    ON skill_package_sources(source_hash, skill_id, skill_version);
