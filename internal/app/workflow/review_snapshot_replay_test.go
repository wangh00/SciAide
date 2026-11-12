package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf8"
)

// Replay only the task-scoped captured files, never the live database/model.
func TestCapturedReviewSnapshotPromptSize(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REVIEW_SNAPSHOT_REPLAY")
	if dir == "" {
		t.Skip("no capture supplied")
	}
	read := func(name string) map[string]json.RawMessage {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]json.RawMessage
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	r, s := read("run.json"), read("step.json")
	str := func(v json.RawMessage) string {
		var text string
		if err := json.Unmarshal(v, &text); err != nil {
			t.Fatal(err)
		}
		return text
	}
	var compilation Compilation
	if err := json.Unmarshal([]byte(str(r["compilation_json"])), &compilation); err != nil {
		t.Fatal(err)
	}
	detail := RunDetail{Run: Run{Compilation: compilation, Inputs: json.RawMessage(str(r["inputs_json"]))}}
	step := Step{ID: str(s["id"]), NodeID: str(s["node_id"]), Input: json.RawMessage(str(s["input_json"])), InputSHA256: str(s["input_sha256"])}
	node := compilationNodeMap(compilation)[step.NodeID]
	prompt := buildAIStagePrompt(detail, step, node)
	t.Logf("input bytes=%d, prompt bytes=%d, prompt chars=%d", len(step.Input), len(prompt), utf8.RuneCountInString(prompt))
	if len(prompt) <= 262144 || utf8.RuneCountInString(prompt) > 262144 {
		t.Fatal("capture did not reproduce bytes/characters mismatch")
	}
	if err := os.WriteFile(filepath.Join(dir, "replayed-prompt.txt"), []byte(prompt), 0600); err != nil {
		t.Fatal(err)
	}
}
