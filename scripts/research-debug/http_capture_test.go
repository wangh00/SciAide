package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/wangh00/SciAide/internal/model/responses"
)

func TestHTTPResponseCaptureSeparatePrivateFileAndReopenCap(t *testing.T) {
	root := t.TempDir()
	c, err := openHTTPResponseCapture(root)
	if err != nil {
		t.Fatal(err)
	}
	c.recordHTTPResponse(responses.DebugHTTPResponse{HTTPStatus: 429, Headers: map[string]string{"Retry-After": "120", "X-Request-Id": "req-fixture"}})
	prior := c.bytes
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	c, err = openHTTPResponseCapture(root)
	if err != nil || c.bytes != prior {
		t.Fatal("capture restart lost size", err)
	}
	c.bytes = 1 << 20
	c.recordHTTPResponse(responses.DebugHTTPResponse{HTTPStatus: 502})
	_ = c.Close()
	data, err := os.ReadFile(filepath.Join(root, "http-response-capture.jsonl"))
	var v map[string]any
	if err != nil || int64(len(data)) != prior || json.Unmarshal(data, &v) != nil || v["httpStatus"] != float64(429) || v["protocol"] != "openai_responses" {
		t.Fatal("HTTP file was corrupt, not separate, or unbounded", err)
	}
	for _, name := range []string{"provider-error-capture.jsonl", "stream-fault-capture.jsonl"} {
		if _, err = os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatal("other capture enabled implicitly")
		}
	}
}
