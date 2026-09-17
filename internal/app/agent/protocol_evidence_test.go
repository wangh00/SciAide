package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func TestProtocolEvidenceRolloverPreservesFrozenInputAndCompletedCalls(t *testing.T) {
	for _, protocol := range []modelcap.APIProtocol{modelcap.ProtocolOpenAIResponses, modelcap.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			input := "frozen revision code and scientific contract\n" + strings.Repeat("x", 105854)
			messages := []conversation.Message{{ID: "u", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: input}}}}
			definition := tool.Definition{QualifiedName: resource.OpenTool, Description: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}
			args := json.RawMessage(`{"actionId":"res_1"}`)
			calls := []tool.Call{{ProviderCallID: "call", ToolName: resource.OpenTool, Arguments: args, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Text: "complete evidence", Structured: json.RawMessage(`{"actions":[{"tool":"builtin.resource.open","actionId":"res_2"}]}`)}}}
			payload, _ := json.Marshal(map[string]string{"type": "reasoning", "encrypted_content": strings.Repeat("PRIVATE", 5000)})
			turns := []model.ProviderTurn{{TurnIndex: 1, Protocol: protocol, Items: []model.ProviderItem{
				{Ordinal: 0, Type: "reasoning", Payload: payload},
				{Ordinal: 1, Type: "message", Payload: json.RawMessage(`{"type":"message","content":[{"type":"output_text","text":"checking the previous code"}]}`)},
				{Ordinal: 2, Type: "function_call", CallID: "call", Payload: json.RawMessage(`{"type":"function_call","call_id":"call","name":"resource_open","arguments":"{\"actionId\":\"res_1\"}"}`)},
			}}}
			original, _ := json.Marshal(turns)
			build := func(allow bool, fixed int, currentTurns []model.ProviderTurn) (model.ChatRequest, ContextBuildInfo, error) {
				return NewContextBuilder(190000).buildWithResearchState(context.Background(), messages, "", "u", []tool.Definition{definition}, calls, skill.RunContext{}, "", strings.Repeat("s", fixed), "host phase EXPLORE", ContextLimits{EffectiveTokens: 190000, AutoCompactTokens: 180000, AllowProtocolRollover: allow}, contextmemory.Checkpoint{}, currentTurns...)
			}
			if _, _, err := build(false, 54000, turns); err == nil {
				t.Fatal("fixture did not reproduce native protocol overflow")
			}
			request, info, err := build(true, 54000, turns)
			if err != nil {
				t.Fatal(err)
			}
			if len(request.ProviderTurns) != 0 || !info.Compacted || info.EstimatedTokens > 180000 || len(request.Tools) != 1 || request.ForcedTool != "" {
				t.Fatalf("invalid independent exchange: info=%+v", info)
			}
			foundInput, foundEvidence, foundCommentary := false, false, false
			for _, m := range request.Messages {
				if m.Role == model.RoleTool || len(m.ToolCalls) > 0 || m.ToolCallID != "" || strings.Contains(m.Content, "PRIVATE") {
					t.Fatal("live or encrypted protocol leaked into rollover")
				}
				foundInput = foundInput || m.Content == input
				foundEvidence = foundEvidence || strings.Contains(m.Content, "complete evidence") && strings.Contains(m.Content, "res_1") && strings.Contains(m.Content, `"tool":"resource_open"`)
				foundCommentary = foundCommentary || strings.Contains(m.Content, "checking the previous code")
			}
			if !foundInput || !foundEvidence || !foundCommentary || request.Messages[len(request.Messages)-1].Role != model.RoleSystem {
				t.Fatal("lost exact input, evidence, visible commentary or host state")
			}
			unchanged, _ := json.Marshal(turns)
			if string(original) != string(unchanged) {
				t.Fatal("mutated original protocol audit")
			}
			if native, _, err := build(true, 1000, turns); err != nil || !reflect.DeepEqual(native.ProviderTurns[0].Items, turns[0].Items) {
				t.Fatal("native protocol changed despite sufficient budget", err)
			}
			if _, _, err := build(true, 72750, turns); err == nil {
				t.Fatal("oversized evidence bypassed context limit")
			}
			broken := append([]model.ProviderTurn(nil), turns...)
			broken[0].Items = append([]model.ProviderItem(nil), turns[0].Items...)
			broken[0].Items[2].CallID = "missing"
			if _, _, err := build(true, 54000, broken); err == nil || !strings.Contains(err.Error(), "missing tool result") {
				t.Fatal("protocol integrity failure was treated as budget rollover", err)
			}
			_, _, err = build(false, 54000, turns)
			var overflow *providerHistoryBudgetError
			if !errors.As(err, &overflow) {
				t.Fatal("budget error must be typed", err)
			}
		})
	}
}

func TestContextBudgetChangesApplyToNewRunsNotExistingTurns(t *testing.T) {
	for _, existingTurns := range []int{0, 1} {
		t.Run(string(rune('0'+existingTurns)), func(t *testing.T) {
			loop, state, provider := newLoopFixture(t, nil, submissionScript("done"))
			oldBudget := modelcap.ResolveContextBudget(200000, 0, modelcap.ContextWindowSourceManual)
			newBudget := modelcap.ResolveContextBudget(300000, 0, modelcap.ContextWindowSourceManual)
			state.run.ModelTurns = existingTurns
			state.run.ContextWindowTokens, state.run.ContextBudgetTokens, state.run.AutoCompactTokenLimit = oldBudget.WindowTokens, oldBudget.EffectiveTokens, oldBudget.AutoCompactTokens
			loop.models = budgetResolver{model: provider, budget: newBudget}
			if outcome := loop.Run(context.Background(), state.run.ID); outcome != OutcomeCompleted {
				t.Fatal(outcome, state.run.ErrorMessage)
			}
			want := oldBudget
			if existingTurns == 0 {
				want = newBudget
			}
			if state.run.ContextWindowTokens != want.WindowTokens || state.run.ContextBudgetTokens != want.EffectiveTokens || state.run.AutoCompactTokenLimit != want.AutoCompactTokens {
				t.Fatal("context budget changed at the wrong lifecycle boundary")
			}
		})
	}
}
