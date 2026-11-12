-- Private full baselines: events and model/timeline projections carry only
-- a small bound receipt. No backfill or retroactive adoption of old results.
CREATE TABLE workflow_revision_write_snapshots (
    event_id TEXT PRIMARY KEY,
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    snapshot_sha256 TEXT NOT NULL,
    snapshot_json TEXT NOT NULL CHECK(json_valid(snapshot_json))
);
CREATE INDEX idx_workflow_revision_write_snapshot_run ON workflow_revision_write_snapshots(workflow_run_id);
CREATE TRIGGER workflow_revision_write_snapshot_immutable BEFORE UPDATE ON workflow_revision_write_snapshots
BEGIN SELECT RAISE(ABORT,'Revision write snapshots are immutable'); END;
