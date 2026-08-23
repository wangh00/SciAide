ALTER TABLE model_request_usage ADD COLUMN profile_name TEXT NOT NULL DEFAULT '';

UPDATE model_request_usage
SET profile_name = COALESCE((
    SELECT name FROM model_profiles WHERE model_profiles.id = model_request_usage.model_profile_id
), '')
WHERE profile_name = '';
