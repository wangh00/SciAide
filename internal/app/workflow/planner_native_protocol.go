package workflow

import "strings"

// The execution-only separator cannot occur in a compiled node's version
// ([a-z][a-z0-9_-]{0,63}). Old/custom node names therefore cannot opt into a
// different transport merely by containing a similarly named version token.
const NativePlannerProtocolVersion = "native-planner-v1"

const nativePlannerVersionSuffix = "|" + NativePlannerProtocolVersion

// NativePlannerActionInstruction is the single host-owned transport contract
// used both when freezing the prompt and when preparing the actual request.
const NativePlannerActionInstruction = "Current planning action protocol: use a native function call on every turn. Choose a necessary frozen research action, including loading a selected Skill that has not actually been loaded in this Run, OR call submit_stage_result EXACTLY ONCE with the full planning result object. Do not mix submission with research IO. No lookup is required when no external fact is needed, but that does not waive actual Skill loading. Do not output a free-text or fenced JSON plan instead of the submission call. Arguments are the complete literal JSON result object itself, not a wrapper or string. Schema validation, Skill verification and scientific review remain unchanged; a submission is not an approval."

func IsNativePlannerProtocol(version string) bool {
	base, protocol, ok := strings.Cut(version, "|")
	if !ok || protocol != NativePlannerProtocolVersion || base == "" || len(version) > 128 {
		return false
	}
	for _, c := range base {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func usesNativePlannerProtocol(node CompiledNode) bool {
	builtin := dynamicResearchStarterTemplate().Definition.Nodes[0]
	if !(node.ID == builtin.ID && node.Name == builtin.Name && node.Kind == builtin.Kind && node.SkillRouting == builtin.SkillRouting &&
		node.PromptVersion == builtin.PromptVersion && node.Prompt == builtin.Prompt &&
		node.ReviewPolicy == builtin.ReviewPolicy && node.StageMaterialsVersion == builtin.StageMaterialsVersion &&
		!node.SideEffect && len(node.Dependencies) == 0 && rawJSONEqual(node.Arguments, builtin.Arguments) && rawJSONEqual(node.OutputSchema, builtin.OutputSchema)) {
		return false
	}
	// The compiler appends the standard Skill IO names and deduplicates them.
	// Match that effective scope without trusting text in the composed prompt.
	expected := make(map[string]bool)
	for _, name := range append(append([]string(nil), builtin.AllowedTools...), workflowSkillReadTools...) {
		expected[name] = true
	}
	if len(node.AllowedTools) != len(expected) {
		return false
	}
	for _, tool := range node.AllowedTools {
		if !expected[tool.QualifiedName] {
			return false
		}
		delete(expected, tool.QualifiedName)
	}
	return len(expected) == 0
}

func nativePlannerPrompt(prompt string) string {
	// Only called after exact authentication of the built-in source node.
	// Never run these replacements on stage_input, Skill text, or a saved prompt.
	prompt = strings.Replace(prompt, "你是 SciAide 科研模式的动态研究架构师。", "当前任务是在科研工作流中设计动态研究路线；这描述任务职责，不改变模型身份或供应商规则。", 1)
	prompt = strings.Replace(prompt, "完成必要的工具调用后，必须在同一响应末尾提交一个完整、可解析且符合下方 Schema 的路线 JSON。", "完成必要的研究工具调用后，在后续独立回合调用 submit_stage_result，以完整、可解析且符合下方 Schema 的路线对象为参数；不得与研究工具混批，也不得以响应正文替代提交。", 1)
	prompt = strings.Replace(prompt, "最终代码块必须是字面量 JSON；禁止输出 .replace(...)、函数调用、注释、尾随逗号或任何 JavaScript/Python 表达式。", "提交工具的结果对象参数必须是字面量 JSON；参数值不得含 .replace(...)、注释、尾随逗号或可执行的 JavaScript/Python 表达式。", 1)
	return strings.Replace(prompt, "信息充分时省略 clarification 或返回 needsUserInput=false、questions=[]", `信息充分时省略 clarification，或设置 clarification={"needsUserInput":false,"questions":[]}；needsUserInput 和 questions 仅属于 clarification 子对象，不得提升到根对象`, 1)
}
