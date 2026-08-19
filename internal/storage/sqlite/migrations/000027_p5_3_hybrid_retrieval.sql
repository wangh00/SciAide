CREATE TABLE knowledge_embedding_config (
    id INTEGER PRIMARY KEY NOT NULL CHECK (id = 1),
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0,1)),
    base_url TEXT NOT NULL DEFAULT '',
    model_id TEXT NOT NULL DEFAULT '',
    dimensions INTEGER NOT NULL DEFAULT 0 CHECK (dimensions >= 0),
    config_fingerprint TEXT NOT NULL DEFAULT '',
    secret_ref TEXT NOT NULL DEFAULT 'sciaide/embedding/default',
    timeout_seconds INTEGER NOT NULL DEFAULT 30 CHECK (timeout_seconds BETWEEN 5 AND 300),
    last_tested_at TEXT,
    updated_at TEXT NOT NULL
);

INSERT INTO knowledge_embedding_config(id,updated_at)
VALUES (1,strftime('%Y-%m-%dT%H:%M:%fZ','now'));

ALTER TABLE knowledge_index_versions ADD COLUMN embedding_model TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_index_versions ADD COLUMN embedding_dimensions INTEGER NOT NULL DEFAULT 0;
ALTER TABLE knowledge_index_versions ADD COLUMN embedding_config_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_index_versions ADD COLUMN hybrid_strategy TEXT NOT NULL DEFAULT 'bm25_only_v1';
