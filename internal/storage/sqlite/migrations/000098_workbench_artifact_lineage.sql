CREATE TABLE artifact_lineage_workbench (
    id TEXT PRIMARY KEY NOT NULL,
    artifact_version_id TEXT NOT NULL REFERENCES artifact_versions(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    relation_kind TEXT NOT NULL CHECK (relation_kind IN ('run','workflow_run','workbench_attempt','message','tool_call','artifact_version','workspace_file')),
    source_id_snapshot TEXT NOT NULL CHECK (length(trim(source_id_snapshot)) BETWEEN 1 AND 4096),
    source_run_id TEXT REFERENCES runs(id) ON DELETE SET NULL,
    source_workflow_run_id TEXT REFERENCES workflow_runs(id) ON DELETE SET NULL,
    source_message_id TEXT REFERENCES messages(id) ON DELETE SET NULL,
    source_tool_call_id TEXT REFERENCES tool_calls(id) ON DELETE SET NULL,
    source_artifact_version_id TEXT REFERENCES artifact_versions(id) ON DELETE SET NULL,
    label TEXT NOT NULL DEFAULT '',
    metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (
        length(metadata_json) BETWEEN 2 AND 262144 AND json_valid(metadata_json) AND json_type(metadata_json) = 'object'
    ),
    created_at TEXT NOT NULL,
    UNIQUE (artifact_version_id, ordinal)
);

INSERT INTO artifact_lineage_workbench(
    id,artifact_version_id,ordinal,relation_kind,source_id_snapshot,source_run_id,source_workflow_run_id,
    source_message_id,source_tool_call_id,source_artifact_version_id,label,metadata_json,created_at
)
SELECT id,artifact_version_id,ordinal,relation_kind,source_id_snapshot,source_run_id,source_workflow_run_id,
    source_message_id,source_tool_call_id,source_artifact_version_id,label,metadata_json,created_at
FROM artifact_lineage;
DROP TABLE artifact_lineage;
ALTER TABLE artifact_lineage_workbench RENAME TO artifact_lineage;

CREATE INDEX idx_artifact_lineage_run ON artifact_lineage(source_run_id) WHERE source_run_id IS NOT NULL;
CREATE INDEX idx_artifact_lineage_workflow_run ON artifact_lineage(source_workflow_run_id) WHERE source_workflow_run_id IS NOT NULL;
CREATE INDEX idx_artifact_lineage_message ON artifact_lineage(source_message_id) WHERE source_message_id IS NOT NULL;
CREATE INDEX idx_artifact_lineage_tool ON artifact_lineage(source_tool_call_id) WHERE source_tool_call_id IS NOT NULL;
CREATE TRIGGER artifact_lineage_content_immutable BEFORE UPDATE OF id,artifact_version_id,ordinal,relation_kind,source_id_snapshot,label,metadata_json,created_at ON artifact_lineage BEGIN SELECT RAISE(ABORT, 'artifact lineage is immutable'); END;
CREATE TRIGGER artifact_lineage_run_link_immutable BEFORE UPDATE OF source_run_id ON artifact_lineage WHEN OLD.source_run_id IS NULL OR NEW.source_run_id IS NOT NULL BEGIN SELECT RAISE(ABORT, 'artifact lineage source link is immutable'); END;
CREATE TRIGGER artifact_lineage_workflow_run_link_immutable BEFORE UPDATE OF source_workflow_run_id ON artifact_lineage WHEN OLD.source_workflow_run_id IS NULL OR NEW.source_workflow_run_id IS NOT NULL BEGIN SELECT RAISE(ABORT, 'artifact lineage source link is immutable'); END;
CREATE TRIGGER artifact_lineage_message_link_immutable BEFORE UPDATE OF source_message_id ON artifact_lineage WHEN OLD.source_message_id IS NULL OR NEW.source_message_id IS NOT NULL BEGIN SELECT RAISE(ABORT, 'artifact lineage source link is immutable'); END;
CREATE TRIGGER artifact_lineage_tool_link_immutable BEFORE UPDATE OF source_tool_call_id ON artifact_lineage WHEN OLD.source_tool_call_id IS NULL OR NEW.source_tool_call_id IS NOT NULL BEGIN SELECT RAISE(ABORT, 'artifact lineage source link is immutable'); END;
CREATE TRIGGER artifact_lineage_artifact_link_immutable BEFORE UPDATE OF source_artifact_version_id ON artifact_lineage WHEN OLD.source_artifact_version_id IS NULL OR NEW.source_artifact_version_id IS NOT NULL BEGIN SELECT RAISE(ABORT, 'artifact lineage source link is immutable'); END;


-- Rebuilding lineage also drops this existing timeline trigger.
CREATE TRIGGER research_timeline_registration_insert AFTER INSERT ON artifact_lineage
WHEN NEW.source_workflow_run_id IS NOT NULL
BEGIN
    INSERT INTO research_timeline(research_task_id,workflow_run_id,kind,source_id,snapshot_json,created_at)
    SELECT wr.research_task_id,wr.id,'registration',NEW.id,
        json_object('name',a.name,'outputName',json_extract(v.provenance_json,'$.extra.workflowDeliverable'),'versionId',v.id),NEW.created_at
    FROM workflow_runs wr JOIN artifact_versions v ON v.id=NEW.artifact_version_id JOIN artifacts a ON a.id=v.artifact_id
    WHERE wr.id=NEW.source_workflow_run_id AND wr.research_task_id<>'' AND json_extract(v.provenance_json,'$.extra.workflowDeliverable') IS NOT NULL;
END;
