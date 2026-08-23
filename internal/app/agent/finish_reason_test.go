package agent

import (
	"errors"
	"testing"

	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
)

func TestValidateModelTurnTerminalRejectsIncompleteOutcomes(t *testing.T) {
	for _, reason := range []string{"", "length", "content_filter", "incomplete", "unknown"} {
		if err := validateModelTurnTerminal(modelTurn{finishReason: reason, text: "partial"}); err == nil {
			t.Fatalf("finish reason %q was accepted", reason)
		}
	}
}

func TestValidateModelTurnTerminalKeepsRefusalText(t *testing.T) {
	if err := validateModelTurnTerminal(modelTurn{finishReason: "refusal", text: "I cannot help with that."}); err != nil {
		t.Fatalf("text refusal rejected: %v", err)
	}
}

func TestValidateModelTurnTerminalRequiresToolPair(t *testing.T) {
	if err := validateModelTurnTerminal(modelTurn{finishReason: "tool_calls"}); err == nil {
		t.Fatal("tool finish without calls was accepted")
	}
	if err := validateModelTurnTerminal(modelTurn{finishReason: "tool_calls", toolCalls: []model.ToolCall{{ID: "call", Name: "tool"}}}); err != nil {
		t.Fatalf("valid tool terminal rejected: %v", err)
	}
}

func TestRetryableModelTurnTerminalErrorOnlyRetriesAmbiguousTerminal(t *testing.T) {
	for _, turn := range []modelTurn{
		{finishReason: "", text: "partial"},
		{finishReason: "stop"},
	} {
		err := retryableModelTurnTerminalError(turn)
		var appErr *apperr.Error
		if !errors.As(err, &appErr) || !appErr.Retryable {
			t.Fatalf("turn %#v error = %#v, want retryable", turn, err)
		}
	}
	for _, reason := range []string{"length", "content_filter", "tool_calls"} {
		err := retryableModelTurnTerminalError(modelTurn{finishReason: reason, text: "partial"})
		var appErr *apperr.Error
		if !errors.As(err, &appErr) || appErr.Retryable {
			t.Fatalf("finish reason %q error = %#v, want permanent", reason, err)
		}
	}
}
