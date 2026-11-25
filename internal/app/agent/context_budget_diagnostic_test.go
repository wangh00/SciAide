package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestContextBudgetFailureReportsFixedPartsWithoutContent(t *testing.T) {
	text := "PRIVATE_CURRENT_INPUT"
	_, _, err := NewContextBuilder(2000).BuildWithInfo(context.Background(), []conversation.Message{{ID: "u", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: text}}}}, "", []tool.Definition{{QualifiedName: "builtin.fixture", Description: strings.Repeat("D", 3000), InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil)
	if err == nil || !strings.Contains(err.Error(), `"phase":"fixed_input"`) || !strings.Contains(err.Error(), `"estimation_source":"character_fallback"`) || !strings.Contains(err.Error(), `"tool_definitions_estimate":`) || !strings.Contains(err.Error(), `"current_input_estimate":21`) {
		t.Fatal("fixed-budget failure has no actionable component breakdown", err)
	}
	if strings.Contains(err.Error(), text) || strings.Contains(err.Error(), strings.Repeat("D", 40)) {
		t.Fatal("budget diagnostic exposed input/definition text")
	}
}

func TestContextBudgetDiagnosticAddsIndependentReservationsWithoutDoubleCounting(t *testing.T) {
	base := &contextBudgetError{reason: "fixed", Budget: contextBudgetDiagnostic{Phase: "fixed_input", Source: "character_fallback", Required: 8500, CurrentIsolated: true}}
	limits := ContextLimits{EffectiveTokens: 10000, AutoCompactTokens: 9000}
	for _, err := range []error{withObservedBudgetReservation(withCompletedBudgetReservation(base, ContextLimits{EffectiveTokens: 8000, AutoCompactTokens: 7000}, 3, 1000), limits, 2000), withCompletedBudgetReservation(withObservedBudgetReservation(base, ContextLimits{EffectiveTokens: 9000, AutoCompactTokens: 8000}, 2000), limits, 3, 1000)} {
		var d *contextBudgetError
		if !errors.As(err, &d) || d.Budget.Available != 7000 || d.Budget.Effective != 10000 || d.Budget.Required != 11500 || d.Budget.Reserved != 1000 || d.Budget.ObservedReserve != 2000 {
			t.Fatal("reservation breakdown is misleading", err)
		}
	}
	if base.Budget.Required != 8500 || base.Budget.Reserved != 0 {
		t.Fatal("diagnostic annotation mutated earlier evidence")
	}
}

func TestContextBudgetFailureExplainsCompletedReplayReservation(t *testing.T) {
	calls := []tool.Call{}
	for i := 0; i < 12; i++ {
		calls = append(calls, tool.Call{ProviderCallID: strings.Repeat("c", i+1), ToolName: "builtin.fixture", Arguments: json.RawMessage(`{"field":"` + strings.Repeat("A", 128) + `"}`), Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Text: strings.Repeat("PRIVATE_RESULT", 100)}})
	}
	_, _, err := NewContextBuilder(7000).buildWithProtocolEvidence(context.Background(), []conversation.Message{{ID: "u", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: "read"}}}}, "", "u", nil, calls, skill.RunContext{}, "", "", "", ContextLimits{EffectiveTokens: 7000, AutoCompactTokens: 6300}, contextmemory.Checkpoint{}, nil)
	if err == nil || !strings.Contains(err.Error(), `"phase":"completed_evidence"`) || !strings.Contains(err.Error(), `"completed_call_count":12`) || !strings.Contains(err.Error(), `"reserved_evidence_estimate":`) {
		t.Fatal("replay reservation is hidden behind a misleading fixed-input error", err)
	}
	if strings.Contains(err.Error(), "PRIVATE_RESULT") {
		t.Fatal("diagnostic exposed saved source content")
	}
}
