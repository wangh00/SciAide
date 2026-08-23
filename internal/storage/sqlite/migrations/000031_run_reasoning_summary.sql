ALTER TABLE runs
    ADD COLUMN reasoning_summary TEXT NOT NULL DEFAULT ''
    CHECK (length(reasoning_summary) <= 4000);
