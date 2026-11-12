-- A separate durable wait marker, not a permission approval. The underlying
-- active-run reservation remains intact; no existing Run/table is rebuilt.
ALTER TABLE runs ADD COLUMN pending_user_input_call_id TEXT NOT NULL DEFAULT '';
