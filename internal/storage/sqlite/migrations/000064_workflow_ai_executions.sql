-- sciaide:foreign_keys_off

CREATE TABLE workflow_steps_p7_ai (
    id TEXT PRIMARY KEY NOT NULL,
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL CHECK (length(trim(node_id)) BETWEEN 1 AND 64),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    node_kind TEXT NOT NULL CHECK (node_kind IN ('tool','shell','python','human_confirmation','candidate_selection','citation_selection','ai_analysis','agent_stage')),
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

INSERT INTO workflow_steps_p7_ai(
    id,workflow_run_id,node_id,ordinal,node_kind,status,attempt,input_json,input_sha256,output_json,
    tool_call_id,idempotency_key,error_code,error_message,started_at,completed_at,updated_at
)
SELECT id,workflow_run_id,node_id,ordinal,node_kind,status,attempt,input_json,input_sha256,output_json,
    tool_call_id,idempotency_key,error_code,error_message,started_at,completed_at,updated_at
FROM workflow_steps;

CREATE TABLE workflow_human_decisions_p7_ai (
    id TEXT PRIMARY KEY NOT NULL,
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    workflow_step_id TEXT NOT NULL REFERENCES workflow_steps_p7_ai(id) ON DELETE CASCADE,
    decision_kind TEXT NOT NULL CHECK (decision_kind IN ('node_confirmation','retry_side_effect')),
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    approved INTEGER NOT NULL CHECK (approved IN (0,1)),
    note TEXT NOT NULL DEFAULT '' CHECK (length(note) <= 10000),
    context_json TEXT NOT NULL DEFAULT '{}' CHECK (length(context_json) BETWEEN 2 AND 262144 AND json_valid(context_json)),
    created_at TEXT NOT NULL,
    UNIQUE (workflow_step_id, decision_kind, attempt)
);

INSERT INTO workflow_human_decisions_p7_ai(
    id,workflow_run_id,workflow_step_id,decision_kind,attempt,approved,note,context_json,created_at
)
SELECT id,workflow_run_id,workflow_step_id,decision_kind,attempt,approved,note,context_json,created_at
FROM workflow_human_decisions;

DROP TABLE workflow_human_decisions;
DROP TABLE workflow_steps;
ALTER TABLE workflow_steps_p7_ai RENAME TO workflow_steps;
ALTER TABLE workflow_human_decisions_p7_ai RENAME TO workflow_human_decisions;

CREATE INDEX idx_workflow_steps_run_ordinal ON workflow_steps(workflow_run_id, ordinal);
CREATE INDEX idx_workflow_steps_active ON workflow_steps(status, updated_at, id) WHERE status IN ('running','waiting_approval','waiting_human_confirmation');
CREATE TRIGGER workflow_human_decisions_immutable
BEFORE UPDATE ON workflow_human_decisions
BEGIN
    SELECT RAISE(ABORT, 'Workflow human decisions are immutable');
END;

CREATE TABLE workflow_ai_executions (
    id TEXT PRIMARY KEY NOT NULL,
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    workflow_step_id TEXT NOT NULL REFERENCES workflow_steps(id) ON DELETE CASCADE,
    attempt INTEGER NOT NULL CHECK (attempt > 0),
    node_kind TEXT NOT NULL CHECK (node_kind IN ('ai_analysis','agent_stage')),
    model_profile_id TEXT NOT NULL,
    model_id TEXT NOT NULL,
    reasoning_level TEXT NOT NULL CHECK (reasoning_level IN ('none','minimal','low','medium','high','xhigh')),
    prompt_version TEXT NOT NULL CHECK (length(trim(prompt_version)) BETWEEN 1 AND 128),
    prompt_text TEXT NOT NULL CHECK (length(prompt_text) BETWEEN 1 AND 262144),
    prompt_sha256 TEXT NOT NULL CHECK (length(prompt_sha256)=64 AND prompt_sha256=lower(prompt_sha256)),
    input_sha256 TEXT NOT NULL CHECK (length(input_sha256)=64 AND input_sha256=lower(input_sha256)),
    allowed_tools_json TEXT NOT NULL CHECK (json_valid(allowed_tools_json) AND json_type(allowed_tools_json)='array'),
    output_schema_json TEXT NOT NULL CHECK (json_valid(output_schema_json) AND json_type(output_schema_json)='object'),
    output_schema_sha256 TEXT NOT NULL CHECK (length(output_schema_sha256)=64 AND output_schema_sha256=lower(output_schema_sha256)),
    status TEXT NOT NULL CHECK (status IN ('prepared','running','completed','failed','cancelled','interrupted')),
    output_text TEXT NOT NULL DEFAULT '',
    output_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(output_json) AND json_type(output_json)='object'),
    output_sha256 TEXT NOT NULL DEFAULT '' CHECK (output_sha256='' OR (length(output_sha256)=64 AND output_sha256=lower(output_sha256))),
    input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    reasoning_tokens INTEGER NOT NULL DEFAULT 0 CHECK (reasoning_tokens >= 0),
    model_turns INTEGER NOT NULL DEFAULT 0 CHECK (model_turns >= 0),
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    updated_at TEXT NOT NULL,
    UNIQUE(workflow_step_id, attempt),
    CHECK (completed_at IS NULL OR status IN ('completed','failed','cancelled','interrupted'))
);

CREATE INDEX idx_workflow_ai_executions_run
    ON workflow_ai_executions(workflow_run_id, workflow_step_id, attempt);

CREATE TABLE workflow_ai_chat_runs (
    execution_id TEXT PRIMARY KEY NOT NULL REFERENCES workflow_ai_executions(id) ON DELETE CASCADE,
    chat_run_id TEXT NOT NULL UNIQUE REFERENCES runs(id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL
);

CREATE TRIGGER validate_workflow_ai_chat_run_before_insert
BEFORE INSERT ON workflow_ai_chat_runs
WHEN (
    SELECT wc.conversation_id
    FROM workflow_ai_executions execution
    JOIN workflow_conversations wc ON wc.workflow_run_id=execution.workflow_run_id
    WHERE execution.id=NEW.execution_id
) <> (
    SELECT conversation_id FROM runs WHERE id=NEW.chat_run_id
)
BEGIN
    SELECT RAISE(ABORT, 'workflow AI chat run conversation mismatch');
END;
