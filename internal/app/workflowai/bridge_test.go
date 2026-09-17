package workflowai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

func TestCompactVisibleDraftRemovesStructuredTail(t *testing.T) {
	value := compactVisibleDraft("正在检查数据字段。\n```json\n{\"status\":\"ok\"}\n```")
	if value != "正在检查数据字段。" {
		t.Fatalf("compactVisibleDraft() = %q", value)
	}
}

func TestStageTextFallsBackToCompletedRunStep(t *testing.T) {
	snapshot := chat.Snapshot{
		Run:      chat.Run{Status: chat.RunCompleted, AssistantMessageID: "assistant"},
		Messages: []conversation.Message{{ID: "assistant", Role: conversation.RoleAssistant, Parts: []conversation.MessagePart{{Type: "text"}}}},
		RunSteps: []chat.RunStep{{Commentary: "说明\n```json\n{\"summary\":\"done\"}\n```"}},
	}
	if got := stageText(snapshot); got == "" || !strings.Contains(got, `"summary":"done"`) {
		t.Fatalf("stageText() = %q", got)
	}
}

func TestCompactToolResultIsBounded(t *testing.T) {
	value := compactToolResult("a\n\n" + "b" + "c")
	if value != "a bc" {
		t.Fatalf("compactToolResult() = %q", value)
	}
	long := compactToolResult(strings.Repeat("x", 181))
	if len([]rune(long)) != 181 || !strings.HasSuffix(long, "…") {
		t.Fatalf("compactToolResult() did not preserve bounded ellipsis: %q", long)
	}
}

func TestCurrentActionPrefersRunningTool(t *testing.T) {
	activity := workflowActivityWithTool(tool.CallRunning, "builtin.python.execute")
	got := currentAction(chat.Snapshot{Run: chat.Run{Status: chat.RunRunning}}, activity)
	if got != "正在执行：builtin.python.execute" {
		t.Fatalf("currentAction() = %q", got)
	}
}

func TestSafeToolSummaryShowsBoundedTargets(t *testing.T) {
	call := tool.Call{ToolName: "builtin.workspace.read_text", Arguments: []byte(`{"path":"data/results.csv"}`)}
	if got := safeToolSummary(call); got != "读取 Workspace 文件 · data/results.csv" {
		t.Fatalf("safeToolSummary() = %q", got)
	}
	call = tool.Call{ToolName: "builtin.shell.execute", Arguments: []byte(`{"command":"Get-ChildItem","workdir":"analysis"}`)}
	if got := safeToolSummary(call); got != "Shell 命令 · analysis" {
		t.Fatalf("safeToolSummary() = %q", got)
	}
}

func TestSafeToolSummaryLabelsReviewGateAsDeliveryCheck(t *testing.T) {
	call := tool.Call{ToolName: "builtin.research.workflow.review.gate"}
	if got := safeToolSummary(call); got != "核验研究交付条件" {
		t.Fatalf("safeToolSummary(review gate) = %q", got)
	}
}

func TestActivityToolProjectionCarriesSafeArgumentsAndPermissions(t *testing.T) {
	call := tool.Call{
		ToolName:    "builtin.shell.execute",
		Arguments:   []byte(`{"command":"Get-ChildItem","apiKey":"hidden"}`),
		Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionProcessExecute, Resource: "powershell"}},
	}
	arguments := tool.SafeActivityArguments(call.Arguments)
	if !strings.Contains(string(arguments), "Get-ChildItem") || strings.Contains(string(arguments), "hidden") {
		t.Fatalf("unsafe activity arguments = %s", arguments)
	}
	if len(call.Permissions) != 1 || call.Permissions[0].Kind != tool.PermissionProcessExecute {
		t.Fatalf("permissions fixture invalid: %#v", call.Permissions)
	}
}

func workflowActivityWithTool(status tool.CallStatus, name string) workflow.AIStageActivity {
	return workflow.AIStageActivity{ToolCalls: []workflow.AIStageToolActivity{{ToolName: name, Status: status}}}
}

func TestResourceAndSearchActivityNames(t *testing.T) {
	c := tool.Call{ToolName: "builtin.resource.open", Result: &tool.Result{Structured: json.RawMessage(`{"label":"读取 Skill 章节 · statistical-analysis / 检验选择"}`)}}
	if got := safeToolSummary(c); got != "读取 Skill 章节 · statistical-analysis / 检验选择" {
		t.Fatal(got)
	}
	for name, want := range map[string]string{"builtin.knowledge.search": "本地知识库检索 · light", "builtin.research.search": "在线学术检索 · light"} {
		if got := safeToolSummary(tool.Call{ToolName: name, Arguments: json.RawMessage(`{"query":"light"}`)}); got != want {
			t.Fatal(got)
		}
	}
}

func TestActivityLabelsIncludeIssuedButNotYetReadChildren(t *testing.T) {
	labels := issuedActivityLabels([]tool.Call{{ToolName: "builtin.resource.open", Result: &tool.Result{Structured: json.RawMessage(`{"actionId":"res_parent","label":"资料目录","actions":[{"actionId":"res_child","label":"读取资料 · growth.csv"}]}`)}}})
	if labels["res_child"] != "读取资料 · growth.csv" {
		t.Fatal(labels)
	}
}
