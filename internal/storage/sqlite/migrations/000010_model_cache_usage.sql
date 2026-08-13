ALTER TABLE runs ADD COLUMN cached_input_tokens INTEGER NOT NULL DEFAULT 0
    CHECK (cached_input_tokens >= 0);
ALTER TABLE runs ADD COLUMN cache_write_tokens INTEGER NOT NULL DEFAULT 0
    CHECK (cache_write_tokens >= 0);
ALTER TABLE runs ADD COLUMN cache_reported_turns INTEGER NOT NULL DEFAULT 0
    CHECK (cache_reported_turns >= 0);
ALTER TABLE runs ADD COLUMN cache_hit_turns INTEGER NOT NULL DEFAULT 0
    CHECK (cache_hit_turns >= 0 AND cache_hit_turns <= cache_reported_turns);
