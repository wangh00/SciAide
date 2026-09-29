-- Durable content identity recorded before copying downloaded task material.
ALTER TABLE research_candidate_task_imports ADD COLUMN pending_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE research_candidate_task_imports ADD COLUMN pending_kind TEXT NOT NULL DEFAULT '';

-- Keep the reason for a local evidence rewind in the same ordered timeline.
CREATE TRIGGER research_timeline_evidence_refresh AFTER INSERT ON workflow_events
WHEN NEW.event_type='workflow.evidence_refreshed'
BEGIN
    INSERT INTO research_timeline(research_task_id,workflow_run_id,kind,source_id,event_type,snapshot_json,created_at)
    SELECT research_task_id,id,'event',NEW.id,NEW.event_type,
        json_object('event',json(NEW.payload_json)),NEW.created_at
    FROM workflow_runs WHERE id=NEW.workflow_run_id AND research_task_id<>'';
END;
