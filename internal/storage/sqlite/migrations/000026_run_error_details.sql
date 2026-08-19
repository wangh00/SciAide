ALTER TABLE runs ADD COLUMN error_details TEXT NOT NULL DEFAULT ''
    CHECK (length(error_details) <= 8192);
