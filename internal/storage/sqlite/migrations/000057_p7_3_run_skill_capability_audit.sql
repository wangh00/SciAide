ALTER TABLE run_dynamic_skills
ADD COLUMN capability_audit_json TEXT NOT NULL DEFAULT '{}'
CHECK (json_valid(capability_audit_json) AND json_type(capability_audit_json) = 'object');
