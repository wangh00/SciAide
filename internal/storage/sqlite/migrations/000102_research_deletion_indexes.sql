-- Deletion follows foreign keys. Missing child indexes repeatedly scan growing
-- history tables. Do not remove foreign keys or archived artifact provenance.
CREATE INDEX idx_runs_user_message ON runs(user_message_id);
CREATE INDEX idx_runs_assistant_message ON runs(assistant_message_id);
CREATE INDEX idx_permission_grants_run ON permission_grants(run_id);
CREATE INDEX idx_context_checkpoint_message ON conversation_context_checkpoints(through_message_id);
CREATE INDEX idx_message_citations_tool ON message_citations(tool_call_id);
CREATE INDEX idx_python_kernel_executions_run ON python_kernel_executions(run_id);
CREATE INDEX idx_workflow_decisions_run ON workflow_human_decisions(workflow_run_id);
CREATE INDEX idx_revision_proposals_workflow ON research_revision_proposals(workflow_run_id);
CREATE INDEX idx_revision_proposals_chat ON research_revision_proposals(chat_run_id);
CREATE INDEX idx_revision_proposals_message ON research_revision_proposals(user_message_id);
CREATE INDEX idx_research_timeline_workflow ON research_timeline(workflow_run_id);

-- run_events is a polymorphic event table without cascading foreign keys.
-- Cover ALL deletion entrypoints, including plan/project/conversation cascades.
CREATE TRIGGER run_events_after_chat_delete AFTER DELETE ON runs
BEGIN
    DELETE FROM run_events WHERE aggregate_id=OLD.id AND aggregate_type='run';
    DELETE FROM run_event_sequences WHERE aggregate_id=OLD.id
        AND NOT EXISTS(SELECT 1 FROM run_events WHERE aggregate_id=OLD.id);
END;
CREATE TRIGGER run_events_after_workflow_delete AFTER DELETE ON workflow_runs
BEGIN
    DELETE FROM run_events WHERE aggregate_id=OLD.id AND aggregate_type='workflow_run';
    DELETE FROM run_event_sequences WHERE aggregate_id=OLD.id
        AND NOT EXISTS(SELECT 1 FROM run_events WHERE aggregate_id=OLD.id);
END;
