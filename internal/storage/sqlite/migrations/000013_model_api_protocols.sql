ALTER TABLE model_profiles ADD COLUMN api_protocol TEXT NOT NULL DEFAULT 'openai_chat_completions'
    CHECK (api_protocol IN ('openai_chat_completions', 'openai_responses', 'anthropic_messages'));

ALTER TABLE runs ADD COLUMN api_protocol TEXT NOT NULL DEFAULT 'openai_chat_completions'
    CHECK (api_protocol IN ('openai_chat_completions', 'openai_responses', 'anthropic_messages'));
