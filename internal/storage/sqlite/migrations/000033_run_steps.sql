CREATE TABLE run_steps (
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    turn_index INTEGER NOT NULL CHECK (turn_index > 0),
    commentary_text TEXT NOT NULL DEFAULT '' CHECK (length(commentary_text) <= 100000),
    reasoning_summary TEXT NOT NULL DEFAULT '' CHECK (length(reasoning_summary) <= 4000),
    reasoning_observed INTEGER NOT NULL DEFAULT 0 CHECK (reasoning_observed IN (0, 1)),
    reasoning_signature_observed INTEGER NOT NULL DEFAULT 0 CHECK (reasoning_signature_observed IN (0, 1)),
    created_at TEXT NOT NULL,
    completed_at TEXT NOT NULL,
    PRIMARY KEY (run_id, turn_index)
);

CREATE INDEX idx_run_steps_run_turn
    ON run_steps(run_id, turn_index);
