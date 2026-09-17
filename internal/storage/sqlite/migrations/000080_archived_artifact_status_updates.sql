-- Archived research tasks keep their immutable outputs addressable. New
-- artifacts still require an active task (the INSERT trigger from 000075),
-- while renaming or changing the lifecycle status of an existing artifact is
-- valid historical resource management.
DROP TRIGGER IF EXISTS research_artifact_scope_guard_update;
CREATE TRIGGER research_artifact_scope_guard_update
BEFORE UPDATE ON artifacts
BEGIN
    SELECT CASE WHEN OLD.project_id IS NOT NEW.project_id
                     OR OLD.scope_kind IS NOT NEW.scope_kind
                     OR OLD.research_task_id IS NOT NEW.research_task_id
        THEN RAISE(ABORT, 'artifact resource scope is immutable') END;
    SELECT CASE WHEN OLD.current_version_id IS NOT NEW.current_version_id
                     AND NEW.scope_kind='task'
                     AND EXISTS (
                         SELECT 1 FROM research_tasks t
                         WHERE t.id=NEW.research_task_id AND t.project_id=NEW.project_id
                           AND t.status='archived'
                     )
        THEN RAISE(ABORT, 'archived research task cannot receive new artifact versions') END;
    SELECT CASE WHEN NEW.scope_kind='task' AND NOT EXISTS (
        SELECT 1 FROM research_tasks t
        WHERE t.id=NEW.research_task_id AND t.project_id=NEW.project_id
    ) THEN RAISE(ABORT, 'artifact task scope does not belong to project') END;
    SELECT CASE WHEN NEW.scope_kind IN ('project_shared','legacy_project')
                     AND trim(NEW.research_task_id)<>''
        THEN RAISE(ABORT, 'non-task artifact cannot carry research task id') END;
END;
