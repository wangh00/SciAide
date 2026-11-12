package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestResourcePageHistoryKeepsCurrentPageAndValidPreviews(t *testing.T) {
	structured, _ := json.Marshal(map[string]any{"kind": "workspace_read", "actionId": "res-current", "label": "results.json", "text": strings.Repeat("sample,", 650), "truncated": true, "actions": []any{map[string]any{"actionId": "res-next", "kind": "workspace_read", "label": "continue results.json"}}})
	page := tool.BuildModelContextSnapshot("call", "", tool.Result{Status: tool.ResultSuccess, Text: strings.Repeat("sample,", 650), Structured: structured})
	if !json.Valid([]byte(page)) {
		t.Fatal("fresh page context invalid")
	}
	before := []string{page, page, page, page}
	fitted := fitToolContexts(before, 20000)
	if fitted[3] != page {
		t.Fatal("newly read exact page was obscured by old previews")
	}
	for i, value := range fitted {
		if !json.Valid([]byte(value)) {
			t.Fatal("raw JSON tool string cut in half", i)
		}
		if value != page && !strings.Contains(value, "contextPreviewTruncated") {
			t.Fatal("omitted structured evidence not disclosed")
		}
		if before[i] != page {
			t.Fatal("durable page changed")
		}
	}
}

func TestUnfittableNewestWorkspacePageDoesNotHideFittingTaskPage(t *testing.T) {
	page := map[string]any{"status": "success", "text": "page", "structured": map[string]any{"section": "stage_input", "offset": 0, "nextOffset": 4500, "content": strings.Repeat("x", 4500), "sha256": strings.Repeat("a", 64)}}
	task, _ := json.Marshal(page)
	workspace, _ := json.Marshal(map[string]any{"status": "success", "text": strings.Repeat("y", 8500), "structured": map[string]any{"kind": "workspace_read", "actionId": "res-current"}})
	fitted := fitToolContexts([]string{string(task), string(workspace)}, 7000)
	if fitted[0] != string(task) {
		t.Fatal("newer oversized workspace page displaced readable task evidence")
	}
	for _, budget := range []int{1, 2, 4, 6, 23, 43} {
		if _, err := fitQuestionContexts([]string{string(workspace), string(workspace)}, []bool{false, false}, budget); err == nil {
			t.Fatal("budget too small for an omission marker was silently emptied", budget)
		}
	}
}

func TestMarkedLegacySnapshotStaysJSONAcrossBothBudgetLayers(t *testing.T) {
	broken := `{"status":"success","text":"HEAD` + "\n...[tool result truncated for model context]...\n" + strings.Repeat("legacy ", 800) + `TAIL"}`
	call := tool.Call{ModelContextVersion: tool.ModelContextSnapshotVersion, ModelContext: broken, Result: &tool.Result{Status: tool.ResultSuccess, Text: "Complete durable original"}}
	first := toolCallContext(call)
	second := fitToolContexts([]string{first}, 256)[0]
	if !json.Valid([]byte(first)) || !json.Valid([]byte(second)) || len([]rune(second)) > 256 || !strings.Contains(second, "contextPreviewTruncated") {
		t.Fatal("legacy snapshot broke again in shared budgeting")
	}
	if call.ModelContext != broken || call.Result.Text != "Complete durable original" {
		t.Fatal("old snapshot or original result rewritten")
	}
}
