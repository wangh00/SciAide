ALTER TABLE model_profile_models ADD COLUMN reasoning_verified_levels_json TEXT NOT NULL DEFAULT '[]'
    CHECK (json_valid(reasoning_verified_levels_json));

ALTER TABLE model_profile_models ADD COLUMN reasoning_rejected_levels_json TEXT NOT NULL DEFAULT '[]'
    CHECK (json_valid(reasoning_rejected_levels_json));

ALTER TABLE model_profile_models ADD COLUMN reasoning_control_unsupported INTEGER NOT NULL DEFAULT 0
    CHECK (reasoning_control_unsupported IN (0, 1));

ALTER TABLE model_profile_models ADD COLUMN reasoning_last_requested_level TEXT NOT NULL DEFAULT ''
    CHECK (reasoning_last_requested_level IN ('', 'low', 'medium', 'high', 'xhigh', 'max'));

ALTER TABLE model_profile_models ADD COLUMN reasoning_last_resolved_level TEXT NOT NULL DEFAULT ''
    CHECK (reasoning_last_resolved_level IN ('', 'low', 'medium', 'high', 'xhigh', 'max'));

ALTER TABLE model_profile_models ADD COLUMN reasoning_wire_mode TEXT NOT NULL DEFAULT ''
    CHECK (reasoning_wire_mode IN ('', 'openai_effort', 'responses_effort', 'anthropic_adaptive', 'anthropic_legacy', 'provider_default'));
