-- One agent turn can now contain an explicit image capability probe, a
-- multimodal fallback request, and the primary conversation request. Keep
-- idempotency at the provider-request ID instead of collapsing the whole turn.
CREATE TABLE model_request_usage_v39 (
    id TEXT PRIMARY KEY NOT NULL,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    turn_index INTEGER NOT NULL CHECK (turn_index > 0),
    request_kind TEXT NOT NULL CHECK (request_kind IN ('conversation', 'compaction', 'image_probe', 'multimodal_fallback', 'legacy')),
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
    status_code INTEGER NOT NULL DEFAULT 200 CHECK (status_code BETWEEN 100 AND 599),
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    first_token_millis INTEGER CHECK (first_token_millis IS NULL OR first_token_millis >= 0),
    is_streaming INTEGER NOT NULL DEFAULT 1 CHECK (is_streaming IN (0, 1)),
    started_at TEXT NOT NULL,
    completed_at TEXT NOT NULL,
    duration_millis INTEGER NOT NULL DEFAULT 0 CHECK (duration_millis >= 0)
);

INSERT INTO model_request_usage_v39(
    id,run_id,turn_index,request_kind,model_profile_id,model_id,api_protocol,
    input_tokens,fresh_input_tokens,output_tokens,reasoning_tokens,cached_input_tokens,cache_write_tokens,
    cache_details_reported,status_code,error_code,error_message,first_token_millis,is_streaming,
    started_at,completed_at,duration_millis
)
SELECT
    id,run_id,turn_index,request_kind,model_profile_id,model_id,api_protocol,
    input_tokens,fresh_input_tokens,output_tokens,reasoning_tokens,cached_input_tokens,cache_write_tokens,
    cache_details_reported,status_code,error_code,error_message,first_token_millis,is_streaming,
    started_at,completed_at,duration_millis
FROM model_request_usage;

DROP TABLE model_request_usage;
ALTER TABLE model_request_usage_v39 RENAME TO model_request_usage;

CREATE INDEX idx_model_request_usage_started
    ON model_request_usage(started_at DESC, id DESC);
CREATE INDEX idx_model_request_usage_model_started
    ON model_request_usage(model_profile_id, model_id, started_at DESC);
CREATE INDEX idx_model_request_usage_run_turn
    ON model_request_usage(run_id, turn_index);
CREATE INDEX idx_model_request_usage_status_started
    ON model_request_usage(status_code, started_at DESC);
