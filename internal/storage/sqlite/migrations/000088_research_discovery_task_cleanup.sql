-- Discovery is task-local; published materials retain their separate provenance.
DELETE FROM research_queries WHERE id IN (
 SELECT o.query_id FROM research_query_origins o JOIN research_tasks t ON t.id=o.task_id
 WHERE t.archived_at IS NOT NULL
);
DELETE FROM research_candidate_reviews WHERE task_id IN (SELECT id FROM research_tasks WHERE archived_at IS NOT NULL);

CREATE TRIGGER research_discovery_cleanup_on_archive
AFTER UPDATE OF archived_at ON research_tasks
WHEN NEW.archived_at IS NOT NULL
BEGIN
 DELETE FROM research_queries WHERE project_id=NEW.project_id AND id IN (SELECT query_id FROM research_query_origins WHERE task_id=NEW.id);
 DELETE FROM research_candidate_reviews WHERE project_id=NEW.project_id AND task_id=NEW.id;
END;
