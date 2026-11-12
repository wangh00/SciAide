package tool

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestModelContextEnvelopePreviewIsJSONAndDoesNotInventContinuation(t *testing.T) {
	structured, _ := json.Marshal(map[string]any{"kind": "workspace_read", "actionId": "res-offered-current", "text": strings.Repeat("文\n\"🧪", 5000), "nextOffset": 5000, "actions": []any{map[string]any{"actionId": "res-offered-next"}}})
	result := Result{Status: ResultSuccess, Text: "HEAD " + strings.Repeat("text ", 6000) + " TAIL", Structured: structured}
	before := string(result.Structured)
	snapshot := BuildModelContextSnapshot("call", "", result)
	if !json.Valid([]byte(snapshot)) || utf8.RuneCountInString(snapshot) > MaxModelContextRunes {
		t.Fatal("invalid oversized frozen snapshot")
	}
	var value map[string]any
	_ = json.Unmarshal([]byte(snapshot), &value)
	if value["structuredNotShown"] != true || value["rereadActionId"] != "res-offered-current" || value["structured"] != nil || value["nextOffset"] != nil {
		t.Fatal("partial evidence/continuation misrepresented")
	}
	if string(result.Structured) != before {
		t.Fatal("durable result changed")
	}
	for _, limit := range []int{0, 1, 2, 20, 100, 256, 1000} {
		preview, ok := FitModelContextPreview(snapshot, limit)
		if !ok || utf8.RuneCountInString(preview) > limit || (preview != "" && !json.Valid([]byte(preview))) {
			t.Fatal("invalid nested JSON projection", limit)
		}
	}
}

func TestModelContextFitsUnchangedAndWrapsOnlyMarkedLegacyDamage(t *testing.T) {
	short := `{"status":"success","structured":{"kind":"workspace_read","actionId":"res-a"},"text":"exact"}`
	if preview, ok := FitModelContextPreview(short, 1000); !ok || preview != short || !IsWorkspaceReadContext(preview) {
		t.Fatal("fitting snapshot rewritten")
	}
	broken := `{"status":"success","text":"HEAD` + contextPreviewMarker + `TAIL"}`
	preview, ok := FitModelContextPreview(broken, 2000)
	if !ok || !json.Valid([]byte(preview)) || !strings.Contains(preview, "contextPreviewTruncated") || IsWorkspaceReadContext(preview) {
		t.Fatal("legacy damage guessed into valid structured facts")
	}
	if _, ok := FitModelContextPreview("ordinary plain text", 50); ok {
		t.Fatal("plain text reclassified")
	}
}

func TestSkillReadByteCountsAreNotPresentedForHiddenText(t *testing.T) {
	result := Result{Status: ResultSuccess, Text: strings.Repeat("skill content ", 3000), Structured: json.RawMessage(`{"name":"skill","path":"references/method.md","bytesRead":6000,"originalBytes":6000,"truncated":false}`)}
	snapshot := BuildModelContextSnapshot("skill-call", "", result)
	var value map[string]json.RawMessage
	if json.Unmarshal([]byte(snapshot), &value) != nil || value["structured"] != nil || string(value["structuredNotShown"]) != "true" {
		t.Fatal("hidden text retained complete-read counters")
	}
}
