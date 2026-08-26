-- sciaide:foreign_keys_off

CREATE TABLE workflow_steps_p7_6 (
    id TEXT PRIMARY KEY NOT NULL,
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL CHECK (length(trim(node_id)) BETWEEN 1 AND 64),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    node_kind TEXT NOT NULL CHECK (node_kind IN ('tool','shell','python','human_confirmation','candidate_selection','citation_selection')),
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

INSERT INTO workflow_steps_p7_6(
    id,workflow_run_id,node_id,ordinal,node_kind,status,attempt,input_json,input_sha256,output_json,
    tool_call_id,idempotency_key,error_code,error_message,started_at,completed_at,updated_at
)
SELECT id,workflow_run_id,node_id,ordinal,node_kind,status,attempt,input_json,input_sha256,output_json,
    tool_call_id,idempotency_key,error_code,error_message,started_at,completed_at,updated_at
FROM workflow_steps;

CREATE TABLE workflow_human_decisions_p7_6 (
    id TEXT PRIMARY KEY NOT NULL,
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    workflow_step_id TEXT NOT NULL REFERENCES workflow_steps_p7_6(id) ON DELETE CASCADE,
    decision_kind TEXT NOT NULL CHECK (decision_kind IN ('node_confirmation','retry_side_effect')),
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    approved INTEGER NOT NULL CHECK (approved IN (0,1)),
    note TEXT NOT NULL DEFAULT '' CHECK (length(note) <= 10000),
    context_json TEXT NOT NULL DEFAULT '{}' CHECK (length(context_json) BETWEEN 2 AND 262144 AND json_valid(context_json)),
    created_at TEXT NOT NULL,
    UNIQUE (workflow_step_id, decision_kind, attempt)
);

INSERT INTO workflow_human_decisions_p7_6(
    id,workflow_run_id,workflow_step_id,decision_kind,attempt,approved,note,context_json,created_at
)
SELECT id,workflow_run_id,workflow_step_id,decision_kind,attempt,approved,note,context_json,created_at
FROM workflow_human_decisions;

DROP TABLE workflow_human_decisions;
DROP TABLE workflow_steps;
ALTER TABLE workflow_steps_p7_6 RENAME TO workflow_steps;
ALTER TABLE workflow_human_decisions_p7_6 RENAME TO workflow_human_decisions;

CREATE INDEX idx_workflow_steps_run_ordinal ON workflow_steps(workflow_run_id, ordinal);
CREATE INDEX idx_workflow_steps_active ON workflow_steps(status, updated_at, id) WHERE status IN ('running','waiting_approval','waiting_human_confirmation');
CREATE TRIGGER workflow_human_decisions_immutable
BEFORE UPDATE ON workflow_human_decisions
BEGIN
    SELECT RAISE(ABORT, 'Workflow human decisions are immutable');
END;
