package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestCompleteRootSubmissionKeepsItsOwnSchemaError(t *testing.T) {
	schema := raw(`{"type":"object","required":["summary","items"],"properties":{"summary":{"type":"string"},"items":{"type":"array","items":{"type":"object","properties":{"quote":{"type":"string","maxLength":3}}}}}}`)
	for _, text := range []string{`{"summary":"present","items":[{"quote":"too long"}]}`, `{"summary":"present","items":[{"quote":"too long"},{"quote":"ok"}]}`} {
		direct := (tool.JSONSchemaValidator{}).Validate(schema, json.RawMessage(text))
		_, err := extractStageJSON(text, schema)
		if direct == nil || err == nil || !strings.Contains(err.Error(), direct.Error()) || strings.Contains(err.Error(), "summary: required") {
			t.Fatal("root field diagnostic replaced by nested recovery", direct, err)
		}
	}
}

func TestCompleteRootJSONTreatsMarkdownFencesInsideStringsAsData(t *testing.T) {
	schema := raw(`{"type":"object","required":["summary"],"properties":{"summary":{"type":"string"}}}`)
	text := `{"summary":"Example only: ` + "```json\\n{\\\"example\\\":true}\\n```" + `"}`
	if !json.Valid([]byte(text)) {
		t.Fatal("invalid fixture")
	}
	got, err := extractStageJSON(text, schema)
	if err != nil || string(got) != text {
		t.Fatal("string fence replaced root submission", string(got), err)
	}
}

// Optional read-only replay of a developer-owned incident; no real model or
// source text is checked into fixtures. The parser must match direct Schema
// diagnostics, not missing fields from some nested fragment.
func TestCapturedRootSubmissionErrorReplay(t *testing.T) {
	root := os.Getenv("SCIAIDE_ROOT_SUBMISSION_REPLAY")
	if root == "" {
		t.Skip("no local incident capture selected")
	}
	text, err := os.ReadFile(filepath.Join(root, "submission.json"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile(filepath.Join(root, "schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	direct := (tool.JSONSchemaValidator{}).Validate(schema, text)
	_, _, actual := NormalizeAIStageSubmission(string(text), schema, "evidence_screening")
	if direct == nil || actual == nil || !strings.Contains(actual.Error(), direct.Error()) {
		t.Fatal("captured root diagnostic mismatch", direct, actual)
	}
	t.Log("read-only captured root diagnostic:", direct)
}
