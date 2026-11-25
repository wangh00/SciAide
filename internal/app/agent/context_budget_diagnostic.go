package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/model"
)

// Counts only, never prompts, schema contents, source text or private names.
// Character estimates, measured anchors and the provider's window are NOT
// interchangeable token measurements; the source/unit stay explicit.
type contextBudgetDiagnostic struct {
	Phase               string `json:"phase"`
	Source              string `json:"estimation_source"`
	Unit                string `json:"unit"`
	ComponentUnit       string `json:"component_unit"`
	Effective           int    `json:"effective_budget"`
	Available           int    `json:"available_context_budget"`
	Auto                int    `json:"auto_compact_limit"`
	Required            int    `json:"required_estimate"`
	System              int    `json:"fixed_system_estimate,omitempty"`
	Definitions         int    `json:"tool_definitions_estimate,omitempty"`
	DefinitionCount     int    `json:"tool_definition_count,omitempty"`
	OtherFixed          int    `json:"other_fixed_estimate,omitempty"`
	Current             int    `json:"current_input_estimate"`
	CurrentIsolated     bool   `json:"current_input_isolated"`
	UserMessages        int    `json:"user_messages_estimate,omitempty"`
	Protocol            int    `json:"tool_protocol_estimate,omitempty"`
	Reserved            int    `json:"reserved_evidence_estimate,omitempty"`
	CompletedCalls      int    `json:"completed_call_count,omitempty"`
	ObservedReserve     int    `json:"observed_overflow_reserve,omitempty"`
	DeferredDefinitions int    `json:"deferred_builtin_count,omitempty"`
}

type contextBudgetError struct {
	reason string
	Budget contextBudgetDiagnostic
}

func (e *contextBudgetError) Error() string {
	raw, _ := json.Marshal(e.Budget)
	return e.reason + "; budget=" + string(raw)
}
func (e *contextBudgetError) userMessage() string {
	source := "保守字符估算，并非实际 token 用量"
	if e.Budget.Source != "character_fallback" {
		source = "usage 锚点及新增内容预留，并非本次实际 token 用量"
	}
	input := fmt.Sprintf("当前输入字符估算 %d", e.Budget.Current)
	if !e.Budget.CurrentIsolated {
		input = "当前输入未单独计量"
		if e.Budget.UserMessages > 0 {
			input = fmt.Sprintf("用户消息合计 %d（含历史/宿主补充）", e.Budget.UserMessages)
		}
	}
	return fmt.Sprintf("上下文预算不足，已在模型调用前停止。来源：%s；有效预算 %d，可用于当前上下文 %d，已知所需估算 %d；工具定义字符估算 %d，%s，完成调用回放预留 %d。原输入和已完成结果保留，未自动扩窗或重试。", source, e.Budget.Effective, e.Budget.Available, e.Budget.Required, e.Budget.Definitions, input, e.Budget.Reserved)
}

func toolDefinitionEstimate(d model.ToolDefinition) int {
	return utf8.RuneCountInString(d.Name) + utf8.RuneCountInString(d.Description) + utf8.RuneCount(d.InputSchema)
}

func fixedContextBudgetError(request model.ChatRequest, limits ContextLimits, base, current, protocol int, phase, reason string) error {
	d := contextBudgetDiagnostic{Phase: phase, Source: "character_fallback", Unit: "estimate_units_not_actual_tokens", Effective: limits.EffectiveTokens, Available: limits.EffectiveTokens, Auto: limits.AutoCompactTokens, Required: base + current + protocol, Current: current, Protocol: protocol, DefinitionCount: len(request.Tools)}
	d.CurrentIsolated = true
	d.ComponentUnit = "unicode_character_estimate"
	for _, m := range request.Messages {
		if m.Role == model.RoleSystem {
			d.System += estimateMessageTokens(m)
		}
	}
	for _, t := range request.Tools {
		d.Definitions += toolDefinitionEstimate(t)
	}
	d.OtherFixed = max(0, base-d.System-d.Definitions)
	return &contextBudgetError{reason: reason, Budget: d}
}

func withCompletedBudgetReservation(err error, limits ContextLimits, calls, reserved int) error {
	var prior *contextBudgetError
	if !errors.As(err, &prior) {
		return err
	}
	value := *prior
	value.Budget.Phase = "completed_evidence"
	value.Budget.Effective = limits.EffectiveTokens
	value.Budget.Auto = limits.AutoCompactTokens
	value.Budget.Available = max(0, limits.EffectiveTokens-reserved-value.Budget.ObservedReserve)
	value.Budget.Required += reserved
	value.Budget.Reserved += reserved
	value.Budget.CompletedCalls = calls
	return &value
}

func completedBudgetReservationError(reason string, limits ContextLimits, calls, reserved int) error {
	return &contextBudgetError{reason: reason, Budget: contextBudgetDiagnostic{Phase: "completed_evidence", Source: "character_fallback", Unit: "estimate_units_not_actual_tokens", ComponentUnit: "unicode_character_estimate", Effective: limits.EffectiveTokens, Available: max(0, limits.EffectiveTokens-reserved), Auto: limits.AutoCompactTokens, Required: reserved, Reserved: reserved, CompletedCalls: calls}}
}

func withObservedBudgetReservation(err error, limits ContextLimits, reserved int) error {
	var prior *contextBudgetError
	if !errors.As(err, &prior) {
		return err
	}
	copy := *prior
	copy.Budget.Source = "observed_input_overflow_reserve"
	copy.Budget.Effective = limits.EffectiveTokens
	copy.Budget.Auto = limits.AutoCompactTokens
	copy.Budget.Available = max(0, limits.EffectiveTokens-reserved-copy.Budget.Reserved)
	copy.Budget.Required += reserved
	copy.Budget.ObservedReserve = reserved
	copy.Budget.ComponentUnit = "unicode_character_footprint_not_additive_to_usage_estimate"
	return &copy
}

func finalContextBudgetError(request model.ChatRequest, observation *chat.ContextUsageObservation, limit, used int) *contextBudgetError {
	e := fixedContextBudgetError(request, ContextLimits{EffectiveTokens: limit}, 0, 0, 0, "final_request", "final request exceeds effective context budget; fixed input, schemas and human answers were not truncated").(*contextBudgetError)
	e.Budget.Required = used
	e.Budget.CurrentIsolated = false
	for _, m := range request.Messages {
		if m.Role == model.RoleUser {
			e.Budget.UserMessages += estimateMessageTokens(m)
		}
	}
	if observation != nil {
		if _, matched := observedRequestEstimate(request, *observation); matched {
			e.Budget.Source = "observed_input_with_growth_reserve"
			e.Budget.ComponentUnit = "unicode_character_footprint_not_additive_to_usage_estimate"
		}
	}
	return e
}
