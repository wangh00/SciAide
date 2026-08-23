-- Document tools must never parse images. Repair image rows that older builds
-- incorrectly marked failed after a model called builtin.document.inspect.
UPDATE attachments
SET status='ready',
    unit_count=0,
    extracted_runes=0,
    truncated=0,
    error_message='',
    updated_at=strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE document_format='image'
  AND status='failed'
  AND error_message LIKE 'unsupported document format "image"%';
