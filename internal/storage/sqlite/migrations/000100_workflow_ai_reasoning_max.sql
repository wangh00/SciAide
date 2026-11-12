-- sciaide:foreign_keys_off
-- Keep all historical effort values and execution bytes. The requested MAX
-- preference must not be downgraded just to fit this persisted snapshot.
DROP TRIGGER validate_workflow_ai_chat_run_before_insert;

CREATE TABLE workflow_ai_executions_max (
    id TEXT PRIMARY KEY NOT NULL,
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    workflow_step_id TEXT NOT NULL REFERENCES workflow_steps(id) ON DELETE CASCADE,
    attempt INTEGER NOT NULL CHECK (attempt > 0),
    node_kind TEXT NOT NULL CHECK (node_kind IN ('ai_analysis','agent_stage')),
    model_profile_id TEXT NOT NULL,
    model_id TEXT NOT NULL,
    reasoning_level TEXT NOT NULL CHECK (reasoning_level IN ('none','minimal','low','medium','high','xhigh','max')),
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

INSERT INTO workflow_ai_executions_max (
    id,workflow_run_id,workflow_step_id,attempt,node_kind,model_profile_id,model_id,reasoning_level,
    prompt_version,prompt_text,prompt_sha256,input_sha256,allowed_tools_json,output_schema_json,output_schema_sha256,
    status,output_text,output_json,output_sha256,input_tokens,output_tokens,reasoning_tokens,model_turns,
    error_code,error_message,created_at,started_at,completed_at,updated_at
)
SELECT id,workflow_run_id,workflow_step_id,attempt,node_kind,model_profile_id,model_id,reasoning_level,
    prompt_version,prompt_text,prompt_sha256,input_sha256,allowed_tools_json,output_schema_json,output_schema_sha256,
    status,output_text,output_json,output_sha256,input_tokens,output_tokens,reasoning_tokens,model_turns,
    error_code,error_message,created_at,started_at,completed_at,updated_at
FROM workflow_ai_executions;

DROP TABLE workflow_ai_executions;
ALTER TABLE workflow_ai_executions_max RENAME TO workflow_ai_executions;

CREATE INDEX idx_workflow_ai_executions_run
    ON workflow_ai_executions(workflow_run_id, workflow_step_id, attempt);

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
