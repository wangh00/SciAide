ALTER TABLE runs ADD COLUMN fresh_input_tokens INTEGER NOT NULL DEFAULT 0
    CHECK (fresh_input_tokens >= 0);
ALTER TABLE runs ADD COLUMN cache_reported_fresh_input_tokens INTEGER NOT NULL DEFAULT 0
    CHECK (cache_reported_fresh_input_tokens >= 0);

-- Migration 000010 only received OpenAI-compatible totals, whose input_tokens
-- include cache reads/creation. Preserve those rows using the new four-bucket
-- semantics. Mixed historical reporting inside one run cannot be separated,
-- so the best durable approximation is the normalized run total.
UPDATE runs SET fresh_input_tokens = CASE
    WHEN input_tokens >= cached_input_tokens + cache_write_tokens
        THEN input_tokens - cached_input_tokens - cache_write_tokens
    ELSE 0
END;
UPDATE runs SET cache_reported_fresh_input_tokens = fresh_input_tokens
WHERE cache_reported_turns > 0;

CREATE INDEX idx_runs_usage_dimensions
    ON runs(created_at, model_profile_id, model_id);
