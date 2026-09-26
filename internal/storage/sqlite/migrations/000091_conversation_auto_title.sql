-- Opt in only for newly created untitled chats; never rename legacy conversations.
ALTER TABLE conversations ADD COLUMN auto_title_pending INTEGER NOT NULL DEFAULT 0 CHECK (auto_title_pending IN (0, 1));
