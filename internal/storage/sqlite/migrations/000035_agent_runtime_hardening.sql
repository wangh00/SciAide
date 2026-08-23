ALTER TABLE provider_turn_items
    ADD COLUMN provider_item_id TEXT NOT NULL DEFAULT '';

ALTER TABLE provider_turn_items
    ADD COLUMN phase TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX idx_provider_turn_item_id
    ON provider_turn_items(run_id, turn_index, provider_item_id)
    WHERE provider_item_id <> '';

CREATE TABLE run_event_sequences (
    aggregate_id TEXT PRIMARY KEY NOT NULL,
    last_sequence INTEGER NOT NULL CHECK (last_sequence > 0)
);

INSERT INTO run_event_sequences(aggregate_id, last_sequence)
SELECT aggregate_id, MAX(sequence)
FROM run_events
GROUP BY aggregate_id;

CREATE TABLE model_turn_journal (
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    turn_index INTEGER NOT NULL CHECK (turn_index > 0),
    status TEXT NOT NULL CHECK (status IN ('streaming', 'completed', 'interrupted', 'failed')),
    draft_text TEXT NOT NULL DEFAULT '',
    finish_reason TEXT NOT NULL DEFAULT '',
    provider_item_count INTEGER NOT NULL DEFAULT 0 CHECK (provider_item_count >= 0),
    started_at TEXT NOT NULL,
    completed_at TEXT,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (run_id, turn_index)
);

CREATE INDEX idx_model_turn_journal_run
    ON model_turn_journal(run_id, turn_index);
