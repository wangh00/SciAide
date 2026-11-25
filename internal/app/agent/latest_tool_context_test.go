package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/model"
)

func TestToolContextKeepsNewestCompleteResultWithinSharedBudget(t *testing.T) {
	old := `{"status":"success","text":"old","structured":{"payload":"` + strings.Repeat("older evidence ", 700) + `"}}`
	latest := `{"status":"success","text":"latest page","structured":{"queryId":"q","offset":3,"nextOffset":6,"items":["` + strings.Repeat("current ", 210) + `"]}}`
	values := []string{old, latest}
	fitted := fitToolContexts(values, 2500)
	if fitted[1] != latest {
		t.Fatal("shared result budget hid the latest complete page even though it fits with an old-result omission")
	}
	if toolContextTokens(fitted) > 2500 || !json.Valid([]byte(fitted[0])) || !strings.Contains(fitted[0], `"structuredNotShown":true`) || strings.Contains(fitted[0], `"nextOffset"`) {
		t.Fatal("old-result omission lost budget, JSON or explicit disclosure")
	}
	// Old results need an actual valid omission marker, not a fixed 256-rune
	// reservation which can itself displace a readable current result.
	tight := len([]rune(latest)) + 24
	fitted = fitToolContexts(values, tight)
	if fitted[1] != latest || toolContextTokens(fitted) > tight || !json.Valid([]byte(fitted[0])) || !strings.Contains(fitted[0], `"contentNotShown":true`) {
		t.Fatal("fixed old-result reserve hid a fitting current result")
	}
}

func TestCurrentToolResultUsesExistingEffectiveHeadroom(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprint(native), func(t *testing.T) {
			calls := []tool.Call{{ProviderCallID: "old", ToolName: "old", Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: strings.Repeat("old", 600)}}, {ProviderCallID: "latest", ToolName: "read", Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Structured: json.RawMessage(`{"nextOffset":6,"value":"` + strings.Repeat("current", 130) + `"}`)}}}
			turns := []model.ProviderTurn{}
			protocol := 0
			for i, c := range calls {
				if native {
					payload := json.RawMessage(fmt.Sprintf(`{"type":"function_call","call_id":%q,"arguments":"{}"}`, c.ProviderCallID))
					turns = append(turns, model.ProviderTurn{TurnIndex: i + 1, Items: []model.ProviderItem{{Ordinal: 0, CallID: c.ProviderCallID, Payload: payload}}})
					protocol += len(payload)
				} else {
					protocol += estimateMessageTokens(model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{Name: c.ToolName, Arguments: c.Arguments}}})
				}
			}
			base := estimateRequestTokens(model.ChatRequest{Messages: []model.Message{{Role: model.RoleSystem, Content: fixedSystemRules}}})
			auto := base + estimateMessageTokens(model.Message{Role: model.RoleUser, Content: "current"}) + protocol + 2*256 + 100
			limits := ContextLimits{AutoCompactTokens: auto, EffectiveTokens: auto + 2000}
			req, info, err := NewContextBuilder(0).BuildWithRuntimeContext(context.Background(), []conversation.Message{checkpointText("u", "run", "current")}, "", "u", nil, calls, skill.RunContext{}, limits, contextmemory.Checkpoint{}, turns...)
			if err != nil || info.EstimatedTokens > limits.EffectiveTokens {
				t.Fatal("effective window changed or request failed", info, err)
			}
			context := ""
			for _, m := range req.Messages {
				if m.ToolCallID == "latest" {
					context = m.Content
				}
			}
			for _, turn := range req.ProviderTurns {
				for _, m := range turn.ToolResults {
					if m.ToolCallID == "latest" {
						context = m.Content
					}
				}
			}
			if !strings.Contains(context, strings.Repeat("current", 130)) || strings.Contains(context, `"structuredNotShown":true`) {
				t.Fatal("soft compact threshold hid a current result that fits the effective window")
			}
		})
	}
}
