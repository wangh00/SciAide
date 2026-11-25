package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func toolLedgerFixture(count int) ([]tool.Call, []model.ProviderTurn) {
	calls := []tool.Call{}
	items := []model.ProviderItem{{Ordinal: 0, Type: "reasoning", Payload: json.RawMessage(`{"type":"reasoning","encrypted_content":"` + strings.Repeat("PRIVATE_NATIVE", 5000) + `"}`)}}
	for i := 0; i < count; i++ {
		args, _ := json.Marshal(map[string]string{"query": fmt.Sprintf("query-%03d-", i) + strings.Repeat("x", 1000)})
		r := tool.Result{Status: tool.ResultSuccess, Text: fmt.Sprintf("original-result-%03d", i)}
		c := tool.Call{ID: fmt.Sprintf("host-%03d", i), ProviderCallID: fmt.Sprintf("call-%03d", i), RunID: "run", ToolName: "builtin.fixture.read", ToolVersion: "1", Status: tool.CallCompleted, Arguments: args, Result: &r, ModelContextVersion: tool.ModelContextSnapshotVersion}
		c.ModelContext = tool.BuildModelContextSnapshot(c.ID, "", r)
		calls = append(calls, c)
		items = append(items, model.ProviderItem{Ordinal: i + 1, Type: "function_call", CallID: c.ProviderCallID, Payload: json.RawMessage(`{"type":"function_call","call_id":"` + c.ProviderCallID + `"}`)})
	}
	turns := []model.ProviderTurn{}
	for start := 0; start < count; start += 32 {
		group := []model.ProviderItem{}
		if start == 0 {
			group = append(group, items[0])
		}
		for _, item := range items[start+1 : min(count, start+32)+1] {
			item.Ordinal = len(group)
			group = append(group, item)
		}
		turns = append(turns, model.ProviderTurn{TurnIndex: len(turns) + 1, Protocol: modelcap.ProtocolOpenAIResponses, Items: group})
	}
	return calls, turns
}

func TestToolLedgerWindowBaselineAndBoundedReplay(t *testing.T) {
	for _, count := range []int{32, 96} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			calls, turns := toolLedgerFixture(count)
			answer := "human decision: " + strings.Repeat("保留构念🧪", 300)
			calls = append(calls[:2], append([]tool.Call{{ID: "human", RunID: "run", ProviderCallID: "human", ToolName: userinput.ToolName, Status: tool.CallCompleted, Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: answer}}}, calls[2:]...)...)
			beforeTurns, _ := json.Marshal(turns)
			originalContext, originalArgs := []string{}, []string{}
			for _, c := range calls {
				originalContext, originalArgs = append(originalContext, c.ModelContext), append(originalArgs, string(c.Arguments))
			}
			messages := []conversation.Message{{ID: "u", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: "exact current research input; do not approve a conclusion"}}}}
			defs := []tool.Definition{{QualifiedName: "builtin.fixture.read", InputSchema: json.RawMessage(`{"type":"object"}`)}}
			limits := ContextLimits{EffectiveTokens: 24000, AutoCompactTokens: 21600, AllowProtocolRollover: true}
			builder := NewContextBuilder(24000)
			_, _, baselineErr := builder.buildWithResearchState(context.Background(), messages, "", "u", defs, calls, skill.RunContext{}, "", "", "", limits, contextmemory.Checkpoint{}, turns...)
			var budget *contextBudgetError
			if !errors.As(baselineErr, &budget) || budget.Budget.Phase != "completed_evidence" || budget.Budget.Reserved == 0 {
				t.Fatal("original budget rejection did not reproduce", baselineErr)
			}
			t.Logf("baseline: %d original calls rejected at unchanged 24000 window; reserved=%d", count, budget.Budget.Reserved)
			defs = append(defs, tool.Definition{QualifiedName: contextmemory.ToolLedgerReadName, InputSchema: json.RawMessage(`{"type":"object"}`)})
			request, info, err := builder.buildWithToolLedger(context.Background(), messages, "", "u", defs, calls, skill.RunContext{}, "", "", "", limits, contextmemory.Checkpoint{}, turns)
			if err != nil || !info.ProtocolRollover || info.EstimatedTokens > 24000 || len(request.ProviderTurns) != 0 || len(request.CompletedToolEvidence) != contextmemory.ToolLedgerRecentCalls+1 {
				t.Fatal("bounded independent exchange failed", info, err)
			}
			all := ""
			var ref contextmemory.ToolLedgerRef
			for _, m := range request.Messages {
				all += m.Content
				if _, tail, found := strings.Cut(m.Content, "<tool_history_ledger>"); found {
					raw, _, _ := strings.Cut(tail, "</tool_history_ledger>")
					if json.Unmarshal([]byte(raw), &ref) != nil {
						t.Fatal("bad ledger reference")
					}
				}
			}
			if ref.Count != count-8 || strings.Contains(all, "query-000-") || strings.Contains(all, "PRIVATE_NATIVE") || !strings.Contains(all, fmt.Sprintf("query-%03d-", count-1)) || !strings.Contains(all, answer) {
				t.Fatal("window lost protected input/answers or replayed archived/private content")
			}
			ledger, err := contextmemory.BuildToolLedger(calls, "run", ref.ThroughCallID)
			if err != nil || ledger.Ref != ref {
				t.Fatal("prefix cannot be replayed from original records", err)
			}
			for i, c := range calls {
				if c.ModelContext != originalContext[i] || string(c.Arguments) != originalArgs[i] {
					t.Fatal("original call mutated")
				}
			}
			afterTurns, _ := json.Marshal(turns)
			if string(beforeTurns) != string(afterTurns) {
				t.Fatal("native audit mutated")
			}
			t.Logf("candidate: archived=%d retained ordinary=8 + human=1 estimate=%d; exact prefix readable, original records unchanged", ref.Count, info.EstimatedTokens)
		})
	}
}

func TestToolLedgerWindowCannotReconstructOrCrop(t *testing.T) {
	calls, turns := toolLedgerFixture(32)
	defs := []tool.Definition{{QualifiedName: contextmemory.ToolLedgerReadName, InputSchema: json.RawMessage(`{"type":"object"}`)}}
	input := []conversation.Message{{ID: "u", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: "current"}}}}
	build := func(defs []tool.Definition) error {
		_, _, err := NewContextBuilder(24000).buildWithToolLedger(context.Background(), input, "", "u", defs, calls, skill.RunContext{}, "", "", "", ContextLimits{EffectiveTokens: 24000, AutoCompactTokens: 21600}, contextmemory.Checkpoint{}, turns)
		return err
	}
	if build(nil) == nil {
		t.Fatal("window enabled without reader")
	}
	calls[0].ModelContext = ""
	if !errors.Is(build(defs), contextmemory.ErrToolLedgerSnapshotUnavailable) {
		t.Fatal("missing saved snapshot reconstructed from private Result")
	}
	calls[0].ModelContext = "saved"
	calls[0].Result = nil
	if build(defs) == nil {
		t.Fatal("pending tool was archived")
	}
	calls, turns = toolLedgerFixture(32)
	input[0].Parts[0].Text = strings.Repeat("immutable", 5000)
	if build(defs) == nil {
		t.Fatal("oversized current input silently cropped")
	}
	if len(withoutToolLedgerDefinition(defs)) != 0 || len(defs) != 1 {
		t.Fatal("default exposure or source definitions mutated")
	}
}

func TestToolLedgerFallbackEligibilityDoesNotWidenOtherBoundaries(t *testing.T) {
	base := completedBudgetReservationError("baseline", ContextLimits{EffectiveTokens: 24000, AutoCompactTokens: 21600}, 32, 40000).(*contextBudgetError)
	annotated := toolLedgerSnapshotFailure(base)
	var public *apperr.Error
	var original *contextBudgetError
	if !errors.As(annotated, &public) || public.Code != "CONTEXT_BUILD_FAILED" || !strings.Contains(public.UserMessage, "原模型快照") || !errors.As(annotated, &original) || original != base || !strings.Contains(public.Details, "original_snapshot_unavailable") {
		t.Fatal("missing original snapshot was not explicit, or original budget identity changed")
	}
	if !toolLedgerFallbackEligible(fmt.Errorf("wrapped: %w", base), true, false, false, false, "") {
		t.Fatal("original character reservation not eligible")
	}
	for _, change := range []func(*contextBudgetError){
		func(e *contextBudgetError) { e.Budget.Phase = "fixed_input" },
		func(e *contextBudgetError) { e.Budget.Source = "observed_input_overflow_reserve" },
		func(e *contextBudgetError) { e.Budget.Reserved = 0 },
		func(e *contextBudgetError) { e.Budget.ObservedReserve = 1 },
	} {
		modified := *base
		change(&modified)
		if toolLedgerFallbackEligible(&modified, true, false, false, false, "") {
			t.Fatal("another budget boundary widened")
		}
	}
	for _, scope := range [][4]bool{{false, false, false, false}, {true, true, false, false}, {true, false, true, false}, {true, false, false, true}} {
		if toolLedgerFallbackEligible(base, scope[0], scope[1], scope[2], scope[3], "") {
			t.Fatal("unavailable/frozen/resource scope widened")
		}
	}
	if toolLedgerFallbackEligible(base, true, false, false, false, "pending question") || toolLedgerFallbackEligible(nil, true, false, false, false, "") || toolLedgerFallbackEligible(errors.New("transport failure"), true, false, false, false, "") {
		t.Fatal("question/success/transport failure opened ledger fallback")
	}
}

func TestToolLedgerNormalizedBudgetUsesSameWindowContract(t *testing.T) {
	for _, count := range []int{32, 96} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			calls, _ := toolLedgerFixture(count)
			answer := strings.Repeat("保留当前构念🧪", 150)
			calls = append(calls, tool.Call{ID: "human", RunID: "run", ProviderCallID: "human", ToolName: userinput.ToolName, Status: tool.CallCompleted, Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Text: answer}})
			input := []conversation.Message{{ID: "u", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: "exact current research input"}}}}
			defs := []tool.Definition{{QualifiedName: "builtin.fixture.read", InputSchema: json.RawMessage(`{"type":"object"}`)}}
			limits := ContextLimits{EffectiveTokens: 24000, AutoCompactTokens: 21600, AllowProtocolRollover: true}
			b := NewContextBuilder(24000)
			_, _, original := b.buildWithResearchState(context.Background(), input, "", "u", defs, calls, skill.RunContext{}, "", "", "", limits, contextmemory.Checkpoint{})
			var budget *contextBudgetError
			if !errors.As(original, &budget) || budget.Budget.Phase != "tool_protocol" || budget.Budget.Protocol <= 0 {
				t.Fatal("normalized argument-envelope refusal did not reproduce", original)
			}
			t.Logf("normalized baseline %d calls: phase=%s protocol=%d required=%d effective=%d", count, budget.Budget.Phase, budget.Budget.Protocol, budget.Budget.Required, budget.Budget.Effective)
			if !toolLedgerFallbackEligible(original, true, false, false, false, "") {
				t.Fatal("same normalized history reservation cannot reach the shared ledger candidate")
			}
			defs = append(defs, tool.Definition{QualifiedName: contextmemory.ToolLedgerReadName, InputSchema: json.RawMessage(`{"type":"object"}`)})
			r, info, err := b.buildWithToolLedger(context.Background(), input, "", "u", defs, calls, skill.RunContext{}, "", "", "", limits, contextmemory.Checkpoint{}, nil)
			if err != nil || info.EstimatedTokens > 24000 || len(r.ProviderTurns) != 0 || len(r.CompletedToolEvidence) != 9 {
				t.Fatal("normalized independent window failed", info, err)
			}
			all := ""
			for _, m := range r.Messages {
				all += m.Content
			}
			if !strings.Contains(all, answer) || !strings.Contains(all, input[0].Parts[0].Text) {
				t.Fatal("normalized entry cropped human answer/current input")
			}
		})
	}
}

func TestToolLedgerNormalizedResultReserveIsNotZeroNativeOverflow(t *testing.T) {
	calls, _ := toolLedgerFixture(96)
	protocolOnly := 0
	for i := range calls {
		calls[i].Arguments = json.RawMessage(`{"query":"short"}`)
		calls[i].Result = &tool.Result{Status: tool.ResultSuccess, Text: strings.Repeat("saved-result", 100)}
		calls[i].ModelContext = tool.BuildModelContextSnapshot(calls[i].ID, "", *calls[i].Result)
		protocolOnly += estimateMessageTokens(model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{Name: calls[i].ToolName, Arguments: calls[i].Arguments}}})
	}
	input := []conversation.Message{{ID: "u", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: "current"}}}}
	_, _, err := NewContextBuilder(24000).buildWithResearchState(context.Background(), input, "", "u", nil, calls, skill.RunContext{}, "", "", "", ContextLimits{EffectiveTokens: 24000, AutoCompactTokens: 21600, AllowProtocolRollover: true}, contextmemory.Checkpoint{})
	var budget *contextBudgetError
	if protocolOnly >= 24000 || !errors.As(err, &budget) || budget.Budget.Phase != "tool_protocol" || budget.Budget.Protocol <= protocolOnly || !toolLedgerFallbackEligible(err, true, false, false, false, "") {
		t.Fatal("normalized return reservation was misclassified as zero native history overflow", protocolOnly, err)
	}
	for _, change := range []func(*contextBudgetError){func(e *contextBudgetError) { e.Budget.Protocol = 0 }, func(e *contextBudgetError) { e.Budget.Required = e.Budget.Effective }, func(e *contextBudgetError) { e.Budget.Source = "observed_input_overflow_reserve" }, func(e *contextBudgetError) { e.Budget.ObservedReserve = 1 }, func(e *contextBudgetError) { e.Budget.Phase = "final_request" }} {
		modified := *budget
		change(&modified)
		if toolLedgerFallbackEligible(&modified, true, false, false, false, "") {
			t.Fatal("non-overflow or measured/final boundary opened normalized fallback")
		}
	}
	for _, scope := range [][4]bool{{false, false, false, false}, {true, true, false, false}, {true, false, true, false}, {true, false, false, true}} {
		if toolLedgerFallbackEligible(budget, scope[0], scope[1], scope[2], scope[3], "") {
			t.Fatal("normalized scope widened")
		}
	}
	if toolLedgerFallbackEligible(budget, true, false, false, false, "pending") {
		t.Fatal("normalized question continuation widened")
	}
}

func TestToolLedgerEarlyNormalizedBudgetCannotBypassNativeValidation(t *testing.T) {
	for _, invalid := range []string{"turn_order", "item_order", "duplicate_call", "missing_result"} {
		t.Run(invalid, func(t *testing.T) {
			calls, turns := toolLedgerFixture(32)
			// Normalized argument envelopes are rejected before native selection;
			// the shared candidate must still validate the WHOLE native history.
			items := []model.ProviderItem{{Ordinal: 0, Type: "function_call", CallID: calls[0].ProviderCallID, Payload: json.RawMessage(`{"type":"function_call"}`)}}
			turns = []model.ProviderTurn{{TurnIndex: 1, Protocol: modelcap.ProtocolOpenAIResponses, Items: items}}
			switch invalid {
			case "turn_order":
				turns = append(turns, turns[0])
			case "item_order":
				turns[0].Items = append(turns[0].Items, model.ProviderItem{Ordinal: 0, Type: "reasoning", Payload: json.RawMessage(`{"type":"reasoning"}`)})
			case "duplicate_call":
				other := turns[0]
				other.TurnIndex = 2
				turns = append(turns, other)
			case "missing_result":
				turns[0].Items[0].CallID = "not-a-completed-call"
			}
			input := []conversation.Message{{ID: "u", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: "current"}}}}
			defs := []tool.Definition{{QualifiedName: contextmemory.ToolLedgerReadName, InputSchema: json.RawMessage(`{"type":"object"}`)}}
			limits := ContextLimits{EffectiveTokens: 24000, AutoCompactTokens: 21600, AllowProtocolRollover: true}
			b := NewContextBuilder(24000)
			_, _, original := b.buildWithResearchState(context.Background(), input, "", "u", defs, calls, skill.RunContext{}, "", "", "", limits, contextmemory.Checkpoint{}, turns...)
			var budget *contextBudgetError
			if !errors.As(original, &budget) || budget.Budget.Phase != "tool_protocol" {
				t.Fatal("fixture did not hit early normalized budget before native checks", original)
			}
			if _, _, err := b.buildWithToolLedger(context.Background(), input, "", "u", defs, calls, skill.RunContext{}, "", "", "", limits, contextmemory.Checkpoint{}, turns); err == nil {
				t.Fatal("invalid native groups bypassed by normalized ledger fallback")
			}
		})
	}
}
