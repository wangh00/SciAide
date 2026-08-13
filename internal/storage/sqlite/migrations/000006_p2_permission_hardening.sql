CREATE TRIGGER validate_approval_snapshot_before_insert
BEFORE INSERT ON approvals
BEGIN
    SELECT CASE WHEN NEW.resource <> trim(NEW.resource)
        THEN RAISE(ABORT, 'approval resource must be normalized') END;
    SELECT CASE WHEN NEW.requested_scope <> 'call'
        AND (NEW.risk IN ('high', 'destructive')
            OR NEW.permission_kind IN ('filesystem.external', 'process.execute', 'destructive', 'secret.use'))
        THEN RAISE(ABORT, 'dangerous approval must be call scoped') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1
        FROM tool_calls tc
        JOIN runs r ON r.id = tc.run_id
        JOIN conversations c ON c.id = r.conversation_id
        WHERE tc.id = NEW.tool_call_id
          AND tc.run_id = NEW.run_id
          AND c.project_id = NEW.project_id
          AND tc.tool_name = NEW.tool_name
          AND tc.tool_version = NEW.tool_version
          AND tc.risk = NEW.risk
          AND (
              (NEW.permission_kind = 'tool.invoke'
                  AND NEW.resource = NEW.tool_name
                  AND tc.risk IN ('moderate', 'high', 'destructive'))
              OR EXISTS (
                  SELECT 1 FROM json_each(tc.permissions_json) required
                  WHERE json_extract(required.value, '$.kind') = NEW.permission_kind
                    AND COALESCE(json_extract(required.value, '$.resource'), '') = NEW.resource
              )
          )
    ) THEN RAISE(ABORT, 'approval does not match tool call snapshot') END;
END;

CREATE TRIGGER validate_permission_grant_before_insert
BEFORE INSERT ON permission_grants
BEGIN
    SELECT CASE WHEN NEW.resource <> trim(NEW.resource)
        THEN RAISE(ABORT, 'permission grant resource must be normalized') END;
    SELECT CASE WHEN NEW.scope = 'run' AND NOT EXISTS (
        SELECT 1 FROM runs r
        JOIN conversations c ON c.id = r.conversation_id
        WHERE r.id = NEW.run_id AND c.project_id = NEW.project_id
    ) THEN RAISE(ABORT, 'run grant project mismatch') END;
END;
