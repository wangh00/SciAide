-- User-facing material metadata is independent of rebuildable knowledge indexes.
CREATE TABLE material_library (
 attachment_id TEXT PRIMARY KEY REFERENCES attachments(id) ON DELETE CASCADE,
 title TEXT NOT NULL DEFAULT '',
 notes TEXT NOT NULL DEFAULT '',
 archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1))
);
