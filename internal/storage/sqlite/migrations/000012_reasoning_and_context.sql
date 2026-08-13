ALTER TABLE model_profile_models ADD COLUMN reasoning_levels_json TEXT NOT NULL DEFAULT '[]'
    CHECK (json_valid(reasoning_levels_json));
ALTER TABLE model_profile_models ADD COLUMN reasoning_capability_source TEXT NOT NULL DEFAULT 'unsupported'
    CHECK (reasoning_capability_source IN ('manual', 'provider', 'builtin', 'inferred', 'unsupported'));

ALTER TABLE conversations ADD COLUMN reasoning_level TEXT NOT NULL DEFAULT 'medium'
    CHECK (reasoning_level IN ('low', 'medium', 'high', 'xhigh', 'max'));

ALTER TABLE runs ADD COLUMN requested_reasoning_level TEXT NOT NULL DEFAULT 'medium'
    CHECK (requested_reasoning_level IN ('low', 'medium', 'high', 'xhigh', 'max'));
ALTER TABLE runs ADD COLUMN resolved_reasoning_level TEXT NOT NULL DEFAULT ''
    CHECK (resolved_reasoning_level IN ('', 'low', 'medium', 'high', 'xhigh', 'max'));
ALTER TABLE runs ADD COLUMN context_window_tokens INTEGER NOT NULL DEFAULT 200000
    CHECK (context_window_tokens > 0);
ALTER TABLE runs ADD COLUMN context_compacted INTEGER NOT NULL DEFAULT 0
    CHECK (context_compacted IN (0, 1));
