CREATE TABLE provider_turn_items (
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    turn_index INTEGER NOT NULL CHECK (turn_index > 0),
    api_protocol TEXT NOT NULL
        CHECK (api_protocol IN ('openai_chat_completions', 'openai_responses', 'anthropic_messages')),
    item_ordinal INTEGER NOT NULL CHECK (item_ordinal >= 0),
    item_type TEXT NOT NULL CHECK (length(trim(item_type)) > 0),
    provider_call_id TEXT NOT NULL DEFAULT '',
    payload_json TEXT NOT NULL CHECK (json_valid(payload_json) AND json_type(payload_json) = 'object'),
    created_at TEXT NOT NULL,
    PRIMARY KEY (run_id, turn_index, item_ordinal)
);

CREATE INDEX idx_provider_turn_items_run_turn
    ON provider_turn_items(run_id, turn_index, item_ordinal);
