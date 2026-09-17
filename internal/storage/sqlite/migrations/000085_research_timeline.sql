CREATE TABLE research_timeline (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    research_task_id TEXT NOT NULL REFERENCES research_tasks(id) ON DELETE CASCADE,
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK(kind IN ('message','event','proposal','registration')),
    source_id TEXT NOT NULL,
    event_type TEXT NOT NULL DEFAULT '',
    snapshot_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(snapshot_json)),
    created_at TEXT NOT NULL,
    UNIQUE(kind,source_id)
);
CREATE INDEX idx_research_timeline_task_sequence ON research_timeline(research_task_id,sequence);
CREATE TRIGGER research_timeline_immutable BEFORE UPDATE ON research_timeline
BEGIN SELECT RAISE(ABORT,'Research timeline entries are immutable'); END;

-- Reserve both visible turns at Chat Run creation. Streaming updates keep the
-- same entry identity; internal stage prompts remain in audit, not in the feed.
CREATE TRIGGER research_timeline_chat_insert AFTER INSERT ON runs
BEGIN
    INSERT INTO research_timeline(research_task_id,workflow_run_id,kind,source_id,created_at)
    SELECT wr.research_task_id,wr.id,'message',m.id,m.created_at
    FROM workflow_conversations wc JOIN workflow_runs wr ON wr.id=wc.workflow_run_id
    JOIN messages m ON m.id IN (NEW.user_message_id,NEW.assistant_message_id)
    WHERE wc.conversation_id=NEW.conversation_id AND wr.research_task_id<>''
    ORDER BY CASE m.role WHEN 'user' THEN 0 ELSE 1 END;
END;

-- Capture interaction contents in the same transaction as the authoritative
-- lifecycle event, before a later retry can reset the mutable step row.
CREATE TRIGGER research_timeline_event_insert AFTER INSERT ON workflow_events
WHEN NEW.event_type IN (
    'workflow.created','workflow.human_confirmation_requested','workflow.agent_stage_review_requested',
    'workflow.human_decided','workflow.failed','workflow.interrupted','workflow.outcome_unknown',
    'workflow.completed','workflow.paused','workflow.resumed','workflow.cancel_requested',
    'workflow.review_revision_queued','workflow.user_revision_queued','workflow.upstream_revision_queued',
    'workflow.step_retry_queued','research.route_adopted','research.clarification_answered','research.replanned',
    'workflow.tool_proposed'
)
BEGIN
    INSERT INTO research_timeline(research_task_id,workflow_run_id,kind,source_id,event_type,snapshot_json,created_at)
    SELECT wr.research_task_id,wr.id,'event',NEW.id,NEW.event_type,json_object(
        'event',json(NEW.payload_json),'workflowName',wr.workflow_name,'workflowPurpose',wr.workflow_purpose,
        'status',wr.status,'errorCode',wr.error_code,'errorMessage',wr.error_message,
        'inputs',CASE WHEN NEW.event_type='workflow.created' THEN json(wr.inputs_json) ELSE json('{}') END,
        'outputs',CASE WHEN NEW.event_type='workflow.completed' THEN json(wr.outputs_json) ELSE json('{}') END,
        'step',json_object('id',COALESCE(ws.id,''),'nodeId',COALESCE(ws.node_id,''),'nodeKind',COALESCE(ws.node_kind,''),
            'attempt',COALESCE(ws.attempt,0),'status',COALESCE(ws.status,''),
            'input',json(COALESCE(ws.input_json,'{}')),'output',json(COALESCE(ws.output_json,'{}')),
            'errorCode',COALESCE(ws.error_code,''),'errorMessage',COALESCE(ws.error_message,'')),
        'nodeName',COALESCE(json_extract(node.value,'$.name'),''),
        'prompt',COALESCE(json_extract(node.value,'$.prompt'),''),
        'decision',json(COALESCE((SELECT json_object('approved',json(CASE WHEN approved THEN 'true' ELSE 'false' END),'note',note,'context',json(context_json)) FROM workflow_human_decisions WHERE workflow_run_id=wr.id AND workflow_step_id=ws.id ORDER BY rowid DESC LIMIT 1),'{}'))) ,NEW.created_at
    FROM workflow_runs wr
    LEFT JOIN workflow_steps ws ON ws.workflow_run_id=wr.id AND ws.id=json_extract(NEW.payload_json,'$.stepId')
    LEFT JOIN json_each(wr.compilation_json,'$.nodes') node ON json_extract(node.value,'$.id')=ws.node_id
    WHERE wr.id=NEW.workflow_run_id AND wr.research_task_id<>'';
END;

CREATE TRIGGER research_timeline_proposal_insert AFTER INSERT ON research_revision_proposals
BEGIN
    INSERT INTO research_timeline(research_task_id,workflow_run_id,kind,source_id,created_at)
    SELECT research_task_id,id,'proposal',NEW.id,NEW.created_at FROM workflow_runs WHERE id=NEW.workflow_run_id AND research_task_id<>'';
END;

CREATE TRIGGER research_timeline_registration_insert AFTER INSERT ON artifact_lineage
WHEN NEW.source_workflow_run_id IS NOT NULL
BEGIN
    INSERT INTO research_timeline(research_task_id,workflow_run_id,kind,source_id,snapshot_json,created_at)
    SELECT wr.research_task_id,wr.id,'registration',NEW.id,
        json_object('name',a.name,'outputName',json_extract(v.provenance_json,'$.extra.workflowDeliverable'),'versionId',v.id),NEW.created_at
    FROM workflow_runs wr JOIN artifact_versions v ON v.id=NEW.artifact_version_id JOIN artifacts a ON a.id=v.artifact_id
    WHERE wr.id=NEW.source_workflow_run_id AND wr.research_task_id<>'' AND json_extract(v.provenance_json,'$.extra.workflowDeliverable') IS NOT NULL;
END;

-- Recover old deliveries only from preserved revision evidence, never from
-- today's outputs. Other missing historical snapshots remain explicitly absent.
INSERT INTO research_timeline(research_task_id,workflow_run_id,kind,source_id,event_type,snapshot_json,created_at)
SELECT task_id,run_id,kind,source_id,event_type,snapshot,created_at FROM (
    SELECT wr.research_task_id task_id,wr.id run_id,'message' kind,m.id source_id,'' event_type,'{}' snapshot,m.created_at,
        CASE m.role WHEN 'user' THEN 1 ELSE 2 END tie,m.rowid source_order
    FROM workflow_runs wr JOIN workflow_conversations wc ON wc.workflow_run_id=wr.id JOIN messages m ON m.conversation_id=wc.conversation_id
    WHERE wr.research_task_id<>''
    UNION ALL
    SELECT wr.research_task_id,wr.id,'event',e.id,e.event_type,
        json_object('event',json(e.payload_json),'workflowName',wr.workflow_name,'workflowPurpose',wr.workflow_purpose,'historical',json('true'),
            'inputs',CASE WHEN e.event_type='workflow.created' THEN json(wr.inputs_json) ELSE json('{}') END,
            'outputs',CASE WHEN e.event_type='workflow.completed' THEN json(COALESCE(
                (SELECT json_extract(p.previous_snapshot_json,'$.run.outputs') FROM research_revision_proposals p WHERE p.workflow_run_id=wr.id AND p.status='confirmed' AND p.confirmed_at>=e.created_at ORDER BY p.rowid LIMIT 1),
                CASE WHEN wr.status='completed' AND e.sequence=(SELECT MAX(last.sequence) FROM workflow_events last WHERE last.workflow_run_id=wr.id AND last.event_type='workflow.completed') THEN wr.outputs_json END,'{}')) ELSE json('{}') END),
        e.created_at,0,e.rowid
    FROM workflow_events e JOIN workflow_runs wr ON wr.id=e.workflow_run_id WHERE wr.research_task_id<>'' AND e.event_type IN (
        'workflow.created','workflow.human_confirmation_requested','workflow.agent_stage_review_requested','workflow.human_decided',
        'workflow.failed','workflow.interrupted','workflow.outcome_unknown','workflow.completed','workflow.paused','workflow.resumed',
        'workflow.cancel_requested','workflow.review_revision_queued','workflow.user_revision_queued','workflow.upstream_revision_queued',
        'workflow.step_retry_queued','research.route_adopted','research.clarification_answered','research.replanned','workflow.tool_proposed')
    UNION ALL
    SELECT wr.research_task_id,wr.id,'proposal',p.id,'','{}',p.created_at,3,p.rowid FROM research_revision_proposals p JOIN workflow_runs wr ON wr.id=p.workflow_run_id WHERE wr.research_task_id<>''
    UNION ALL
    SELECT wr.research_task_id,wr.id,'registration',l.id,'',json_object('name',a.name,'outputName',json_extract(v.provenance_json,'$.extra.workflowDeliverable'),'versionId',v.id),l.created_at,4,l.rowid
    FROM artifact_lineage l JOIN workflow_runs wr ON wr.id=l.source_workflow_run_id JOIN artifact_versions v ON v.id=l.artifact_version_id JOIN artifacts a ON a.id=v.artifact_id
    WHERE wr.research_task_id<>'' AND json_extract(v.provenance_json,'$.extra.workflowDeliverable') IS NOT NULL
) ORDER BY substr(created_at,1,19),substr(CASE WHEN substr(created_at,20,1)='.' THEN substr(created_at,21,length(created_at)-21) ELSE '' END || '000000000',1,9),tie,source_order;
