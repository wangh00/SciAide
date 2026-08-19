ALTER TABLE model_profile_models
    ADD COLUMN context_window_tokens INTEGER NOT NULL DEFAULT 200000
        CHECK (context_window_tokens BETWEEN 4096 AND 10000000);

ALTER TABLE model_profile_models
    ADD COLUMN auto_compact_token_limit INTEGER NOT NULL DEFAULT 180000
        CHECK (auto_compact_token_limit > 0 AND auto_compact_token_limit <= context_window_tokens);

ALTER TABLE model_profile_models
    ADD COLUMN context_window_source TEXT NOT NULL DEFAULT 'fallback'
        CHECK (context_window_source IN ('fallback', 'provider', 'manual', 'builtin'));

ALTER TABLE runs
    ADD COLUMN context_budget_tokens INTEGER NOT NULL DEFAULT 190000
        CHECK (context_budget_tokens > 0 AND context_budget_tokens <= context_window_tokens);

ALTER TABLE runs
    ADD COLUMN auto_compact_token_limit INTEGER NOT NULL DEFAULT 180000
        CHECK (auto_compact_token_limit > 0 AND auto_compact_token_limit <= context_budget_tokens);

ALTER TABLE runs
    ADD COLUMN context_window_source TEXT NOT NULL DEFAULT 'fallback'
        CHECK (context_window_source IN ('fallback', 'provider', 'manual', 'builtin'));
