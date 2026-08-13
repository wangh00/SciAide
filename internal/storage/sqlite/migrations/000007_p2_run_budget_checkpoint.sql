ALTER TABLE runs
    ADD COLUMN model_turns INTEGER NOT NULL DEFAULT 0 CHECK (model_turns >= 0);
