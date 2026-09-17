CREATE TABLE research_revision_proposals (
    id TEXT PRIMARY KEY NOT NULL,
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    chat_run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    user_message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    source_call_id TEXT NOT NULL UNIQUE REFERENCES tool_calls(id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK(status IN ('pending','superseded','confirmed')),
    proposal_json TEXT NOT NULL CHECK(json_valid(proposal_json)),
    previous_snapshot_json TEXT CHECK(previous_snapshot_json IS NULL OR json_valid(previous_snapshot_json)),
    created_at TEXT NOT NULL,
    confirmed_at TEXT
);
CREATE UNIQUE INDEX idx_research_revision_pending ON research_revision_proposals(workflow_run_id) WHERE status='pending';

CREATE TRIGGER research_revision_proposal_identity_immutable
BEFORE UPDATE OF id,workflow_run_id,chat_run_id,user_message_id,source_call_id,proposal_json,created_at ON research_revision_proposals
BEGIN
    SELECT RAISE(ABORT, 'Research revision proposal identity is immutable');
END;

CREATE TRIGGER research_revision_proposal_final_immutable
BEFORE UPDATE ON research_revision_proposals WHEN OLD.status<>'pending'
BEGIN
    SELECT RAISE(ABORT, 'Final research revision proposals are immutable');
END;

-- Invalidate in the same transaction as the new user message. A late result
-- from the previous Chat Run cannot create an executable stale proposal.
CREATE TRIGGER invalidate_research_revision_on_message
AFTER INSERT ON messages WHEN NEW.role='user'
BEGIN
    UPDATE research_revision_proposals SET status='superseded'
    WHERE status='pending' AND workflow_run_id IN
        (SELECT workflow_run_id FROM workflow_conversations WHERE conversation_id=NEW.conversation_id);
END;
