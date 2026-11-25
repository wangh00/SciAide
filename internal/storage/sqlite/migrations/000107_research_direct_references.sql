-- Explicit reference selections belong to a direct research task before any
-- optional candidate exists. Legacy tasks keep their selections in frozen
-- starter inputs; this field is not backfilled or used as Workflow authority.
ALTER TABLE research_tasks ADD COLUMN exploration_reference_ids_json TEXT NOT NULL DEFAULT '[]'
    CHECK(json_valid(exploration_reference_ids_json)
      AND json_type(exploration_reference_ids_json)='array'
      AND json_array_length(exploration_reference_ids_json)<=16);
