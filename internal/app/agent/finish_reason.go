package agent

import (
	"errors"
	"strings"

	"github.com/wangh00/SciAide/internal/apperr"
)

func retryableModelTurnTerminalError(turn modelTurn) error {
	err := validateModelTurnTerminal(turn)
	if err == nil {
		return nil
	}
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || (appErr.Code != "MODEL_RESPONSE_EMPTY" && appErr.Code != "MODEL_RESPONSE_INCOMPLETE") {
		return err
	}
	copy := *appErr
	copy.Retryable = true
	return &copy
}

func validateModelTurnTerminal(turn modelTurn) error {
	reason := strings.ToLower(strings.TrimSpace(turn.finishReason))
	if len(turn.toolCalls) > 0 {
		if reason == "tool_calls" || reason == "tool_use" {
			return nil
		}
		return &apperr.Error{Code: "MODEL_TOOL_CALL_INVALID", UserMessage: "模型在非工具终止状态中返回了工具调用。"}
	}

	switch reason {
	case "tool_calls", "tool_use", "requires_action":
		return &apperr.Error{Code: "MODEL_TOOL_CALL_INVALID", UserMessage: "模型声明调用工具但没有给出有效调用。"}
	case "length", "max_tokens", "max_output_tokens":
		return &apperr.Error{Code: "MODEL_OUTPUT_TRUNCATED", UserMessage: "模型回答达到输出上限，已保留未完成草稿但不会标记为完成。"}
	case "content_filter", "safety":
		return &apperr.Error{Code: "MODEL_OUTPUT_FILTERED", UserMessage: "模型回答被服务端过滤，未标记为完成。"}
	case "incomplete", "pause_turn", "", "error", "failed", "cancelled", "canceled":
		return &apperr.Error{Code: "MODEL_RESPONSE_INCOMPLETE", UserMessage: "模型响应没有完整结束，已保留本轮草稿。"}
	case "refusal":
		// Preserve a provider-visible refusal exactly as returned. An empty refusal
		// item is protocol state, not a user-facing answer.
		if visibleModelText(turn.text) == "" {
			return &apperr.Error{Code: "MODEL_RESPONSE_EMPTY", UserMessage: "模型没有返回可展示的回答。"}
		}
		return nil
	case "stop", "completed", "end_turn", "stop_sequence":
		if visibleModelText(turn.text) == "" {
			return &apperr.Error{Code: "MODEL_RESPONSE_EMPTY", UserMessage: "模型没有返回可展示的回答。"}
		}
		return nil
	default:
		return &apperr.Error{Code: "MODEL_RESPONSE_INCOMPLETE", UserMessage: "模型返回了无法确认的结束状态，已保留本轮草稿。"}
	}
}

func validateCheckpointFinish(reason string) error {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "stop", "completed", "end_turn", "stop_sequence":
		return nil
	case "length", "max_tokens", "max_output_tokens":
		return &apperr.Error{Code: "CONTEXT_CHECKPOINT_TRUNCATED", UserMessage: "上下文摘要达到输出上限，未替换原历史。"}
	default:
		return &apperr.Error{Code: "CONTEXT_CHECKPOINT_INCOMPLETE", UserMessage: "上下文摘要没有完整结束，未替换原历史。"}
	}
}
