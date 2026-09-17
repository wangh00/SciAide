CREATE TABLE workflow_conversations (
    workflow_run_id TEXT PRIMARY KEY NOT NULL
        REFERENCES workflow_runs(id) ON DELETE CASCADE,
    conversation_id TEXT NOT NULL UNIQUE
        REFERENCES conversations(id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL
);

CREATE TRIGGER validate_workflow_conversation_project_before_insert
BEFORE INSERT ON workflow_conversations
WHEN (
    SELECT project_id FROM workflow_runs WHERE id = NEW.workflow_run_id
) <> (
    SELECT project_id FROM conversations WHERE id = NEW.conversation_id
)
BEGIN
    SELECT RAISE(ABORT, 'workflow conversation project mismatch');
END;

CREATE INDEX idx_workflow_conversations_conversation
    ON workflow_conversations(conversation_id);
