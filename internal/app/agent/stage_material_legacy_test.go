package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/model"
)

func materialProgressJSON(t *testing.T, request model.ChatRequest) map[string]any {
	t.Helper()
	for _, m := range request.Messages {
		if strings.HasPrefix(m.Content, stageMaterialProgressPrefix) {
			var data map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(m.Content, stageMaterialProgressPrefix)), &data); err != nil {
				t.Fatal(err)
			}
			return data
		}
	}
	t.Fatal("missing material reading metadata")
	return nil
}

func TestStageMaterialLegacyNavigationUsesVerifiedGlobalCoordinates(t *testing.T) {
	input, _ := json.Marshal(map[string]any{"originalRequest": "保留要求", "context": map[string]string{"markdown": strings.Repeat("中🙂\n\"\\", 4000)}})
	prompt := "fixed rules\n<stage_input>\n" + string(input) + "\n</stage_input>"
	projected, ok := workflow.ProjectResearchStagePrompt(prompt, input, 4000)
	if !ok {
		t.Fatal("fixture not projected")
	}
	c := stageBudgetContract(input, prompt)
	c.ID, c.NodeID, c.WorkflowStepID = "e", "draft", "step"
	g, raw := newStageMaterialLedger(c, "run"), newStageReadConvergence(c, "run")
	material, err := workflow.ResolveResearchStageMaterial(input, "/context/markdown")
	if err != nil {
		t.Fatal(err)
	}
	request := model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, HostToolReferences: true, Content: projected}}, Tools: []model.ToolDefinition{{Name: workflow.ResearchTaskReadTool, InputSchema: json.RawMessage(`{"type":"object","properties":{"section":{"type":"string"},"offset":{"type":"integer"}}}`)}}}
	start := material.OriginalJSONOffset
	first := materialProgressJSON(t, g.withProgress(request, nil, nil, 40000, raw))
	record := first["records"].([]any)[0].(map[string]any)
	if first["addressMode"] != "legacy_whole_json" || first["scientificCompletionAssessment"] != "not_assessed" || int(record["nextGlobalOffset"].(float64)) != start || !strings.Contains(first["coordinateSystem"].(string), "never pointer") {
		t.Fatal("legacy cursor became value-relative or granted pointer", first)
	}
	// Even escaped text is measured in original JSON Unicode coordinates.
	raw.seen = []readSpan{{start, start + 500}}
	args, _ := json.Marshal(map[string]any{"section": "stage_input", "offset": start + 500})
	end := start + 900
	data, _ := json.Marshal(map[string]any{"section": "stage_input", "nodeId": c.NodeID, "sha256": c.InputSHA256, "offset": start + 500, "nextOffset": end, "totalCharacters": len([]rune(string(input))), "content": string([]rune(string(input))[start+500 : end])})
	call := tool.Call{ID: "page", ProviderCallID: "page", RunID: "run", ToolName: workflow.ResearchTaskReadTool, Arguments: args, Result: &tool.Result{Status: tool.ResultSuccess, Structured: data}}
	request.Messages = append(request.Messages, model.Message{Role: model.RoleTool, ToolCallID: "page", Content: toolCallContext(call)})
	next := materialProgressJSON(t, g.withProgress(request, []tool.Call{call}, nil, 40000, raw))["records"].([]any)[0].(map[string]any)
	if int(next["nextGlobalOffset"].(float64)) != end || int(next["previouslyExposedRanges"].([]any)[0].([]any)[0].(float64)) != start || int(next["currentlyVisibleRanges"].([]any)[0].([]any)[1].(float64)) != end {
		t.Fatal("raw previous/current receipts or global cursor were mixed", next)
	}
	for _, mode := range []string{"hash", "coordinate", "untrusted"} {
		bad := request
		bad.Messages = cloneModelMessages(request.Messages[:1])
		switch mode {
		case "hash":
			bad.Messages[0].Content = strings.ReplaceAll(projected, c.InputSHA256, strings.Repeat("0", 64))
		case "coordinate":
			bad.Messages[0].Content = strings.ReplaceAll(projected, fmt.Sprintf(`"offset":%d`, start), fmt.Sprintf(`"offset":%d`, start+1))
		case "untrusted":
			bad.Messages[0].HostToolReferences = false
		}
		if got := g.withProgress(bad, nil, nil, 40000, raw); len(got.Messages) != len(bad.Messages) {
			t.Fatal("unverified navigation was trusted", mode)
		}
	}
}

func TestStageMaterialLegacyDiscoveryIsExplicitlyPartial(t *testing.T) {
	values := map[string]string{}
	for i := 0; i < 100; i++ {
		values[fmt.Sprintf("v%03d", i)] = "source"
	}
	input, _ := json.Marshal(values)
	c := stageBudgetContract(input, "host")
	c.ID, c.NodeID, c.WorkflowStepID = "e", "draft", "step"
	g := newStageMaterialLedger(c, "run")
	links := []any{}
	for i := 0; i < 100; i++ {
		pointer := fmt.Sprintf("/v%03d", i)
		m, _ := workflow.ResolveResearchStageMaterial(input, pointer)
		links = append(links, map[string]any{"$read": map[string]any{"section": "stage_input", "pointer": pointer, "sha256": c.InputSHA256, "offset": m.OriginalJSONOffset, "endOffset": m.OriginalJSONEndOffset}})
	}
	navigation, _ := json.Marshal(links)
	request := model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, HostToolReferences: true, Content: "<stage_input>\n" + string(navigation) + "\n</stage_input>"}}}
	state := materialProgressJSON(t, g.withProgress(request, nil, nil, 80000, newStageReadConvergence(c, "run")))
	if state["discoveryTruncated"] != true || state["additionalRecordsOmitted"] != nil || int(state["additionalVerifiedRecordsOmitted"].(float64)) != 48 {
		t.Fatal("bounded discovery was advertised as a complete inventory", state)
	}
}

func TestStageMaterialHiddenPreviewDoesNotInventAnAmbiguousCursor(t *testing.T) {
	_, _, page := materialLedgerFixture(t)
	_, m := page("page", "/A", 3)
	for _, limit := range []int{400, 40} {
		preview, ok := fitTaskPagePreview(m.Content, limit)
		var data map[string]any
		if !ok || json.Unmarshal([]byte(preview), &data) != nil || data["contentNotShown"] != true {
			t.Fatal("invalid hidden preview", limit, preview)
		}
		if limit == 400 && (data["pointer"] != "/A" || data["addressMode"] != "json_pointer" || data["retryOffset"] != float64(3) || data["valueSha256"] == nil) || limit == 40 && data["retryOffset"] != nil {
			t.Fatal("hidden page lost identity or gave an ambiguous cursor", preview)
		}
	}
}
