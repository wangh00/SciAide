ALTER TABLE knowledge_index_versions
    ADD COLUMN retrieval_engine TEXT NOT NULL DEFAULT 'lexical_scan_v1'
    CHECK (retrieval_engine IN ('lexical_scan_v1','fts5_bm25_v1'));
