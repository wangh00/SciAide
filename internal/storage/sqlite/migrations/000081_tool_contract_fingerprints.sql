-- Freeze the complete tool contract shown to the model at proposal time.
-- Empty values are retained for pre-migration calls and are accepted by the
-- executor as a legacy snapshot with the existing field-level checks.
ALTER TABLE tool_calls ADD COLUMN contract_sha256 TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_tool_calls_contract_sha256 ON tool_calls(contract_sha256);
