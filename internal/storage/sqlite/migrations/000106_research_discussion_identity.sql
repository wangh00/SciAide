-- Discussion authorization belongs to the durable task and explicit human
-- conversations, not to the lifecycle or creation order of a planner attempt.
-- NULL is legacy/unknown, never an implicit authorization to explore.
ALTER TABLE research_tasks ADD COLUMN discussion_phase TEXT
    CHECK(discussion_phase IS NULL OR discussion_phase IN ('exploring','execution'));
ALTER TABLE research_tasks ADD COLUMN discussion_conversation_id TEXT NOT NULL DEFAULT '';

CREATE TABLE research_discussion_conversations (
    conversation_id TEXT PRIMARY KEY NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    research_task_id TEXT NOT NULL,
    -- Immutable provenance, NOT an owner FK or a capability token.
    source_workflow_run_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY(research_task_id,project_id) REFERENCES research_tasks(id,project_id) ON DELETE CASCADE
);
CREATE INDEX idx_research_discussion_task ON research_discussion_conversations(project_id,research_task_id);

INSERT INTO research_discussion_conversations
SELECT wc.conversation_id,wr.project_id,wr.research_task_id,wr.id,wc.created_at
FROM workflow_conversations wc JOIN workflow_runs wr ON wr.id=wc.workflow_run_id
JOIN conversations c ON c.id=wc.conversation_id AND c.project_id=wr.project_id
JOIN research_tasks t ON t.id=wr.research_task_id AND t.project_id=wr.project_id
WHERE wr.workflow_purpose='research_starter';

UPDATE research_tasks SET discussion_conversation_id=COALESCE(
    (SELECT d.conversation_id FROM research_discussion_conversations d
     WHERE d.project_id=research_tasks.project_id AND d.research_task_id=research_tasks.id ORDER BY d.created_at,d.rowid LIMIT 1),''),
    discussion_phase=CASE
    WHEN EXISTS(SELECT 1 FROM workflow_runs wr WHERE wr.project_id=research_tasks.project_id
        AND wr.research_task_id=research_tasks.id AND wr.workflow_purpose<>'research_starter')
      OR EXISTS(SELECT 1 FROM workflow_events e JOIN workflow_runs wr ON wr.id=e.workflow_run_id
        WHERE wr.project_id=research_tasks.project_id AND wr.research_task_id=research_tasks.id AND e.event_type='research.route_adopted')
    THEN 'execution'
    WHEN EXISTS(SELECT 1 FROM research_discussion_conversations d
        WHERE d.project_id=research_tasks.project_id AND d.research_task_id=research_tasks.id) THEN 'exploring'
    ELSE NULL END;

CREATE TRIGGER research_discussion_owner_immutable BEFORE UPDATE ON research_discussion_conversations
BEGIN SELECT RAISE(ABORT,'Research discussion ownership is immutable'); END;
CREATE TRIGGER research_discussion_phase_no_reopen BEFORE UPDATE OF discussion_phase ON research_tasks
WHEN OLD.discussion_phase='execution' AND NEW.discussion_phase IS NOT 'execution'
BEGIN SELECT RAISE(ABORT,'Adopted research discussion cannot be reopened'); END;
CREATE TRIGGER research_discussion_primary_guard BEFORE UPDATE OF discussion_conversation_id ON research_tasks
WHEN OLD.discussion_conversation_id<>NEW.discussion_conversation_id AND EXISTS(
    SELECT 1 FROM research_discussion_conversations d WHERE d.conversation_id=OLD.discussion_conversation_id
    AND d.project_id=OLD.project_id AND d.research_task_id=OLD.id)
BEGIN SELECT RAISE(ABORT,'Existing primary research discussion cannot be rebound'); END;

CREATE TRIGGER research_discussion_workflow_binding_guard BEFORE INSERT ON workflow_conversations
WHEN EXISTS(SELECT 1 FROM research_discussion_conversations d JOIN workflow_runs wr ON wr.id=NEW.workflow_run_id
    WHERE d.conversation_id=NEW.conversation_id AND (d.project_id<>wr.project_id OR d.research_task_id<>wr.research_task_id))
BEGIN SELECT RAISE(ABORT,'Workflow conversation conflicts with research discussion owner'); END;

CREATE TRIGGER research_discussion_after_workflow_binding AFTER INSERT ON workflow_conversations
BEGIN
    INSERT INTO research_discussion_conversations(conversation_id,project_id,research_task_id,source_workflow_run_id,created_at)
    SELECT NEW.conversation_id,wr.project_id,wr.research_task_id,wr.id,NEW.created_at
    FROM workflow_runs wr JOIN conversations c ON c.id=NEW.conversation_id AND c.project_id=wr.project_id
    JOIN research_tasks t ON t.id=wr.research_task_id AND t.project_id=wr.project_id
    WHERE wr.id=NEW.workflow_run_id AND wr.workflow_purpose='research_starter'
    ON CONFLICT(conversation_id) DO NOTHING;

    UPDATE research_tasks SET discussion_conversation_id=NEW.conversation_id
    WHERE EXISTS(SELECT 1 FROM research_discussion_conversations d WHERE d.conversation_id=NEW.conversation_id
        AND d.project_id=research_tasks.project_id AND d.research_task_id=research_tasks.id)
      AND NOT EXISTS(SELECT 1 FROM research_discussion_conversations d WHERE d.conversation_id=research_tasks.discussion_conversation_id
        AND d.project_id=research_tasks.project_id AND d.research_task_id=research_tasks.id);

    UPDATE research_tasks SET discussion_phase='execution'
    WHERE id=(SELECT research_task_id FROM workflow_runs WHERE id=NEW.workflow_run_id)
      AND project_id=(SELECT project_id FROM workflow_runs WHERE id=NEW.workflow_run_id)
      AND EXISTS(SELECT 1 FROM workflow_runs wr WHERE wr.id=NEW.workflow_run_id AND wr.workflow_purpose<>'research_starter');

    UPDATE research_tasks SET discussion_phase='exploring'
    WHERE discussion_phase IS NULL AND EXISTS(SELECT 1 FROM research_discussion_conversations d
        WHERE d.conversation_id=NEW.conversation_id AND d.project_id=research_tasks.project_id AND d.research_task_id=research_tasks.id)
      AND NOT EXISTS(SELECT 1 FROM workflow_runs wr WHERE wr.project_id=research_tasks.project_id
        AND wr.research_task_id=research_tasks.id AND wr.workflow_purpose<>'research_starter')
      AND NOT EXISTS(SELECT 1 FROM workflow_events e JOIN workflow_runs wr ON wr.id=e.workflow_run_id
        WHERE wr.project_id=research_tasks.project_id AND wr.research_task_id=research_tasks.id AND e.event_type='research.route_adopted');
END;

-- Close in the same transaction as formal work/adoption; deleting that
-- history never re-grants exploration rights to an old human conversation.
CREATE TRIGGER research_discussion_after_formal_run AFTER INSERT ON workflow_runs
WHEN NEW.workflow_purpose<>'research_starter' AND trim(NEW.research_task_id)<>''
BEGIN
    UPDATE research_tasks SET discussion_phase='execution' WHERE id=NEW.research_task_id AND project_id=NEW.project_id;
END;
CREATE TRIGGER research_discussion_after_adoption AFTER INSERT ON workflow_events
WHEN NEW.event_type='research.route_adopted'
BEGIN
    UPDATE research_tasks SET discussion_phase='execution'
    WHERE (id,project_id)=(SELECT research_task_id,project_id FROM workflow_runs WHERE id=NEW.workflow_run_id);
END;
CREATE TRIGGER research_discussion_after_formal_run_update AFTER UPDATE OF workflow_purpose,research_task_id,project_id ON workflow_runs
WHEN NEW.workflow_purpose<>'research_starter' AND trim(NEW.research_task_id)<>''
BEGIN
    UPDATE research_tasks SET discussion_phase='execution' WHERE id=NEW.research_task_id AND project_id=NEW.project_id;
END;
CREATE TRIGGER research_discussion_after_task_insert AFTER INSERT ON research_tasks
WHEN EXISTS(SELECT 1 FROM workflow_runs wr WHERE wr.project_id=NEW.project_id AND wr.research_task_id=NEW.id AND wr.workflow_purpose<>'research_starter')
BEGIN
    UPDATE research_tasks SET discussion_phase='execution' WHERE id=NEW.id AND project_id=NEW.project_id;
END;

-- Shared predicates for guidance, reads, writes and the completion fence.
-- Internal Workflow AI shares a conversation but never its exploration role.
CREATE VIEW research_exploration_context AS
SELECT d.*,t.research_question FROM research_discussion_conversations d
JOIN research_tasks t ON t.id=d.research_task_id AND t.project_id=d.project_id
JOIN conversations c ON c.id=d.conversation_id AND c.project_id=d.project_id
WHERE t.discussion_phase='exploring' AND t.status<>'archived' AND t.archived_at IS NULL;
CREATE VIEW research_exploration_chats AS
SELECT d.*,cr.id AS chat_run_id,cr.status AS chat_status
FROM research_exploration_context d JOIN runs cr ON cr.conversation_id=d.conversation_id
WHERE NOT EXISTS(SELECT 1 FROM workflow_ai_chat_runs b WHERE b.chat_run_id=cr.id);
CREATE VIEW research_discussion_latest_human AS
SELECT t.id AS research_task_id,t.project_id,
    (SELECT m.id FROM research_discussion_conversations d JOIN messages m ON m.conversation_id=d.conversation_id
     WHERE d.project_id=t.project_id AND d.research_task_id=t.id AND m.role='user'
       AND NOT EXISTS(SELECT 1 FROM workflow_ai_chat_runs b WHERE b.chat_run_id=m.run_id)
     ORDER BY m.rowid DESC LIMIT 1) AS user_message_id
FROM research_tasks t;
-- History/adoption must also include older formal/legacy bindings. This view
-- is NOT an exploration capability; only explicit owners above confer that.
CREATE VIEW research_task_conversations AS
SELECT project_id,research_task_id,conversation_id FROM research_discussion_conversations
UNION
SELECT wr.project_id,wr.research_task_id,wc.conversation_id
FROM workflow_conversations wc JOIN workflow_runs wr ON wr.id=wc.workflow_run_id
WHERE trim(wr.research_task_id)<>'';

-- Preserve all immutable note data and exact chat/message/call FKs. Planner
-- identity remains provenance only; explicit chat/history deletion still
-- cascades through its authentic source records.
DROP TRIGGER research_working_state_finish_guard;
DROP TRIGGER research_working_state_immutable;
DROP INDEX idx_research_working_state_task_version;
ALTER TABLE research_working_states RENAME TO research_working_states_before_discussion;
CREATE TABLE research_working_states (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL,
    research_task_id TEXT NOT NULL,
    workflow_run_id TEXT NOT NULL,
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
INSERT INTO research_working_states SELECT * FROM research_working_states_before_discussion;
DROP TABLE research_working_states_before_discussion;
CREATE INDEX idx_research_working_state_task_version ON research_working_states(project_id,research_task_id,version DESC);
CREATE TRIGGER research_working_state_immutable BEFORE UPDATE ON research_working_states
BEGIN SELECT RAISE(ABORT,'Research working states are immutable'); END;

CREATE TRIGGER research_working_state_finish_guard BEFORE UPDATE OF status ON tool_calls
WHEN OLD.status='running' AND NEW.status='completed'
 AND EXISTS(SELECT 1 FROM research_working_states s WHERE s.source_call_id=NEW.id)
 AND NOT EXISTS(
    SELECT 1 FROM research_working_states s
    JOIN research_working_state_proposals proposed ON proposed.source_call_id=s.source_call_id AND proposed.source_user_message_id=s.source_user_message_id
    JOIN research_exploration_chats d ON d.chat_run_id=s.source_chat_run_id AND d.chat_status='running'
      AND d.project_id=s.project_id AND d.research_task_id=s.research_task_id
    JOIN research_discussion_latest_human latest ON latest.project_id=s.project_id AND latest.research_task_id=s.research_task_id
      AND latest.user_message_id=s.source_user_message_id
    JOIN messages human ON human.id=s.source_user_message_id AND human.conversation_id=d.conversation_id
      AND human.run_id=d.chat_run_id AND human.role='user' AND human.status='complete'
    WHERE s.source_call_id=NEW.id AND NEW.run_id=d.chat_run_id AND NEW.workflow_run_id IS NULL
      AND NEW.tool_name='builtin.research.state.update'
      AND s.version=1+COALESCE((SELECT MAX(previous.version) FROM research_working_states previous
          JOIN tool_calls pc ON pc.id=previous.source_call_id AND pc.status='completed'
          JOIN tool_results pr ON pr.tool_call_id=pc.id AND pr.status='success'
          WHERE previous.project_id=s.project_id AND previous.research_task_id=s.research_task_id),0)
 )
BEGIN SELECT RAISE(ABORT,'Research working state binding or version changed'); END;
