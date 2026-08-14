ALTER TABLE runs ADD COLUMN reasoning_observed INTEGER NOT NULL DEFAULT 0
    CHECK (reasoning_observed IN (0, 1));

ALTER TABLE runs ADD COLUMN reasoning_signature_observed INTEGER NOT NULL DEFAULT 0
    CHECK (reasoning_signature_observed IN (0, 1));

ALTER TABLE runs ADD COLUMN reasoning_tokens INTEGER NOT NULL DEFAULT 0
    CHECK (reasoning_tokens >= 0);
