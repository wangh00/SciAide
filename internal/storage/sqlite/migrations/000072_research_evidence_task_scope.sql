-- Research bibliography metadata is project-wide, but imported material and
-- evidence snapshots belong to the research task that materialized them.
-- Empty research_task_id preserves historical/project-level imports.
ALTER TABLE research_bibliography_materials
    ADD COLUMN research_task_id TEXT NOT NULL DEFAULT ''
    CHECK (length(research_task_id) <= 128);

ALTER TABLE research_evidence_entries
    ADD COLUMN research_task_id TEXT NOT NULL DEFAULT ''
    CHECK (length(research_task_id) <= 128);

CREATE INDEX idx_research_bibliography_material_task
    ON research_bibliography_materials(project_id, bibliography_id, research_task_id, updated_at);
CREATE INDEX idx_research_evidence_task
    ON research_evidence_entries(project_id, bibliography_id, research_task_id, created_at, id);
