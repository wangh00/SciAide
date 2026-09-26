ALTER TABLE material_library ADD COLUMN collected INTEGER NOT NULL DEFAULT 0 CHECK(collected IN (0,1));
ALTER TABLE material_library ADD COLUMN content_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE material_library ADD COLUMN origin_task_title TEXT NOT NULL DEFAULT '';
