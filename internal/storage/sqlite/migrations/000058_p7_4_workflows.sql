CREATE TABLE workflows (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 120),
    description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
    current_version_id TEXT REFERENCES workflow_versions(id) ON DELETE SET NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE workflow_versions (
    id TEXT PRIMARY KEY NOT NULL,
    workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    version_number INTEGER NOT NULL CHECK (version_number > 0),
    definition_json TEXT NOT NULL CHECK (
        length(definition_json) BETWEEN 2 AND 1048576
        AND json_valid(definition_json)
        AND json_type(definition_json) = 'object'
    ),
    definition_sha256 TEXT NOT NULL CHECK (
        length(definition_sha256) = 64 AND lower(definition_sha256) = definition_sha256
    ),
    compilation_json TEXT NOT NULL CHECK (
        length(compilation_json) BETWEEN 2 AND 2097152
        AND json_valid(compilation_json)
        AND json_type(compilation_json) = 'object'
    ),
    compilation_sha256 TEXT NOT NULL CHECK (
        length(compilation_sha256) = 64 AND lower(compilation_sha256) = compilation_sha256
    ),
    created_at TEXT NOT NULL,
    UNIQUE (workflow_id, version_number)
);

CREATE INDEX idx_workflows_project_updated
    ON workflows(project_id, updated_at DESC, id);
CREATE INDEX idx_workflow_versions_workflow_version
    ON workflow_versions(workflow_id, version_number DESC);

CREATE TRIGGER workflows_current_version_owner
BEFORE UPDATE OF current_version_id ON workflows
WHEN NEW.current_version_id IS NOT NULL
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM workflow_versions version
        WHERE version.id = NEW.current_version_id
          AND version.workflow_id = NEW.id
          AND version.version_number = NEW.version
    ) THEN RAISE(ABORT, 'Workflow current version does not belong to Workflow') END;
END;

CREATE TRIGGER workflows_initial_version_owner
BEFORE INSERT ON workflows
WHEN NEW.current_version_id IS NOT NULL
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM workflow_versions version
        WHERE version.id = NEW.current_version_id
          AND version.workflow_id = NEW.id
          AND version.version_number = NEW.version
    ) THEN RAISE(ABORT, 'Workflow current version does not belong to Workflow') END;
END;

CREATE TRIGGER workflow_versions_immutable
BEFORE UPDATE ON workflow_versions
BEGIN
    SELECT RAISE(ABORT, 'Workflow versions are immutable');
END;
