package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/wangh00/SciAide/internal/model/responses"
)

func TestErrorCaptureSerializesAndBoundsLocalRecords(t *testing.T) {
	root := t.TempDir()
	c, err := openErrorCapture(root)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			c.record(responses.DebugProviderError{Kind: "fixture", Code: "fixture", Message: "already-redacted"})
		}()
	}
	group.Wait()
	prior := c.bytes
	c.bytes = 1 << 20
	c.record(responses.DebugProviderError{Message: "must-not-append"})
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(root, "provider-error-capture.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, _ := f.Stat()
	if info.Size() != prior {
		t.Fatal("capture exceeded bound")
	}
	decoder := json.NewDecoder(f)
	for i := 0; i < 8; i++ {
		var entry map[string]any
		if decoder.Decode(&entry) != nil || entry["protocol"] != "openai_responses" || entry["message"] != "already-redacted" {
			t.Fatal("concurrent record corruption")
		}
	}
}

func TestStreamFaultCaptureHasSeparateBoundedPrivateFile(t *testing.T) {
	root := t.TempDir()
	c, err := openStreamFaultCapture(root)
	if err != nil {
		t.Fatal(err)
	}
	c.recordStreamFault(responses.DebugStreamFault{DataBase64: "e30=", DataBytes: 2})
	prior := c.bytes
	c.bytes = 1 << 20
	c.recordStreamFault(responses.DebugStreamFault{DataBase64: "must-not-append"})
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "stream-fault-capture.jsonl"))
	var v map[string]any
	if err != nil || int64(len(data)) != prior || json.Unmarshal(data, &v) != nil || v["dataBase64"] != "e30=" || v["protocol"] != "openai_responses" {
		t.Fatal("stream fault file is unbounded or corrupt", err)
	}
	if _, err := os.Stat(filepath.Join(root, "provider-error-capture.jsonl")); !os.IsNotExist(err) {
		t.Fatal("fault evidence went to the error-text file")
	}
}
