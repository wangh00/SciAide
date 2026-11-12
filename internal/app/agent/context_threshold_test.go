package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func TestCurrentInputAboveCompactThresholdKeepsCompletedEvidence(t *testing.T) {
	ctx := context.Background()
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprint(native), func(t *testing.T) {
			input := strings.Repeat("完整冻结的研究要求😀", 750) + "不得丢失最后一个限制"
			messages := []conversation.Message{checkpointText("old", "old-run", strings.Repeat("早期讨论", 1000)), checkpointText("u", "run", input)}
			calls := []tool.Call{
				{ID: "seed", ProviderCallID: "seed", ToolName: "seed", Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: "HEAD-original-evidence\n" + strings.Repeat("普通证据", 1500) + "\nTAIL-original-evidence"}},
				{ID: "human", ProviderCallID: "human", ToolName: userinput.ToolName, Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: "完整保留我确认的研究范围和决定"}},
			}
			turns := []model.ProviderTurn{}
			if native {
				for i, call := range calls {
					turns = append(turns, model.ProviderTurn{TurnIndex: i + 1, Protocol: modelcap.ProtocolOpenAIResponses, Items: []model.ProviderItem{{Ordinal: 0, Type: "function_call", CallID: call.ProviderCallID, Payload: json.RawMessage(fmt.Sprintf(`{"type":"function_call","call_id":%q,"arguments":"{}"}`, call.ProviderCallID))}}})
				}
			}
			original, _ := json.Marshal([]any{messages, calls, turns})
			fixed := len([]rune(fixedSystemRules)) + len([]rune(input))
			limits := ContextLimits{AutoCompactTokens: fixed - 1, EffectiveTokens: fixed + 3200, CurrentUserHostToolReferences: true}
			request, info, err := NewContextBuilder(0).BuildWithRuntimeContext(ctx, messages, "", "u", nil, calls, skill.RunContext{}, limits, contextmemory.Checkpoint{}, turns...)
			if err != nil {
				t.Fatal(err)
			}
			if info.CompactedThroughMessageID != "old" || info.EstimatedTokens > limits.EffectiveTokens || info.AutoCompactTokenLimit != limits.AutoCompactTokens || info.ContextBudgetTokens != limits.EffectiveTokens {
				t.Fatal("threshold became a hard cap or old history bypassed compaction", info)
			}
			if request.Messages[1].Content != input || !request.Messages[1].HostToolReferences || len(request.ProviderTurns) != len(turns) {
				t.Fatal("changed current input or native ownership")
			}
			results := map[string]string{}
			for _, message := range request.Messages {
				if message.Role == model.RoleTool {
					results[message.ToolCallID] = message.Content
				}
			}
			for i, turn := range request.ProviderTurns {
				if !reflect.DeepEqual(turn.Items, turns[i].Items) {
					t.Fatal("changed native protocol item")
				}
				for _, result := range turn.ToolResults {
					results[result.ToolCallID] = result.Content
				}
			}
			if results["human"] != toolCallContext(calls[1]) || !strings.Contains(results["seed"], "HEAD-original-evidence") || !strings.Contains(results["seed"], "TAIL-original-evidence") || !strings.Contains(results["seed"], "tool result truncated") {
				t.Fatal("lost human decision or silently emptied evidence", results)
			}
			unchanged, _ := json.Marshal([]any{messages, calls, turns})
			if string(original) != string(unchanged) {
				t.Fatal("mutated original records")
			}
			limits.EffectiveTokens = fixed - 1
			if _, _, err = NewContextBuilder(0).BuildWithRuntimeContext(ctx, messages, "", "u", nil, calls, skill.RunContext{}, limits, contextmemory.Checkpoint{}, turns...); err == nil {
				t.Fatal("genuine current-input overflow was admitted")
			}
		})
	}
}

// Read only a separately captured incident; fixed host context is an explicit
// synthetic footprint because the formal DB does not retain full HTTP bodies.
// No application startup, formal-store writes, tools or model requests.
func TestCapturedReviewInputCrossingCompactThreshold(t *testing.T) {
	path := os.Getenv("SCIAIDE_CONTEXT_THRESHOLD_CAPTURE")
	if path == "" {
		t.Skip("set explicit incident capture path for offline replay")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Run struct {
			Budget int `json:"context_budget_tokens"`
			Auto   int `json:"auto_compact_token_limit"`
		} `json:"run"`
		Execution []struct {
			Prompt string `json:"prompt_text"`
			Schema string `json:"output_schema_json"`
		} `json:"execution"`
	}
	if err := json.Unmarshal(encoded, &capture); err != nil || len(capture.Execution) != 1 || capture.Run.Auto != 244800 || capture.Run.Budget != 258400 {
		t.Fatal("capture identity changed", err)
	}
	var raw struct {
		Calls []struct {
			ID             string `json:"id"`
			ProviderCallID string `json:"provider_call_id"`
			ToolName       string `json:"tool_name"`
			Arguments      string `json:"arguments_json"`
			ModelContext   string `json:"model_context_text"`
			Text           string `json:"text_content"`
		} `json:"calls"`
	}
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	calls := []tool.Call{}
	for _, call := range raw.Calls {
		calls = append(calls, tool.Call{ID: call.ID, ProviderCallID: call.ProviderCallID, ToolName: call.ToolName, Arguments: json.RawMessage(call.Arguments), ModelContext: call.ModelContext, ModelContextVersion: tool.ModelContextSnapshotVersion, Result: &tool.Result{Status: tool.ResultSuccess, Text: call.Text}})
	}
	input := capture.Execution[0].Prompt
	if len([]rune(input)) != 213006 || len(calls) != 3 {
		t.Fatal("incident prompt or completed calls changed")
	}
	fixed := strings.Repeat("s", 32891-len([]rune(fixedSystemRules))-2)
	limits := ContextLimits{EffectiveTokens: capture.Run.Budget, AutoCompactTokens: capture.Run.Auto, CurrentUserHostToolReferences: true}
	request, info, err := NewContextBuilder(0).buildWithResearchState(context.Background(), []conversation.Message{checkpointText("u", "run", input)}, "", "u", nil, calls, skill.RunContext{}, "", fixed, "", limits, contextmemory.Checkpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if info.CompactedThroughMessageID != "" || info.EstimatedTokens > limits.EffectiveTokens || request.Messages[1].Content != input || len(request.Messages) != 2+len(calls)*2 {
		t.Fatal("frozen input or completed tool pairing lost", info)
	}
	for _, result := range request.Messages[2:] {
		if result.Role == model.RoleTool && len([]rune(result.Content)) < 256 {
			t.Fatal("completed evidence starved")
		}
	}
	submitted, err := fitFinalRequest(request, calls, nil, limits.EffectiveTokens, func(r model.ChatRequest) model.ChatRequest {
		return stageSubmissionRequest(r, json.RawMessage(capture.Execution[0].Schema))
	})
	if err != nil || submitted.Messages[0].Content != request.Messages[0].Content || submitted.Messages[1].Content != input {
		t.Fatal("full-schema submission still exceeds budget or changed input", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(encoded) != string(after) {
		t.Fatal("capture changed", err)
	}
	t.Logf("original prompt=%d; fixed host footprint=32891 (synthetic); completed calls=%d; admitted estimate=%d; soft threshold=%d; effective budget=%d; frozen input and source audit unchanged", len([]rune(input)), len(calls), info.EstimatedTokens, limits.AutoCompactTokens, limits.EffectiveTokens)
}

func TestCrossingCompactThresholdDoesNotBlockNativeRollover(t *testing.T) {
	input := strings.Repeat("冻结输入完整保留", 1000)
	messages := []conversation.Message{checkpointText("old", "old-run", strings.Repeat("旧讨论", 5000)), checkpointText("u", "run", input)}
	calls := []tool.Call{
		{ProviderCallID: "seed", ToolName: "seed", Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: strings.Repeat("已有文献证据", 1000)}},
		{ProviderCallID: "read", ToolName: "read", Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: "已读材料"}},
	}
	turns := []model.ProviderTurn{{TurnIndex: 1, Protocol: modelcap.ProtocolOpenAIResponses, Items: []model.ProviderItem{
		{Ordinal: 0, Type: "reasoning", Payload: json.RawMessage(`{"type":"reasoning","encrypted_content":"` + strings.Repeat("PRIVATE", 4000) + `"}`)},
		{Ordinal: 1, CallID: "read", Payload: json.RawMessage(`{"type":"function_call","call_id":"read","arguments":"{}"}`)},
	}}}
	original, _ := json.Marshal([]any{messages, calls, turns})
	limits := ContextLimits{EffectiveTokens: 18000, AutoCompactTokens: 6000, AllowProtocolRollover: true, CurrentUserHostToolReferences: true}
	request, info, err := NewContextBuilder(0).BuildWithRuntimeContext(context.Background(), messages, "", "u", nil, calls, skill.RunContext{}, limits, contextmemory.Checkpoint{}, turns...)
	if err != nil || !info.ProtocolRollover || info.CompactedThroughMessageID != "old" || info.EstimatedTokens > limits.EffectiveTokens || len(request.ProviderTurns) != 0 || len(request.CompletedToolEvidence) != 2 {
		t.Fatal("current input still blocked completed-evidence rollover", info, err)
	}
	found := false
	for _, message := range request.Messages {
		found = found || message.Content == input && message.HostToolReferences
		if strings.Contains(message.Content, "PRIVATE") || message.Role == model.RoleTool || len(message.ToolCalls) > 0 {
			t.Fatal("incomplete native state leaked into independent exchange")
		}
	}
	after, _ := json.Marshal([]any{messages, calls, turns})
	if !found || string(original) != string(after) {
		t.Fatal("rollover mutated frozen input or audit")
	}
}

func TestCrossingCompactThresholdReservesHostAndHumanAlongsideNative(t *testing.T) {
	input := strings.Repeat("研究目标", 1000)
	calls := []tool.Call{
		{ProviderCallID: "seed", ToolName: "seed", Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: strings.Repeat("原文证据", 1000)}},
		{ProviderCallID: "human", ToolName: userinput.ToolName, Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: strings.Repeat("我的研究决定", 300)}},
		{ProviderCallID: "read", ToolName: "read", Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: strings.Repeat("其他工具材料", 2000)}},
	}
	turns := []model.ProviderTurn{{TurnIndex: 1, Items: []model.ProviderItem{{Ordinal: 0, CallID: "read", Payload: json.RawMessage(`{"type":"function_call","call_id":"read","arguments":"{}"}`)}}}}
	fixed := len([]rune(fixedSystemRules)) + len([]rune(input))
	mandatory := len([]rune(string(turns[0].Items[0].Payload))) + 2*256 + len([]rune(toolCallContext(calls[1])))
	for _, call := range calls[:2] {
		mandatory += estimateMessageTokens(model.Message{ToolCalls: []model.ToolCall{{Name: call.ToolName, Arguments: call.Arguments}}})
	}
	limits := ContextLimits{EffectiveTokens: fixed + mandatory, AutoCompactTokens: fixed - 1}
	request, _, err := NewContextBuilder(0).BuildWithRuntimeContext(context.Background(), []conversation.Message{checkpointText("u", "run", input)}, "", "u", nil, calls, skill.RunContext{}, limits, contextmemory.Checkpoint{}, turns...)
	if err != nil {
		t.Fatal("native result consumed host/human reservation", err)
	}
	if len(request.ProviderTurns[0].ToolResults[0].Content) == 0 {
		t.Fatal("native result starved")
	}
	results := map[string]string{}
	for _, message := range request.Messages {
		if message.Role == model.RoleTool {
			results[message.ToolCallID] = message.Content
		}
	}
	if results["human"] != toolCallContext(calls[1]) || len([]rune(results["seed"])) < 256 {
		t.Fatal("human/seed evidence lost", results)
	}
}

func TestCompactThresholdFallbackDoesNotBypassProtocolIntegrityOrHardBudget(t *testing.T) {
	input := strings.Repeat("不可改写输入", 1000)
	messages := []conversation.Message{checkpointText("u", "run", input)}
	call := tool.Call{ProviderCallID: "read", ToolName: "read", Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: "实际已读取的来源"}}
	turn := model.ProviderTurn{TurnIndex: 1, Items: []model.ProviderItem{{Ordinal: 0, CallID: "absent", Payload: json.RawMessage(`{"type":"function_call","call_id":"absent"}`)}}}
	limits := ContextLimits{EffectiveTokens: 20000, AutoCompactTokens: 4000, AllowProtocolRollover: true}
	if _, _, err := NewContextBuilder(0).BuildWithRuntimeContext(context.Background(), messages, "", "u", nil, []tool.Call{call}, skill.RunContext{}, limits, contextmemory.Checkpoint{}, turn); err == nil || !strings.Contains(err.Error(), "missing tool result") {
		t.Fatal("soft threshold converted protocol corruption into usable evidence", err)
	}
	call.ToolName = userinput.ToolName
	call.Result.Text = strings.Repeat("全部人类决定必须保留", 3000)
	if _, _, err := NewContextBuilder(0).BuildWithRuntimeContext(context.Background(), messages, "", "u", nil, []tool.Call{call}, skill.RunContext{}, limits, contextmemory.Checkpoint{}); err == nil {
		t.Fatal("oversized human decision bypassed hard budget or was truncated")
	}
	call.ToolName, call.Arguments, call.Result.Text = "read", json.RawMessage(`{"frozenArguments":"`+strings.Repeat("x", 20000)+`"}`), "source"
	if _, _, err := NewContextBuilder(0).BuildWithRuntimeContext(context.Background(), messages, "", "u", nil, []tool.Call{call}, skill.RunContext{}, limits, contextmemory.Checkpoint{}); err == nil || !strings.Contains(err.Error(), "协议估算") || !strings.Contains(err.Error(), "非工具调用次数") {
		t.Fatal("oversized call bypassed hard budget or diagnostic implied call count", err)
	}
}
