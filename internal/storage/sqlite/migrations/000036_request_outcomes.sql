ALTER TABLE model_request_usage
    ADD COLUMN status_code INTEGER NOT NULL DEFAULT 200 CHECK (status_code BETWEEN 100 AND 599);

ALTER TABLE model_request_usage
    ADD COLUMN error_code TEXT NOT NULL DEFAULT '';

ALTER TABLE model_request_usage
    ADD COLUMN error_message TEXT NOT NULL DEFAULT '';

ALTER TABLE model_request_usage
    ADD COLUMN first_token_millis INTEGER CHECK (first_token_millis IS NULL OR first_token_millis >= 0);

ALTER TABLE model_request_usage
    ADD COLUMN is_streaming INTEGER NOT NULL DEFAULT 1 CHECK (is_streaming IN (0, 1));

-- Migration 000034 could recover a usage row before terminal validation. Use
-- the durable turn journal to repair only outcomes whose failure/interruption
-- is proven; successful and legacy-only rows keep the conservative 200 value.
UPDATE model_request_usage
SET
    status_code = CASE
        WHEN (SELECT j.status FROM model_turn_journal j
              WHERE j.run_id = model_request_usage.run_id
                AND j.turn_index = model_request_usage.turn_index) = 'interrupted' THEN 499
        ELSE COALESCE((
            SELECT CASE r.error_code
                WHEN 'MODEL_AUTH_FAILED' THEN 401
                WHEN 'MODEL_NOT_FOUND' THEN 404
                WHEN 'MODEL_TIMEOUT' THEN 408
                WHEN 'MODEL_RATE_LIMITED' THEN 429
                WHEN 'MODEL_UNAVAILABLE' THEN 503
                ELSE 502
            END
            FROM runs r WHERE r.id = model_request_usage.run_id
        ), 502)
    END,
    error_code = COALESCE((
        SELECT NULLIF(r.error_code, '') FROM runs r WHERE r.id = model_request_usage.run_id
    ), CASE
        WHEN (SELECT j.status FROM model_turn_journal j
              WHERE j.run_id = model_request_usage.run_id
                AND j.turn_index = model_request_usage.turn_index) = 'interrupted'
        THEN 'REQUEST_INTERRUPTED'
        ELSE 'MODEL_REQUEST_FAILED'
    END),
    error_message = SUBSTR(COALESCE((
        SELECT NULLIF(TRIM(
            COALESCE(r.error_message, '') ||
            CASE WHEN r.error_details <> '' THEN CHAR(10) || CHAR(10) || r.error_details ELSE '' END
        ), '')
        FROM runs r WHERE r.id = model_request_usage.run_id
    ), '历史模型请求未正常完成。'), 1, 8192)
WHERE EXISTS (
    SELECT 1 FROM model_turn_journal j
    WHERE j.run_id = model_request_usage.run_id
      AND j.turn_index = model_request_usage.turn_index
      AND j.status IN ('failed', 'interrupted')
);

CREATE INDEX idx_model_request_usage_status_started
    ON model_request_usage(status_code, started_at DESC);
