CREATE TABLE model_request_usage (
    id TEXT PRIMARY KEY NOT NULL,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    turn_index INTEGER NOT NULL CHECK (turn_index > 0),
    request_kind TEXT NOT NULL CHECK (request_kind IN ('conversation', 'compaction', 'legacy')),
    model_profile_id TEXT NOT NULL,
    model_id TEXT NOT NULL,
    api_protocol TEXT NOT NULL,
    input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    fresh_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (fresh_input_tokens >= 0),
    output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    reasoning_tokens INTEGER NOT NULL DEFAULT 0 CHECK (reasoning_tokens >= 0),
    cached_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cached_input_tokens >= 0),
    cache_write_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_write_tokens >= 0),
    cache_details_reported INTEGER NOT NULL DEFAULT 0 CHECK (cache_details_reported IN (0, 1)),
    started_at TEXT NOT NULL,
    completed_at TEXT NOT NULL,
    duration_millis INTEGER NOT NULL DEFAULT 0 CHECK (duration_millis >= 0),
    UNIQUE (run_id, turn_index)
);

CREATE INDEX idx_model_request_usage_started
    ON model_request_usage(started_at DESC, id DESC);
CREATE INDEX idx_model_request_usage_model_started
    ON model_request_usage(model_profile_id, model_id, started_at DESC);
CREATE INDEX idx_model_request_usage_run_turn
    ON model_request_usage(run_id, turn_index);

-- Older builds could publish two cumulative usage snapshots for one OpenAI
-- request. Keep the snapshot with the richest cache fields within the same
-- run/second/input/output group, then assign stable historical turn indexes.
WITH raw_usage AS (
    SELECT
        e.event_id,
        e.aggregate_id AS run_id,
        e.sequence,
        e.timestamp,
        MAX(CAST(COALESCE(json_extract(e.payload_json, '$.inputTokens'), 0) AS INTEGER), 0) AS input_tokens,
        MAX(CAST(COALESCE(json_extract(e.payload_json, '$.freshInputTokens'), 0) AS INTEGER), 0) AS fresh_input_tokens,
        MAX(CAST(COALESCE(json_extract(e.payload_json, '$.outputTokens'), 0) AS INTEGER), 0) AS output_tokens,
        MAX(CAST(COALESCE(json_extract(e.payload_json, '$.reasoningTokens'), 0) AS INTEGER), 0) AS reasoning_tokens,
        MAX(CAST(COALESCE(json_extract(e.payload_json, '$.cachedInputTokens'), 0) AS INTEGER), 0) AS cached_input_tokens,
        MAX(CAST(COALESCE(json_extract(e.payload_json, '$.cacheWriteTokens'), 0) AS INTEGER), 0) AS cache_write_tokens,
        CASE WHEN json_extract(e.payload_json, '$.cacheDetailsReported') = 1 THEN 1 ELSE 0 END AS cache_details_reported,
        ROW_NUMBER() OVER (
            PARTITION BY e.aggregate_id, substr(e.timestamp, 1, 19),
                CAST(COALESCE(json_extract(e.payload_json, '$.inputTokens'), 0) AS INTEGER),
                CAST(COALESCE(json_extract(e.payload_json, '$.outputTokens'), 0) AS INTEGER)
            ORDER BY
                CAST(COALESCE(json_extract(e.payload_json, '$.cachedInputTokens'), 0) AS INTEGER) +
                CAST(COALESCE(json_extract(e.payload_json, '$.cacheWriteTokens'), 0) AS INTEGER) DESC,
                e.timestamp DESC,
                e.sequence DESC
        ) AS duplicate_rank
    FROM run_events e
    JOIN runs r ON r.id = e.aggregate_id
    WHERE e.event_type = 'usage.updated'
), numbered_usage AS (
    SELECT raw_usage.*,
        ROW_NUMBER() OVER (PARTITION BY run_id ORDER BY timestamp, sequence) AS turn_index
    FROM raw_usage
    WHERE duplicate_rank = 1
)
INSERT INTO model_request_usage(
    id, run_id, turn_index, request_kind, model_profile_id, model_id, api_protocol,
    input_tokens, fresh_input_tokens, output_tokens, reasoning_tokens,
    cached_input_tokens, cache_write_tokens, cache_details_reported,
    started_at, completed_at, duration_millis
)
SELECT
    n.event_id, n.run_id, n.turn_index, 'legacy', r.model_profile_id, r.model_id, r.api_protocol,
    n.input_tokens, n.fresh_input_tokens, n.output_tokens, n.reasoning_tokens,
    n.cached_input_tokens, n.cache_write_tokens, n.cache_details_reported,
    n.timestamp, n.timestamp, 0
FROM numbered_usage n
JOIN runs r ON r.id = n.run_id;

-- Preserve aggregate-only fixtures/databases which predate durable usage
-- events. They remain visibly marked as a legacy aggregate in request detail.
INSERT INTO model_request_usage(
    id, run_id, turn_index, request_kind, model_profile_id, model_id, api_protocol,
    input_tokens, fresh_input_tokens, output_tokens, reasoning_tokens,
    cached_input_tokens, cache_write_tokens, cache_details_reported,
    started_at, completed_at, duration_millis
)
SELECT
    'legacy:' || r.id, r.id, MAX(r.model_turns, 1), 'legacy', r.model_profile_id, r.model_id, r.api_protocol,
    r.input_tokens, r.fresh_input_tokens, r.output_tokens, r.reasoning_tokens,
    r.cached_input_tokens, r.cache_write_tokens, CASE WHEN r.cache_reported_turns > 0 THEN 1 ELSE 0 END,
    COALESCE(r.started_at, r.created_at), COALESCE(r.completed_at, r.updated_at), 0
FROM runs r
WHERE NOT EXISTS (SELECT 1 FROM model_request_usage u WHERE u.run_id = r.id)
  AND (r.input_tokens > 0 OR r.fresh_input_tokens > 0 OR r.output_tokens > 0 OR
       r.reasoning_tokens > 0 OR r.cached_input_tokens > 0 OR r.cache_write_tokens > 0);

-- Rebuild only runs for which request usage could be recovered. This removes
-- duplicate historical snapshots without changing unrelated run state.
UPDATE runs
SET
    input_tokens = COALESCE((SELECT SUM(u.input_tokens) FROM model_request_usage u WHERE u.run_id = runs.id), 0),
    fresh_input_tokens = COALESCE((SELECT SUM(u.fresh_input_tokens) FROM model_request_usage u WHERE u.run_id = runs.id), 0),
    output_tokens = COALESCE((SELECT SUM(u.output_tokens) FROM model_request_usage u WHERE u.run_id = runs.id), 0),
    reasoning_tokens = COALESCE((SELECT SUM(u.reasoning_tokens) FROM model_request_usage u WHERE u.run_id = runs.id), 0),
    cached_input_tokens = COALESCE((SELECT SUM(u.cached_input_tokens) FROM model_request_usage u WHERE u.run_id = runs.id), 0),
    cache_write_tokens = COALESCE((SELECT SUM(u.cache_write_tokens) FROM model_request_usage u WHERE u.run_id = runs.id), 0),
    cache_reported_turns = COALESCE((SELECT SUM(u.cache_details_reported) FROM model_request_usage u WHERE u.run_id = runs.id), 0),
    cache_reported_fresh_input_tokens = COALESCE((SELECT SUM(CASE WHEN u.cache_details_reported = 1 THEN u.fresh_input_tokens ELSE 0 END) FROM model_request_usage u WHERE u.run_id = runs.id), 0),
    cache_hit_turns = COALESCE((SELECT SUM(CASE WHEN u.cache_details_reported = 1 AND u.cached_input_tokens > 0 THEN 1 ELSE 0 END) FROM model_request_usage u WHERE u.run_id = runs.id), 0)
WHERE EXISTS (SELECT 1 FROM model_request_usage u WHERE u.run_id = runs.id);
