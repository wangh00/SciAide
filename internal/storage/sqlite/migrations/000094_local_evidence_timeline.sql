-- Local evidence follow-up and reuse are explicit, ordered audit events.
CREATE TRIGGER research_timeline_local_evidence AFTER INSERT ON workflow_events
WHEN NEW.event_type IN ('workflow.local_evidence_requested','workflow.materials_reused')
BEGIN
    INSERT INTO research_timeline(research_task_id,workflow_run_id,kind,source_id,event_type,snapshot_json,created_at)
    SELECT research_task_id,id,'event',NEW.id,NEW.event_type,
        json_object('event',json(json_remove(NEW.payload_json,'$.previousSearchOutput','$.previousSearchHash'))),NEW.created_at
    FROM workflow_runs WHERE id=NEW.workflow_run_id AND research_task_id<>'';
END;
