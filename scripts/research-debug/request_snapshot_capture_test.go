package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/wangh00/SciAide/internal/modelutil"
)

func TestRequestSnapshotCaptureIsSeparateBoundedAndReopens(t *testing.T) {
	root := t.TempDir()
	capture, err := openRequestSnapshotCapture(root)
	if err != nil {
		t.Fatal(err)
	}
	capture.recordRequestSnapshot(modelutil.DebugRequestSnapshot{Version: 1, Session: "first", Sequence: 1, Inspection: "complete"})
	prior := capture.bytes
	if err = capture.Close(); err != nil {
		t.Fatal(err)
	}
	capture, err = openRequestSnapshotCapture(root)
	if err != nil || capture.bytes != prior {
		t.Fatal("capture size not preserved", err)
	}
	capture.bytes = 1 << 20
	capture.recordRequestSnapshot(modelutil.DebugRequestSnapshot{Session: "second", Sequence: 2})
	_ = capture.Close()
	data, err := os.ReadFile(filepath.Join(root, "http-request-snapshots.jsonl"))
	var value modelutil.DebugRequestSnapshot
	if err != nil || int64(len(data)) != prior || json.Unmarshal(data, &value) != nil || value.Session != "first" {
		t.Fatal("capture overwritten, malformed, or cap ignored", err)
	}
	for _, name := range []string{"provider-error-capture.jsonl", "http-response-capture.jsonl", "stream-fault-capture.jsonl"} {
		if _, err = os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatal("other capture enabled")
		}
	}
}

func TestRequestSnapshotRunnerFlagDefaultOffWithoutModelCalls(t *testing.T) {
	priorFlags, priorArgs := flag.CommandLine, os.Args
	defer func() { flag.CommandLine, os.Args = priorFlags, priorArgs }()
	for _, enabled := range []bool{false, true} {
		root := t.TempDir()
		flag.CommandLine = flag.NewFlagSet("research-debug", flag.ContinueOnError)
		os.Args = []string{"research-debug", "--root", root, "--max-duration", "100ms"}
		if enabled {
			os.Args = append(os.Args, "--capture-request-snapshots")
		}
		if err := run(); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(root, "http-request-snapshots.jsonl"))
		if enabled && (err != nil || info.Size() != 0) || !enabled && !os.IsNotExist(err) {
			t.Fatal("flag/default did not control separate empty capture", enabled, err)
		}
		budget, err := os.ReadFile(filepath.Join(root, "model-budget.json"))
		var value struct {
			Used    int
			Enabled bool
		}
		// A never-used, disabled budget is not necessarily materialized on disk.
		if err != nil && !os.IsNotExist(err) || err == nil && (json.Unmarshal(budget, &value) != nil || value.Used != 0 || value.Enabled) {
			t.Fatal("runner made or enabled a model call", err)
		}
		if _, err = os.Stat(filepath.Join(root, "debug-connection.json")); !os.IsNotExist(err) {
			t.Fatal("runner failed to close")
		}
	}
}
