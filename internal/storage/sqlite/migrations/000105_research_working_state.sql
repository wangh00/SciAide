-- Capture the latest actual user message at ToolCall proposal, not invocation.
-- User-input questions pause entire batches, so a later answer must not become
-- the source of a call that the model proposed before seeing that answer.
CREATE TABLE research_working_state_proposals (
    source_call_id TEXT PRIMARY KEY NOT NULL REFERENCES tool_calls(id) ON DELETE CASCADE,
    source_user_message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    UNIQUE(source_call_id,source_user_message_id)
);
CREATE TRIGGER research_working_state_proposal_capture AFTER INSERT ON tool_calls
WHEN NEW.run_id IS NOT NULL AND NEW.tool_name='builtin.research.state.update'
BEGIN
    INSERT INTO research_working_state_proposals(source_call_id,source_user_message_id)
    SELECT NEW.id,m.id FROM runs cr JOIN messages m ON m.conversation_id=cr.conversation_id
    WHERE cr.id=NEW.run_id AND cr.status='running' AND m.run_id=cr.id AND m.role='user' AND m.status='complete'
      AND m.id=(SELECT id FROM messages WHERE conversation_id=cr.conversation_id AND role='user' ORDER BY rowid DESC LIMIT 1);
END;
CREATE TRIGGER research_working_state_proposal_immutable BEFORE UPDATE ON research_working_state_proposals
BEGIN SELECT RAISE(ABORT,'Research working state proposal identity is immutable'); END;

-- Model working notes are task-scoped, unverified and separate from frozen
-- research goals, adopted inputs, evidence and scientific decisions.
CREATE TABLE research_working_states (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL,
    research_task_id TEXT NOT NULL,
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    source_chat_run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    source_user_message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    source_call_id TEXT NOT NULL UNIQUE REFERENCES tool_calls(id) ON DELETE CASCADE,
    version INTEGER NOT NULL CHECK(version > 0),
    current_focus TEXT NOT NULL CHECK(length(trim(current_focus)) BETWEEN 1 AND 1000),
    rationale TEXT NOT NULL CHECK(length(trim(rationale)) BETWEEN 1 AND 1000),
    next_step TEXT NOT NULL CHECK(length(next_step) <= 1000),
    unknowns_json TEXT NOT NULL CHECK(json_valid(unknowns_json) AND json_type(unknowns_json)='array' AND json_array_length(unknowns_json)<=8),
    created_at TEXT NOT NULL,
    FOREIGN KEY(research_task_id,project_id) REFERENCES research_tasks(id,project_id) ON DELETE CASCADE,
    FOREIGN KEY(source_call_id,source_user_message_id) REFERENCES research_working_state_proposals(source_call_id,source_user_message_id) ON DELETE CASCADE
);
CREATE INDEX idx_research_working_state_task_version ON research_working_states(project_id,research_task_id,version DESC);
CREATE TRIGGER research_working_state_immutable BEFORE UPDATE ON research_working_states
BEGIN SELECT RAISE(ABORT,'Research working states are immutable'); END;

-- A delayed tool may not become successful after its user turn/task binding
-- or expected version changed. A failed finish remains an explicit unknown
-- tool outcome, and its pending note never enters the effective projection.
CREATE TRIGGER research_working_state_finish_guard BEFORE UPDATE OF status ON tool_calls
WHEN OLD.status='running' AND NEW.status='completed'
 AND EXISTS(SELECT 1 FROM research_working_states s WHERE s.source_call_id=NEW.id)
 AND NOT EXISTS(
    SELECT 1 FROM research_working_states s
    JOIN research_working_state_proposals proposed ON proposed.source_call_id=s.source_call_id AND proposed.source_user_message_id=s.source_user_message_id
    JOIN runs cr ON cr.id=s.source_chat_run_id
    JOIN workflow_conversations wc ON wc.conversation_id=cr.conversation_id AND wc.workflow_run_id=s.workflow_run_id
    JOIN workflow_runs wr ON wr.id=wc.workflow_run_id
    JOIN research_tasks task ON task.id=s.research_task_id AND task.project_id=s.project_id
    JOIN messages human ON human.id=s.source_user_message_id AND human.conversation_id=cr.conversation_id
      AND human.run_id=cr.id AND human.role='user' AND human.status='complete'
    WHERE s.source_call_id=NEW.id AND cr.status='running'
      AND NEW.run_id=cr.id AND NEW.tool_name='builtin.research.state.update'
      AND wr.project_id=s.project_id AND wr.research_task_id=s.research_task_id
      AND wr.workflow_purpose='research_starter' AND wr.status='completed' AND task.status<>'archived'
      AND s.source_user_message_id=(SELECT id FROM messages WHERE conversation_id=cr.conversation_id AND role='user' ORDER BY rowid DESC LIMIT 1)
      AND NOT EXISTS(SELECT 1 FROM workflow_ai_chat_runs b WHERE b.chat_run_id=cr.id)
      AND NOT EXISTS(SELECT 1 FROM workflow_runs newer WHERE newer.project_id=wr.project_id AND newer.research_task_id=wr.research_task_id
          AND (newer.created_at>wr.created_at OR (newer.created_at=wr.created_at AND newer.id>wr.id)))
      AND NOT EXISTS(SELECT 1 FROM workflow_events e JOIN workflow_runs adopted ON adopted.id=e.workflow_run_id
          WHERE adopted.project_id=wr.project_id AND adopted.research_task_id=wr.research_task_id AND e.event_type='research.route_adopted')
      AND s.version=1+COALESCE((SELECT MAX(previous.version) FROM research_working_states previous
          JOIN tool_calls pc ON pc.id=previous.source_call_id AND pc.status='completed'
          JOIN tool_results pr ON pr.tool_call_id=pc.id AND pr.status='success'
          WHERE previous.project_id=s.project_id AND previous.research_task_id=s.research_task_id),0)
 )
BEGIN SELECT RAISE(ABORT,'Research working state binding or version changed'); END;
