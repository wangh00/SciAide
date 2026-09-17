CREATE TABLE research_query_origins (
 query_id TEXT PRIMARY KEY REFERENCES research_queries(id) ON DELETE CASCADE,
 task_id TEXT NOT NULL,
 task_title TEXT NOT NULL
);
INSERT OR IGNORE INTO research_query_origins(query_id,task_id,task_title)
SELECT q.id,t.id,t.title FROM research_queries q
JOIN tool_results result ON json_extract(result.structured_json,'$.queryId')=q.id OR EXISTS(SELECT 1 FROM json_each(result.structured_json,'$.queryIds') WHERE value=q.id)
JOIN tool_calls call ON call.id=result.tool_call_id AND call.tool_name='builtin.research.workflow.search'
JOIN workflow_runs run ON run.id=call.workflow_run_id AND run.project_id=q.project_id
JOIN research_tasks t ON t.id=run.research_task_id;
CREATE TRIGGER research_query_origin_on_result AFTER INSERT ON tool_results
WHEN EXISTS(SELECT 1 FROM tool_calls WHERE id=NEW.tool_call_id AND tool_name='builtin.research.workflow.search')
BEGIN
 INSERT OR IGNORE INTO research_query_origins(query_id,task_id,task_title)
 SELECT q.id,t.id,t.title FROM research_queries q
 JOIN tool_calls call ON call.id=NEW.tool_call_id
 JOIN workflow_runs run ON run.id=call.workflow_run_id AND run.project_id=q.project_id
 JOIN research_tasks t ON t.id=run.research_task_id
 WHERE json_extract(NEW.structured_json,'$.queryId')=q.id OR EXISTS(SELECT 1 FROM json_each(NEW.structured_json,'$.queryIds') WHERE value=q.id);
END;
