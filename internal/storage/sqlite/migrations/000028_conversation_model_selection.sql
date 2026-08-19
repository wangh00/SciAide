ALTER TABLE conversations ADD COLUMN model_profile_id TEXT NOT NULL DEFAULT '';
ALTER TABLE conversations ADD COLUMN model_id TEXT NOT NULL DEFAULT '';

-- Preserve the model most recently used by existing conversations. Empty
-- conversations keep an empty preference and let the client choose a valid
-- configured default.
UPDATE conversations
SET model_profile_id = COALESCE((
        SELECT runs.model_profile_id
        FROM runs
        WHERE runs.conversation_id = conversations.id
        ORDER BY runs.created_at DESC, runs.id DESC
        LIMIT 1
    ), ''),
    model_id = COALESCE((
        SELECT runs.model_id
        FROM runs
        WHERE runs.conversation_id = conversations.id
        ORDER BY runs.created_at DESC, runs.id DESC
        LIMIT 1
    ), '');
