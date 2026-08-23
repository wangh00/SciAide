ALTER TABLE attachments
    ADD COLUMN parse_metadata_json TEXT NOT NULL DEFAULT '{}'
    CHECK (json_valid(parse_metadata_json) AND json_type(parse_metadata_json) = 'object');
