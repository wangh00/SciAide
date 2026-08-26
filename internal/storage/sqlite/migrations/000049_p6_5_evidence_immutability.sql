CREATE TRIGGER research_evidence_model_starts_pending
BEFORE INSERT ON research_evidence_entries
WHEN NEW.provenance = 'model' AND NEW.review_status <> 'pending'
BEGIN
    SELECT RAISE(ABORT, 'model-derived evidence must start pending');
END;

CREATE TRIGGER research_evidence_content_immutable
BEFORE UPDATE OF
    id,project_id,bibliography_id,field_kind,content,provenance,evidence_level,
    attachment_id_snapshot,knowledge_document_id_snapshot,index_version_id_snapshot,
    chunk_id_snapshot,source_name_snapshot,locator_snapshot,quote_text_snapshot,
    quote_sha256,source_start,source_end,created_at
ON research_evidence_entries
BEGIN
    SELECT RAISE(ABORT, 'research evidence content and snapshots are immutable');
END;

CREATE TRIGGER research_evidence_attachment_link_immutable
BEFORE UPDATE OF attachment_id ON research_evidence_entries
WHEN OLD.attachment_id IS NULL OR NEW.attachment_id IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'research evidence live source links cannot be reassigned');
END;

CREATE TRIGGER research_evidence_document_link_immutable
BEFORE UPDATE OF knowledge_document_id ON research_evidence_entries
WHEN OLD.knowledge_document_id IS NULL OR NEW.knowledge_document_id IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'research evidence live source links cannot be reassigned');
END;
