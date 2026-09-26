-- Historical Runs retain their tool scope. New chat Runs explicitly freeze opt-in.
ALTER TABLE runs ADD COLUMN web_search_disabled INTEGER NOT NULL DEFAULT 0 CHECK (web_search_disabled IN (0, 1));
