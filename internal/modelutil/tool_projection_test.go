package modelutil

import (
	"encoding/json"
	"github.com/wangh00/SciAide/internal/model"
	"strings"
	"testing"
)

func TestToolNameProjectionChangesOnlyHostReferences(t *testing.T) {
	name := "builtin.resource.open"
	alias := "builtin_resource_open"
	schema := json.RawMessage(`{"type":"object","properties":{"literal":{"const":"builtin.resource.open"}}}`)
	menu := `{"facts":{"literal":"builtin.resource.open"},"actions":[{"actionId":"res_a","tool":"builtin.resource.open","label":"builtin.resource.open is a filename"}]}`
	result := `{"status":"success","text":"literal builtin.resource.open in a manual","structured":{"actionId":"res_a","kind":"menu","actions":[{"tool":"builtin.resource.open"}],"data":"builtin.resource.open"}}`
	native := json.RawMessage(`{"type":"thinking","thinking":"builtin.resource.open","signature":"immutable"}`)
	req := model.ChatRequest{Tools: []model.ToolDefinition{{Name: name, Description: "Call builtin.resource.open", InputSchema: schema}}, Messages: []model.Message{
		{Role: model.RoleSystem, Content: "Use builtin.resource.open/search. Not builtin.resource.opening.\n<resource_actions>\n" + menu + "\n</resource_actions>"},
		{Role: model.RoleUser, HostToolReferences: true, Content: "Call builtin.resource.open. <stage_input>{\"literal\":\"builtin.resource.open\"}</stage_input><output_schema>" + string(schema) + "</output_schema>"},
		{Role: model.RoleUser, Content: "My literal builtin.resource.open must stay unchanged"},
		{Role: model.RoleTool, Content: result},
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "old", Name: "builtin.resource.open", Arguments: json.RawMessage(`{"literal":"builtin.resource.open"}`)}}},
	}, ProviderTurns: []model.ProviderTurn{{Items: []model.ProviderItem{{Type: "thinking", Payload: native}}, ToolResults: []model.Message{{Role: model.RoleTool, Content: result}}}}}
	before, _ := json.Marshal(req)
	projected := ProjectToolReferences(req, map[string]string{name: alias, "builtin.resource.search": "builtin_resource_search"})
	after, _ := json.Marshal(req)
	if string(before) != string(after) {
		t.Fatal("changed original audit/request")
	}
	if projected.Tools[0].Description != "Call "+alias || string(projected.Tools[0].InputSchema) != string(schema) {
		t.Fatal("definition projection changed arguments contract")
	}
	host := projected.Messages[0].Content
	if !strings.Contains(host, "Use builtin_resource_open / builtin_resource_search.") || !strings.Contains(host, "builtin.resource.opening") || !strings.Contains(host, `"tool":"builtin_resource_open"`) || !strings.Contains(host, `"label":"builtin.resource.open is a filename"`) || !strings.Contains(host, "<resource_actions>\n") {
		t.Fatal(host)
	}
	if !strings.HasPrefix(projected.Messages[1].Content, "Call builtin_resource_open.") || !strings.Contains(projected.Messages[1].Content, "<output_schema>"+string(schema)+"</output_schema>") || !strings.Contains(projected.Messages[1].Content, `"literal":"builtin.resource.open"`) {
		t.Fatal("host prompt data/schema changed")
	}
	if projected.Messages[2].Content != req.Messages[2].Content {
		t.Fatal("user literal changed")
	}
	if !strings.Contains(projected.Messages[3].Content, `"tool":"builtin_resource_open"`) || !strings.Contains(projected.Messages[3].Content, "literal builtin.resource.open in a manual") {
		t.Fatal("tool evidence changed")
	}
	if projected.Messages[4].ToolCalls[0].Name != name || string(projected.Messages[4].ToolCalls[0].Arguments) != string(req.Messages[4].ToolCalls[0].Arguments) {
		t.Fatal("historical call normalization changed arguments")
	}
	if string(projected.ProviderTurns[0].Items[0].Payload) != string(native) || !strings.Contains(projected.ProviderTurns[0].ToolResults[0].Content, `"tool":"builtin_resource_open"`) {
		t.Fatal("native history corrupted")
	}
}
func TestToolProjectionDoesNotRestoreLegacyNames(t *testing.T) {
	for _, canonical := range []string{"builtin.resource.open", "builtin.resource.search"} {
		alias := ProviderToolName(canonical)
		legacy := strings.Replace(canonical, "resource.", "resource_", 1)
		if got := ResolveProviderToolName(legacy, map[string]string{alias: canonical}); got != legacy {
			t.Fatal(got)
		}
		if ResolveProviderToolName(legacy, nil) != legacy {
			t.Fatal("undeclared tool resolved")
		}
	}
	req := model.ChatRequest{Messages: []model.Message{{Role: model.RoleTool, Content: `{"status":"error","text":"Use builtin.resource.open/search, not builtin.resource_open"}`}}}
	out := ProjectToolReferences(req, map[string]string{"builtin.resource.open": "builtin_resource_open"})
	if !strings.Contains(out.Messages[0].Content, "builtin.resource_open") || strings.Contains(out.Messages[0].Content, "/search") {
		t.Fatal(out.Messages[0].Content)
	}
}

func TestInstructionSchemaPlaceholderDoesNotHideHostToolReferences(t *testing.T) {
	text := `Use properties from <output_schema>. Call builtin.resource.open.
<stage_input>{"literal":"builtin.resource.open"}</stage_input>
Again call builtin.resource.open.
<output_schema>{"type":"object","const":"builtin.resource.open"}</output_schema>`
	req := model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: text, HostToolReferences: true}}}
	got := ProjectToolReferences(req, map[string]string{"builtin.resource.open": "builtin_resource_open"}).Messages[0].Content
	if strings.Count(got, "builtin_resource_open") != 2 || strings.Count(got, "builtin.resource.open") != 2 {
		t.Fatal(got)
	}
}

func TestSubmissionReferenceProjectionDoesNotReopenOldTools(t *testing.T) {
	active := map[string]string{"submit_stage_result": "submit_stage_result"}
	req := model.ChatRequest{ForcedTool: "submit_stage_result", Tools: []model.ToolDefinition{{Name: "submit_stage_result"}}, ToolReferenceNames: []string{"builtin.resource.open"}, Messages: []model.Message{{Role: model.RoleSystem, Content: "Old resource instructions: builtin.resource.open. Only submit_stage_result is callable now."}}}
	projected := ProjectToolReferences(req, ToolReferenceAliases(req, active, ProviderToolName))
	if len(active) != 1 || len(projected.Tools) != 1 || projected.ForcedTool != "submit_stage_result" || strings.Contains(projected.Messages[0].Content, "builtin.resource.open") {
		t.Fatal("reference escaped into active tools or was not projected")
	}
}

func TestLegacyMixedNameDoesNotOverrideAnotherDeclaredTool(t *testing.T) {
	aliases := map[string]string{"builtin_resource_open": "builtin.resource.open", "other_declared_alias": "builtin.resource_open"}
	if got := ResolveProviderToolName("builtin.resource_open", aliases); got != "builtin.resource_open" {
		t.Fatal("legacy compatibility overrode declared tool identity", got)
	}
}

func TestTruncatedResourceHistoryKeepsCompleteActionNamesConsistent(t *testing.T) {
	raw := `{"status":"success","structured":{"actionId":"res_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","actions":[{"actionId":"res_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","label":"section","kind":"skill_section","tool":"builtin.resource.open"}]
...[tool result truncated for model context]...
"text":"quoted \"tool\":\"builtin.resource.open\""}`
	req := model.ChatRequest{Messages: []model.Message{{Role: model.RoleTool, Content: raw}}}
	got := ProjectToolReferences(req, map[string]string{"builtin.resource.open": "builtin_resource_open"}).Messages[0].Content
	if !strings.Contains(got, `"tool":"builtin_resource_open"`) || !strings.Contains(got, `\"tool\":\"builtin.resource.open\"`) {
		t.Fatal(got)
	}
	if req.Messages[0].Content != raw {
		t.Fatal("persisted truncated evidence changed")
	}
}
