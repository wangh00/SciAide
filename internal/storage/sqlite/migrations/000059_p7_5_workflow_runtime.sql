-- sciaide:foreign_keys_off

DROP TRIGGER IF EXISTS validate_approval_snapshot_before_insert;

CREATE TABLE workflow_runs (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    workflow_version_id TEXT NOT NULL REFERENCES workflow_versions(id) ON DELETE RESTRICT,
    status TEXT NOT NULL CHECK (status IN (
        'queued','running','waiting_approval','waiting_human_confirmation',
        'paused','completed','failed','cancelled','interrupted'
    )),
    inputs_json TEXT NOT NULL CHECK (
        length(inputs_json) BETWEEN 2 AND 1048576 AND json_valid(inputs_json) AND json_type(inputs_json)='object'
    ),
    inputs_sha256 TEXT NOT NULL CHECK (length(inputs_sha256)=64 AND inputs_sha256=lower(inputs_sha256)),
    compilation_json TEXT NOT NULL CHECK (
        length(compilation_json) BETWEEN 2 AND 2097152 AND json_valid(compilation_json) AND json_type(compilation_json)='object'
    ),
    compilation_sha256 TEXT NOT NULL CHECK (length(compilation_sha256)=64 AND compilation_sha256=lower(compilation_sha256)),
    outputs_json TEXT NOT NULL DEFAULT '{}' CHECK (
        length(outputs_json) BETWEEN 2 AND 2097152 AND json_valid(outputs_json) AND json_type(outputs_json)='object'
    ),
    current_step_ordinal INTEGER NOT NULL DEFAULT 0 CHECK (current_step_ordinal >= 0),
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    cancel_requested INTEGER NOT NULL DEFAULT 0 CHECK (cancel_requested IN (0,1)),
    resume_status TEXT NOT NULL DEFAULT '' CHECK (resume_status IN ('','queued','waiting_approval','waiting_human_confirmation')),
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    updated_at TEXT NOT NULL,
    CHECK (completed_at IS NULL OR status IN ('completed','failed','cancelled','interrupted'))
);

CREATE INDEX idx_workflow_runs_project_updated
    ON workflow_runs(project_id, updated_at DESC, id);
CREATE INDEX idx_workflow_runs_active
    ON workflow_runs(status, updated_at, id)
    WHERE status IN ('queued','running','waiting_approval','waiting_human_confirmation','paused');

CREATE TABLE tool_calls_p7_5 (
    id TEXT PRIMARY KEY NOT NULL,
    run_id TEXT REFERENCES runs(id) ON DELETE CASCADE,
    workflow_run_id TEXT REFERENCES workflow_runs(id) ON DELETE CASCADE,
    provider_call_id TEXT NOT NULL CHECK (length(trim(provider_call_id)) > 0),
    tool_name TEXT NOT NULL CHECK (length(trim(tool_name)) > 0),
    tool_version TEXT NOT NULL CHECK (length(trim(tool_version)) > 0),
    arguments_json TEXT NOT NULL CHECK (json_valid(arguments_json) AND json_type(arguments_json) = 'object'),
    status TEXT NOT NULL CHECK (status IN ('pending', 'awaiting_approval', 'running', 'completed', 'failed', 'denied', 'cancelled', 'interrupted')),
    risk TEXT NOT NULL CHECK (risk IN ('low', 'moderate', 'high', 'destructive')),
    permissions_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(permissions_json) AND json_type(permissions_json) = 'array'),
    idempotent INTEGER NOT NULL DEFAULT 0 CHECK (idempotent IN (0, 1)),
    idempotency_key TEXT,
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    updated_at TEXT NOT NULL,
    CHECK ((run_id IS NOT NULL) <> (workflow_run_id IS NOT NULL)),
    UNIQUE (run_id, provider_call_id),
    UNIQUE (workflow_run_id, provider_call_id)
);

INSERT INTO tool_calls_p7_5(
    id,run_id,workflow_run_id,provider_call_id,tool_name,tool_version,arguments_json,status,risk,
    permissions_json,idempotent,idempotency_key,error_code,error_message,created_at,started_at,completed_at,updated_at
)
SELECT id,run_id,NULL,provider_call_id,tool_name,tool_version,arguments_json,status,risk,
    permissions_json,idempotent,idempotency_key,error_code,error_message,created_at,started_at,completed_at,updated_at
FROM tool_calls;
DROP TABLE tool_calls;
ALTER TABLE tool_calls_p7_5 RENAME TO tool_calls;

CREATE INDEX idx_tool_calls_run_created ON tool_calls(run_id, created_at, id) WHERE run_id IS NOT NULL;
CREATE INDEX idx_tool_calls_workflow_run_created ON tool_calls(workflow_run_id, created_at, id) WHERE workflow_run_id IS NOT NULL;
CREATE INDEX idx_tool_calls_active ON tool_calls(status) WHERE status IN ('pending', 'awaiting_approval', 'running');
CREATE UNIQUE INDEX idx_tool_calls_idempotency_key
    ON tool_calls(run_id, idempotency_key)
    WHERE run_id IS NOT NULL AND idempotency_key IS NOT NULL AND length(idempotency_key) > 0;
CREATE UNIQUE INDEX idx_tool_calls_workflow_idempotency_key
    ON tool_calls(workflow_run_id, idempotency_key)
    WHERE workflow_run_id IS NOT NULL AND idempotency_key IS NOT NULL AND length(idempotency_key) > 0;

CREATE TABLE approvals_p7_5 (
    id TEXT PRIMARY KEY NOT NULL,
    run_id TEXT REFERENCES runs(id) ON DELETE CASCADE,
    workflow_run_id TEXT REFERENCES workflow_runs(id) ON DELETE CASCADE,
    tool_call_id TEXT NOT NULL REFERENCES tool_calls(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    tool_name TEXT NOT NULL CHECK (length(trim(tool_name)) > 0),
    tool_version TEXT NOT NULL CHECK (length(trim(tool_version)) > 0),
    permission_kind TEXT NOT NULL CHECK (permission_kind IN ('tool.invoke', 'workspace.read', 'workspace.write', 'filesystem.external', 'network.domain', 'process.execute', 'destructive', 'secret.use')),
    resource TEXT NOT NULL DEFAULT '',
    risk TEXT NOT NULL CHECK (risk IN ('low', 'moderate', 'high', 'destructive')),
    status TEXT NOT NULL CHECK (status IN ('pending', 'granted', 'denied', 'expired')),
    requested_scope TEXT NOT NULL CHECK (requested_scope IN ('call', 'run', 'project')),
    resolved_scope TEXT CHECK (resolved_scope IS NULL OR resolved_scope IN ('call', 'run', 'project')),
    reason TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    resolved_at TEXT,
    UNIQUE (tool_call_id, permission_kind, resource),
    CHECK ((run_id IS NOT NULL) <> (workflow_run_id IS NOT NULL)),
    CHECK ((status = 'pending' AND resolved_scope IS NULL AND resolved_at IS NULL)
        OR (status <> 'pending' AND resolved_scope IS NOT NULL AND resolved_at IS NOT NULL)),
    CHECK (requested_scope <> 'call' OR resolved_scope IS NULL OR resolved_scope = 'call'),
    CHECK (requested_scope <> 'run' OR resolved_scope IS NULL OR resolved_scope IN ('call', 'run'))
);

INSERT INTO approvals_p7_5(
    id,run_id,workflow_run_id,tool_call_id,project_id,tool_name,tool_version,permission_kind,resource,
    risk,status,requested_scope,resolved_scope,reason,created_at,resolved_at
)
SELECT id,run_id,NULL,tool_call_id,project_id,tool_name,tool_version,permission_kind,resource,
    risk,status,requested_scope,resolved_scope,reason,created_at,resolved_at
FROM approvals;
DROP TABLE approvals;
ALTER TABLE approvals_p7_5 RENAME TO approvals;

CREATE INDEX idx_approvals_run_created ON approvals(run_id, created_at, id) WHERE run_id IS NOT NULL;
CREATE INDEX idx_approvals_workflow_run_created ON approvals(workflow_run_id, created_at, id) WHERE workflow_run_id IS NOT NULL;
CREATE UNIQUE INDEX idx_approvals_one_pending_per_call ON approvals(tool_call_id) WHERE status = 'pending';

CREATE TRIGGER validate_approval_snapshot_before_insert
BEFORE INSERT ON approvals
BEGIN
    SELECT CASE WHEN NEW.resource <> trim(NEW.resource)
        THEN RAISE(ABORT, 'approval resource must be normalized') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1
        FROM tool_calls tc
        LEFT JOIN runs r ON r.id = tc.run_id
        LEFT JOIN conversations c ON c.id = r.conversation_id
        LEFT JOIN workflow_runs wr ON wr.id = tc.workflow_run_id
        WHERE tc.id = NEW.tool_call_id
          AND tc.run_id IS NEW.run_id
          AND tc.workflow_run_id IS NEW.workflow_run_id
          AND COALESCE(c.project_id,wr.project_id) = NEW.project_id
          AND tc.tool_name = NEW.tool_name
          AND tc.tool_version = NEW.tool_version
          AND tc.risk = NEW.risk
          AND (
              (NEW.permission_kind = 'tool.invoke' AND NEW.resource = NEW.tool_name)
              OR EXISTS (
                  SELECT 1 FROM json_each(tc.permissions_json) required
                  WHERE json_extract(required.value, '$.kind') = NEW.permission_kind
                    AND COALESCE(json_extract(required.value, '$.resource'), '') = NEW.resource
              )
          )
    ) THEN RAISE(ABORT, 'approval does not match tool call snapshot') END;
END;

CREATE TABLE process_execution_audits_p7_5 (
    tool_call_id TEXT PRIMARY KEY NOT NULL REFERENCES tool_calls(id) ON DELETE CASCADE,
    run_id TEXT REFERENCES runs(id) ON DELETE CASCADE,
    workflow_run_id TEXT REFERENCES workflow_runs(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    tool_name TEXT NOT NULL,
    executable_path TEXT NOT NULL,
    executable_version TEXT NOT NULL,
    executable_sha256 TEXT NOT NULL CHECK (length(executable_sha256) = 64 AND executable_sha256 = lower(executable_sha256)),
    script_path TEXT NOT NULL DEFAULT '',
    script_sha256 TEXT NOT NULL DEFAULT '' CHECK (script_sha256 = '' OR (length(script_sha256) = 64 AND script_sha256 = lower(script_sha256))),
    command_sha256 TEXT NOT NULL DEFAULT '' CHECK (command_sha256 = '' OR (length(command_sha256) = 64 AND command_sha256 = lower(command_sha256))),
    workdir TEXT NOT NULL,
    timeout_millis INTEGER NOT NULL CHECK (timeout_millis > 0),
    environment_names_json TEXT NOT NULL CHECK (json_valid(environment_names_json) AND json_type(environment_names_json) = 'array'),
    process_id INTEGER,
    state TEXT NOT NULL CHECK (state IN ('prepared','running','completed','failed')),
    termination_reason TEXT CHECK (termination_reason IS NULL OR termination_reason IN ('completed','exit_nonzero','timed_out','cancelled','app_shutdown','start_failed')),
    exit_code INTEGER,
    stdout_bytes INTEGER NOT NULL DEFAULT 0 CHECK (stdout_bytes >= 0),
    stdout_sha256 TEXT NOT NULL DEFAULT '' CHECK (stdout_sha256 = '' OR (length(stdout_sha256) = 64 AND stdout_sha256 = lower(stdout_sha256))),
    stdout_truncated INTEGER NOT NULL DEFAULT 0 CHECK (stdout_truncated IN (0,1)),
    stderr_bytes INTEGER NOT NULL DEFAULT 0 CHECK (stderr_bytes >= 0),
    stderr_sha256 TEXT NOT NULL DEFAULT '' CHECK (stderr_sha256 = '' OR (length(stderr_sha256) = 64 AND stderr_sha256 = lower(stderr_sha256))),
    stderr_truncated INTEGER NOT NULL DEFAULT 0 CHECK (stderr_truncated IN (0,1)),
    error_message TEXT NOT NULL DEFAULT '',
    prepared_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    CHECK ((run_id IS NOT NULL) <> (workflow_run_id IS NOT NULL))
);

INSERT INTO process_execution_audits_p7_5 SELECT
    tool_call_id,run_id,NULL,project_id,tool_name,executable_path,executable_version,executable_sha256,
    script_path,script_sha256,command_sha256,workdir,timeout_millis,environment_names_json,process_id,state,
    termination_reason,exit_code,stdout_bytes,stdout_sha256,stdout_truncated,stderr_bytes,stderr_sha256,
    stderr_truncated,error_message,prepared_at,started_at,completed_at
FROM process_execution_audits;
DROP TABLE process_execution_audits;
ALTER TABLE process_execution_audits_p7_5 RENAME TO process_execution_audits;
CREATE INDEX idx_process_execution_audits_project_prepared ON process_execution_audits(project_id, prepared_at, tool_call_id);
CREATE INDEX idx_process_execution_audits_run ON process_execution_audits(run_id, prepared_at, tool_call_id) WHERE run_id IS NOT NULL;
CREATE INDEX idx_process_execution_audits_workflow_run ON process_execution_audits(workflow_run_id, prepared_at, tool_call_id) WHERE workflow_run_id IS NOT NULL;

CREATE TABLE python_kernel_executions_p7_5 (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    tool_call_id TEXT NOT NULL UNIQUE REFERENCES tool_calls(id) ON DELETE CASCADE,
    run_id TEXT REFERENCES runs(id) ON DELETE CASCADE,
    workflow_run_id TEXT REFERENCES workflow_runs(id) ON DELETE CASCADE,
    environment_id TEXT NOT NULL REFERENCES python_environments(id) ON DELETE CASCADE,
    environment_fingerprint TEXT NOT NULL,
    kernel_id TEXT NOT NULL,
    execution_id TEXT NOT NULL UNIQUE,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    status TEXT NOT NULL CHECK (status IN ('success','error','cancelled','timed_out','failed')),
    code_sha256 TEXT NOT NULL CHECK (length(code_sha256) = 64),
    input_sha256_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(input_sha256_json)),
    output_sha256_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(output_sha256_json)),
    stdout_truncated INTEGER NOT NULL DEFAULT 0 CHECK (stdout_truncated IN (0,1)),
    stderr_truncated INTEGER NOT NULL DEFAULT 0 CHECK (stderr_truncated IN (0,1)),
    exception_type TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    completed_at TEXT NOT NULL,
    reproduction_sha256 TEXT NOT NULL DEFAULT '',
    CHECK ((run_id IS NOT NULL) <> (workflow_run_id IS NOT NULL))
);

INSERT INTO python_kernel_executions_p7_5 SELECT
    id,project_id,tool_call_id,run_id,NULL,environment_id,environment_fingerprint,kernel_id,execution_id,sequence,status,
    code_sha256,input_sha256_json,output_sha256_json,stdout_truncated,stderr_truncated,exception_type,error_message,
    started_at,completed_at,reproduction_sha256
FROM python_kernel_executions;
DROP TABLE python_kernel_executions;
ALTER TABLE python_kernel_executions_p7_5 RENAME TO python_kernel_executions;
CREATE INDEX idx_python_kernel_executions_project_completed ON python_kernel_executions(project_id, completed_at DESC, id DESC);
CREATE INDEX idx_python_kernel_executions_workflow_run ON python_kernel_executions(workflow_run_id, completed_at, id) WHERE workflow_run_id IS NOT NULL;

CREATE TABLE artifact_lineage_p7_5 (
    id TEXT PRIMARY KEY NOT NULL,
    artifact_version_id TEXT NOT NULL REFERENCES artifact_versions(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    relation_kind TEXT NOT NULL CHECK (relation_kind IN ('run','workflow_run','message','tool_call','artifact_version','workspace_file')),
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

INSERT INTO artifact_lineage_p7_5(
    id,artifact_version_id,ordinal,relation_kind,source_id_snapshot,source_run_id,source_workflow_run_id,
    source_message_id,source_tool_call_id,source_artifact_version_id,label,metadata_json,created_at
)
SELECT id,artifact_version_id,ordinal,relation_kind,source_id_snapshot,source_run_id,NULL,
    source_message_id,source_tool_call_id,source_artifact_version_id,label,metadata_json,created_at
FROM artifact_lineage;
DROP TABLE artifact_lineage;
ALTER TABLE artifact_lineage_p7_5 RENAME TO artifact_lineage;

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

CREATE TABLE workflow_steps (
    id TEXT PRIMARY KEY NOT NULL,
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL CHECK (length(trim(node_id)) BETWEEN 1 AND 64),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    node_kind TEXT NOT NULL CHECK (node_kind IN ('tool','shell','python','human_confirmation','citation_selection')),
    status TEXT NOT NULL CHECK (status IN ('queued','running','waiting_approval','waiting_human_confirmation','completed','failed','cancelled','interrupted','outcome_unknown')),
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    input_json TEXT NOT NULL DEFAULT '{}' CHECK (length(input_json) BETWEEN 2 AND 1048576 AND json_valid(input_json) AND json_type(input_json)='object'),
    input_sha256 TEXT NOT NULL DEFAULT '' CHECK (input_sha256='' OR (length(input_sha256)=64 AND input_sha256=lower(input_sha256))),
    output_json TEXT NOT NULL DEFAULT '{}' CHECK (length(output_json) BETWEEN 2 AND 2097152 AND json_valid(output_json) AND json_type(output_json)='object'),
    tool_call_id TEXT UNIQUE REFERENCES tool_calls(id) ON DELETE SET NULL,
    idempotency_key TEXT NOT NULL DEFAULT '',
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    started_at TEXT,
    completed_at TEXT,
    updated_at TEXT NOT NULL,
    UNIQUE (workflow_run_id, node_id),
    UNIQUE (workflow_run_id, ordinal)
);
CREATE INDEX idx_workflow_steps_run_ordinal ON workflow_steps(workflow_run_id, ordinal);
CREATE INDEX idx_workflow_steps_active ON workflow_steps(status, updated_at, id) WHERE status IN ('running','waiting_approval','waiting_human_confirmation');

CREATE TABLE workflow_human_decisions (
    id TEXT PRIMARY KEY NOT NULL,
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    workflow_step_id TEXT NOT NULL REFERENCES workflow_steps(id) ON DELETE CASCADE,
    decision_kind TEXT NOT NULL CHECK (decision_kind IN ('node_confirmation','retry_side_effect')),
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    approved INTEGER NOT NULL CHECK (approved IN (0,1)),
    note TEXT NOT NULL DEFAULT '' CHECK (length(note) <= 10000),
    context_json TEXT NOT NULL DEFAULT '{}' CHECK (length(context_json) BETWEEN 2 AND 262144 AND json_valid(context_json)),
    created_at TEXT NOT NULL,
    UNIQUE (workflow_step_id, decision_kind, attempt)
);

CREATE TRIGGER workflow_human_decisions_immutable
BEFORE UPDATE ON workflow_human_decisions
BEGIN
    SELECT RAISE(ABORT, 'Workflow human decisions are immutable');
END;

CREATE TABLE workflow_events (
    id TEXT PRIMARY KEY NOT NULL,
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    event_type TEXT NOT NULL CHECK (length(trim(event_type)) BETWEEN 1 AND 120),
    payload_json TEXT NOT NULL CHECK (
        length(payload_json) BETWEEN 2 AND 2097152 AND json_valid(payload_json) AND json_type(payload_json)='object'
    ),
    created_at TEXT NOT NULL,
    UNIQUE (workflow_run_id, sequence)
);
CREATE INDEX idx_workflow_events_run_sequence ON workflow_events(workflow_run_id, sequence);
CREATE TRIGGER workflow_events_immutable BEFORE UPDATE ON workflow_events BEGIN SELECT RAISE(ABORT, 'Workflow events are immutable'); END;

CREATE TRIGGER workflow_runs_version_owner
BEFORE INSERT ON workflow_runs
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM workflow_versions v JOIN workflows w ON w.id=v.workflow_id
        WHERE v.id=NEW.workflow_version_id AND v.workflow_id=NEW.workflow_id AND w.project_id=NEW.project_id
    ) THEN RAISE(ABORT, 'Workflow Run version does not belong to Workflow and project') END;
END;
