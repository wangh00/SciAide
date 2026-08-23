-- Migration 000034 conservatively collapsed legacy usage events by whole
-- seconds. Recover distinct requests from the durable events without changing
-- existing request IDs or turn indexes. Only near-simultaneous snapshots with
-- the same input/output totals are treated as one cumulative provider update.
CREATE TEMP TABLE legacy_usage_repair_000037 (
    event_id TEXT PRIMARY KEY NOT NULL,
    run_id TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    timestamp TEXT NOT NULL,
    input_tokens INTEGER NOT NULL,
    fresh_input_tokens INTEGER NOT NULL,
    output_tokens INTEGER NOT NULL,
    reasoning_tokens INTEGER NOT NULL,
    cached_input_tokens INTEGER NOT NULL,
    cache_write_tokens INTEGER NOT NULL,
    cache_details_reported INTEGER NOT NULL
);

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
        CASE WHEN json_extract(e.payload_json, '$.cacheDetailsReported') = 1 THEN 1 ELSE 0 END AS cache_details_reported
    FROM run_events e
    JOIN runs r ON r.id = e.aggregate_id
    WHERE e.event_type = 'usage.updated'
), ordered_usage AS (
    SELECT raw_usage.*,
        LAG(timestamp) OVER (PARTITION BY run_id ORDER BY timestamp, sequence) AS previous_timestamp,
        LAG(input_tokens) OVER (PARTITION BY run_id ORDER BY timestamp, sequence) AS previous_input_tokens,
        LAG(output_tokens) OVER (PARTITION BY run_id ORDER BY timestamp, sequence) AS previous_output_tokens
    FROM raw_usage
), marked_usage AS (
    SELECT ordered_usage.*,
        CASE
            WHEN input_tokens = previous_input_tokens
             AND output_tokens = previous_output_tokens
             AND (julianday(timestamp) - julianday(previous_timestamp)) * 86400.0 BETWEEN 0 AND 0.05
            THEN 0
            ELSE 1
        END AS starts_request
    FROM ordered_usage
), clustered_usage AS (
    SELECT marked_usage.*,
        SUM(starts_request) OVER (PARTITION BY run_id ORDER BY timestamp, sequence ROWS UNBOUNDED PRECEDING) AS request_group
    FROM marked_usage
), ranked_usage AS (
    SELECT clustered_usage.*,
        ROW_NUMBER() OVER (
            PARTITION BY run_id, request_group
            ORDER BY cached_input_tokens + cache_write_tokens DESC, timestamp DESC, sequence DESC
        ) AS duplicate_rank
    FROM clustered_usage
)
INSERT INTO legacy_usage_repair_000037(
    event_id, run_id, sequence, timestamp,
    input_tokens, fresh_input_tokens, output_tokens, reasoning_tokens,
    cached_input_tokens, cache_write_tokens, cache_details_reported
)
SELECT
    event_id, run_id, sequence, timestamp,
    input_tokens, fresh_input_tokens, output_tokens, reasoning_tokens,
    cached_input_tokens, cache_write_tokens, cache_details_reported
FROM ranked_usage
WHERE duplicate_rank = 1
  AND NOT EXISTS (SELECT 1 FROM model_request_usage u WHERE u.id = ranked_usage.event_id);

INSERT INTO model_request_usage(
    id, run_id, turn_index, request_kind, model_profile_id, model_id, api_protocol,
    input_tokens, fresh_input_tokens, output_tokens, reasoning_tokens,
    cached_input_tokens, cache_write_tokens, cache_details_reported,
    started_at, completed_at, duration_millis
)
SELECT
    repair.event_id,
    repair.run_id,
    COALESCE((SELECT MAX(existing.turn_index) FROM model_request_usage existing WHERE existing.run_id = repair.run_id), 0)
        + ROW_NUMBER() OVER (PARTITION BY repair.run_id ORDER BY repair.timestamp, repair.sequence),
    'legacy',
    runs.model_profile_id,
    runs.model_id,
    runs.api_protocol,
    repair.input_tokens,
    repair.fresh_input_tokens,
    repair.output_tokens,
    repair.reasoning_tokens,
    repair.cached_input_tokens,
    repair.cache_write_tokens,
    repair.cache_details_reported,
    repair.timestamp,
    repair.timestamp,
    0
FROM legacy_usage_repair_000037 repair
JOIN runs ON runs.id = repair.run_id;

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
WHERE id IN (SELECT DISTINCT run_id FROM legacy_usage_repair_000037);

DROP TABLE legacy_usage_repair_000037;
