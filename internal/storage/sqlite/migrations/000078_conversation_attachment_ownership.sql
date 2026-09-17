-- Conversation-local attachment ownership must resolve to a live conversation
-- in the same project. Remove historical dangling ownership rows before the
-- stronger guards are installed; their immutable bytes remain in the shared
-- content-addressed object pool and can still be referenced by other rows.
DELETE FROM attachments
WHERE scope_kind='conversation'
  AND NOT EXISTS (
      SELECT 1 FROM conversations c
      WHERE research_task_id='conversation:' || c.id
        AND c.project_id=attachments.project_id
  );

CREATE TRIGGER conversation_attachment_owner_guard_insert
BEFORE INSERT ON attachments
WHEN NEW.scope_kind='conversation'
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM conversations c
        WHERE NEW.research_task_id='conversation:' || c.id
          AND c.project_id=NEW.project_id
    ) THEN RAISE(ABORT, 'conversation attachment does not belong to project') END;
END;

CREATE TRIGGER conversation_attachment_owner_guard_update
BEFORE UPDATE ON attachments
WHEN NEW.scope_kind='conversation'
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM conversations c
        WHERE NEW.research_task_id='conversation:' || c.id
          AND c.project_id=NEW.project_id
    ) THEN RAISE(ABORT, 'conversation attachment does not belong to project') END;
END;

-- Conversation uploads have no user-visible owner after their conversation is
-- deleted. Delete only the ownership rows; object bytes are deliberately not
-- removed here because another scope may reference the same content object.
CREATE TRIGGER conversation_attachment_cleanup_after_delete
AFTER DELETE ON conversations
BEGIN
    DELETE FROM attachments
    WHERE project_id=OLD.project_id
      AND scope_kind='conversation'
      AND research_task_id='conversation:' || OLD.id;
END;
