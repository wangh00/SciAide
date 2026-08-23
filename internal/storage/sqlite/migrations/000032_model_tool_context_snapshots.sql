ALTER TABLE tool_results
    ADD COLUMN model_context_text TEXT NOT NULL DEFAULT '';

ALTER TABLE tool_results
    ADD COLUMN model_context_version INTEGER NOT NULL DEFAULT 0
        CHECK (model_context_version >= 0);
