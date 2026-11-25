package main

import "github.com/wangh00/SciAide/internal/modelutil"

func openRequestSnapshotCapture(root string) (*errorCapture, error) {
	return openCaptureFile(root, "http-request-snapshots.jsonl")
}

func (c *errorCapture) recordRequestSnapshot(event modelutil.DebugRequestSnapshot) {
	c.recordJSON(event)
}
