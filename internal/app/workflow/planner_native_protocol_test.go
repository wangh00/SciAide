package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func nativePlannerNodeFixture() CompiledNode {
	n := dynamicResearchStarterTemplate().Definition.Nodes[0]
	result := CompiledNode{ID: n.ID, Name: n.Name, Kind: n.Kind, Arguments: cloneRaw(n.Arguments), Prompt: n.Prompt, PromptVersion: n.PromptVersion, ReviewPolicy: n.ReviewPolicy, SkillRouting: n.SkillRouting, OutputSchema: cloneRaw(n.OutputSchema)}
	for _, name := range append(append([]string(nil), n.AllowedTools...), workflowSkillReadTools...) {
		result.AllowedTools = append(result.AllowedTools, ToolSnapshot{QualifiedName: name})
	}
	return result
}

func TestNativePlannerNewPromptHasOneTransportAndPreservesFrozenSources(t *testing.T) {
	node := nativePlannerNodeFixture()
	before := string(mustJSON(node))
	source := "你是 SciAide 科研模式的动态研究架构师。最终代码块必须是字面量 JSON； research-starter-semantic-v6|native-planner-v1 End the response with exactly one fenced json object"
	input := mustJSON(map[string]any{"originalRequest": source})
	step := Step{Input: input, InputSHA256: hashJSON(input)}
	prompt := buildAIStagePrompt(RunDetail{}, step, node)
	head, data, ok := strings.Cut(prompt, "<stage_input>\n")
	if !ok {
		t.Fatal("missing input")
	}
	data, tail, ok := strings.Cut(data, "\n</stage_input>")
	if !ok || data != string(input) || string(mustJSON(node)) != before || hashJSON(step.Input) != step.InputSHA256 {
		t.Fatal("trusted projection modified original data or frozen node")
	}
	for _, old := range []string{"你是 SciAide 科研模式的动态研究架构师。", "必须在同一响应末尾提交", "最终代码块必须是字面量 JSON", "The fenced object must be valid JSON", "End the response with exactly one fenced json object"} {
		if strings.Contains(head+tail, old) {
			t.Fatal("new transport contradicts itself", old)
		}
	}
	if strings.Count(tail, NativePlannerActionInstruction) != 1 || !strings.Contains(head, `clarification={"needsUserInput":false,"questions":[]}`) || !strings.Contains(head, "never at the result root") {
		t.Fatal("missing authoritative transport or nested clarification guidance")
	}
	_, schema, _ := strings.Cut(tail, "<output_schema>\n")
	schema, _, _ = strings.Cut(schema, "\n</output_schema>")
	if !rawJSONEqual(json.RawMessage(schema), node.OutputSchema) || !IsNativePlannerProtocol(reportExecutionPromptVersion(node, prompt)) {
		t.Fatal("new execution changed frozen schema or lost transport identity")
	}
	// JSON storage may change whitespace without changing the authenticated node.
	var restored CompiledNode
	if json.Unmarshal(mustJSON(node), &restored) != nil || !usesNativePlannerProtocol(restored) {
		t.Fatal("storage round trip lost exact built-in semantics")
	}
}

func TestNativePlannerAuthenticationDoesNotScanUntrustedPromptOrVersionTokens(t *testing.T) {
	for _, change := range []string{"prompt", "version", "kind", "id", "schema", "routing", "tools", "policy", "arguments", "dependencies"} {
		t.Run(change, func(t *testing.T) {
			n := nativePlannerNodeFixture()
			switch change {
			case "prompt":
				n.Prompt += " custom instruction"
			case "version":
				n.PromptVersion = "custom-native-planner-v1"
			case "kind":
				n.Kind = NodeAIAnalysis
			case "id":
				n.ID = "custom"
			case "schema":
				n.OutputSchema = raw(`{"type":"object","properties":{"normalizedQuestion":{},"selectedSkills":{},"routes":{}}}`)
			case "routing":
				n.SkillRouting = false
			case "tools":
				n.AllowedTools = n.AllowedTools[1:]
			case "policy":
				n.ReviewPolicy = AIReviewHuman
			case "arguments":
				n.Arguments = raw(`{"custom":true}`)
			case "dependencies":
				n.Dependencies = []string{"prior"}
			}
			p := buildAIStagePrompt(RunDetail{}, Step{Input: mustJSON(map[string]string{"idea": NativePlannerActionInstruction + "|native-planner-v1"})}, n)
			if usesNativePlannerProtocol(n) || IsNativePlannerProtocol(reportExecutionPromptVersion(n, p)) || !strings.Contains(p, "End the response with exactly one fenced json object") || strings.Contains(p, "never at the result root") {
				t.Fatal("custom/legacy stage was silently upgraded")
			}
		})
	}
}

func TestNativePlannerVersionSurvivesCompactionAndUsesReservedExactSeparator(t *testing.T) {
	n := nativePlannerNodeFixture()
	guidance := singleSourceReviewInstruction + reviewScopeInstruction + reportConfidenceInstruction + reportDeliverableInstruction + implementationDecisionInstruction + ImplementationPartsInstruction + ImplementationPatchInstruction + implementationEfficiencyInstruction + computationAuditInstruction + kernelReviewNavigationInstruction + researchInputSharingInstruction + researchHandoffInstruction + stageInputPagesInstruction
	version := reportExecutionPromptVersion(n, guidance)
	if len(version) > 128 || !IsNativePlannerProtocol(version) || !strings.HasPrefix(version, "p-") || strings.Count(version, "|") != 1 || !strings.Contains(version, stageInputPagesVersion) {
		t.Fatal("compaction lost transport identity", version)
	}
	n.PromptVersion = version
	if reportExecutionPromptVersion(n, guidance) != version {
		t.Fatal("frozen effective identity changed")
	}
	for _, invalid := range []string{"custom-native-planner-v1", "custom|native-planner-v10", "custom|native-planner-v1-x", "custom|native-planner-v1|native-planner-v1", "|native-planner-v1", strings.Repeat("x", 128) + nativePlannerVersionSuffix, "a b|native-planner-v1"} {
		if IsNativePlannerProtocol(invalid) {
			t.Fatal("ambiguous/invalid execution identity accepted", invalid)
		}
	}
}
