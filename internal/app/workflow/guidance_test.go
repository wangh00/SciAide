package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBoundedGuidanceJSONEnforcesRemainingBudget(t *testing.T) {
	raw := json.RawMessage(`{"text":"` + strings.Repeat("x", 200) + `"}`)
	value, used := boundedGuidanceJSON(raw, 32)
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if used != 32 || !strings.Contains(string(encoded), `"truncated":true`) || !strings.Contains(string(encoded), `"bytes":211`) {
		t.Fatalf("bounded value = %s, used = %d", encoded, used)
	}
	value, used = boundedGuidanceJSON(raw, 0)
	encoded, _ = json.Marshal(value)
	if used != 0 || !strings.Contains(string(encoded), `"preview":""`) {
		t.Fatalf("exhausted value = %s, used = %d", encoded, used)
	}
}

func TestResearchGuidanceStateHasHardSerializedBudget(t *testing.T) {
	stages := make([]map[string]any, 128)
	for index := range stages {
		stages[index] = map[string]any{
			"name":   strings.Repeat("研究阶段", 2_000),
			"kind":   "python",
			"output": map[string]any{"escaped": strings.Repeat("\\\"\n", 8_000)},
		}
	}
	state := map[string]any{
		"workflowRunId":   strings.Repeat("run", 8_000),
		"workflowStatus":  "running",
		"permissionMode":  "full_access",
		"stageName":       strings.Repeat("分析", 8_000),
		"stageKind":       "python",
		"stageStatus":     "running",
		"inputs":          map[string]any{"escaped": strings.Repeat("\\\"\n", 8_000)},
		"completedStages": stages,
		"errorCode":       "",
		"errorMessage":    strings.Repeat("错误", 8_000),
	}

	encoded, err := marshalResearchGuidanceState(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxResearchGuidanceTotalBytes || !json.Valid(encoded) {
		t.Fatalf("guidance bytes = %d, valid = %v", len(encoded), json.Valid(encoded))
	}
	if !strings.Contains(string(encoded), `"guidanceTruncated":true`) || !strings.Contains(string(encoded), `"count":128`) {
		t.Fatalf("guidance did not preserve truncation metadata: %s", encoded)
	}
}

func TestResearchSystemContextStartsWithTrustedResearchModeRules(t *testing.T) {
	context := researchSystemContext("<workflow_state>{}</workflow_state>")
	if !strings.HasPrefix(context, ResearchModeSystemRules) || !strings.Contains(context, "independent review") || !strings.Contains(context, "<workflow_state>") {
		t.Fatalf("research system context = %q", context)
	}
}

func TestWithoutSkillReadToolsRemovesEverySkillContentReader(t *testing.T) {
	values := withoutSkillReadTools([]string{
		"builtin.workspace.read_text",
		"builtin.skill.load",
		"builtin.skill.resource.list",
		"builtin.skill.resource.read_text",
	})
	if len(values) != 1 || values[0] != "builtin.workspace.read_text" {
		t.Fatalf("filtered tools = %v", values)
	}
}

func TestWorkflowRouteContextPresenceDistinguishesDiscoveryFromFrozenEmptyScope(t *testing.T) {
	for _, value := range []json.RawMessage{
		json.RawMessage(`{"routeContext":{"selectedSkills":[]}}`),
		json.RawMessage(`{"route_context":{"selectedSkills":[]}}`),
	} {
		if !hasWorkflowRouteContext(value) {
			t.Fatalf("frozen route context was not detected: %s", value)
		}
	}
	for _, value := range []json.RawMessage{nil, json.RawMessage(`{}`), json.RawMessage(`{"context":{}}`)} {
		if hasWorkflowRouteContext(value) {
			t.Fatalf("ordinary Workflow input was treated as a frozen route: %s", value)
		}
	}
}

func TestExecutionGuidanceDoesNotDuplicateStageInputsOrSchema(t *testing.T) {
	state := map[string]any{
		"workflowRunId": "run", "stageName": "implementation", "stageStatus": "running", "stageCount": 11,
		"inputs":          map[string]any{"code": strings.Repeat("original-code", 5000)},
		"completedStages": []any{map[string]string{"output": "complete-prior-output"}},
		"aiOutputSchema":  map[string]string{"type": "object"}, "structuredOutputRequired": true,
	}
	system, dynamic, err := executionResearchGuidance(state)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(system, ResearchModeSystemRules) || !strings.Contains(dynamic, `"stageCount":11`) || !strings.Contains(dynamic, `"structuredOutputRequired":true`) {
		t.Fatal("lost lifecycle or research rules")
	}
	if strings.Contains(system+dynamic, "original-code") || strings.Contains(dynamic, "complete-prior-output") || strings.Contains(dynamic, "aiOutputSchema") || len(dynamic) > 1000 {
		t.Fatal("duplicated frozen stage content in execution guidance")
	}
	if state["aiOutputSchema"] == nil || state["inputs"] == nil {
		t.Fatal("discussion state or audit mutated")
	}
}
