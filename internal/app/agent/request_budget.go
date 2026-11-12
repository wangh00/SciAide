package agent

import (
	"fmt"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
)

func fitFinalRequest(source model.ChatRequest, calls []tool.Call, observation *chat.ContextUsageObservation, limit int, derive func(model.ChatRequest) model.ChatRequest) (model.ChatRequest, error) {
	request, used, fits := fitDerivedRequest(source, calls, observation, limit, derive)
	if !fits {
		return request, &apperr.Error{Code: "CONTEXT_BUILD_FAILED", UserMessage: "本次输入、输出格式或修正内容超出模型上下文预算；已完成的工具结果和原始要求均保留，可重试当前请求或阶段。", Details: fmt.Sprintf("final request exceeds effective context budget: required=%d available=%d; fixed input, schemas and human answers were not truncated", used, limit)}
	}
	return request, nil
}

func unchangedRequest(request model.ChatRequest) model.ChatRequest { return request }

// Fit the FINAL request, including submission/correction schemas and host
// instructions. Only ordinary persisted tool-result previews are disposable;
// current input, dialogue, human answers, native items and schemas are not.
// No source record is modified and no extra model/tool operation is performed.
func fitDerivedRequest(source model.ChatRequest, calls []tool.Call, observation *chat.ContextUsageObservation, limit int, derive func(model.ChatRequest) model.ChatRequest) (model.ChatRequest, int, bool) {
	request := derive(source)
	used := currentRequestBudgetTokens(request, observation)
	if used <= limit {
		return request, used, true
	}
	ordinary := map[string]bool{}
	for _, call := range calls {
		if call.Result != nil && call.ToolName != userinput.ToolName && call.ProviderCallID != "" {
			ordinary[call.ProviderCallID] = true
		}
	}
	texts := []string{}
	add := func(message model.Message) {
		if message.Role == model.RoleTool && ordinary[message.ToolCallID] {
			texts = append(texts, message.Content)
		}
	}
	for _, message := range source.Messages {
		add(message)
	}
	for _, turn := range source.ProviderTurns {
		for _, message := range turn.ToolResults {
			add(message)
		}
	}
	minimum, total := 0, 0
	for _, text := range texts {
		length := len([]rune(text))
		minimum += min(256, length)
		total += length
	}
	if total <= minimum {
		return request, used, false
	}
	fit := func(budget int) (model.ChatRequest, int) {
		// Reserve a readable, explicitly truncated preview for every call;
		// share the remaining budget rather than emptying older results.
		edges := make([]string, len(texts))
		for i, text := range texts {
			edges[i] = truncateToolContext(text, min(len([]rune(text)), 256))
		}
		fitted := fitToolContexts(texts, budget)
		for i := range fitted {
			if len([]rune(fitted[i])) < len([]rune(edges[i])) {
				fitted[i] = edges[i]
			}
		}
		candidate := source
		candidate.Messages = cloneModelMessages(source.Messages)
		candidate.ProviderTurns = append([]model.ProviderTurn(nil), source.ProviderTurns...)
		index := 0
		replace := func(message *model.Message) {
			if message.Role == model.RoleTool && ordinary[message.ToolCallID] {
				message.Content = fitted[index]
				index++
			}
		}
		for i := range candidate.Messages {
			replace(&candidate.Messages[i])
		}
		for i := range candidate.ProviderTurns {
			candidate.ProviderTurns[i].ToolResults = cloneModelMessages(source.ProviderTurns[i].ToolResults)
			for j := range candidate.ProviderTurns[i].ToolResults {
				replace(&candidate.ProviderTurns[i].ToolResults[j])
			}
		}
		candidate = derive(candidate)
		return candidate, currentRequestBudgetTokens(candidate, observation)
	}
	best, bestUsed := fit(minimum)
	if bestUsed > limit {
		return request, used, false
	}
	low, high := minimum, total
	// Chunk fingerprints are not perfectly monotonic. Only independently
	// checked candidates are admitted; this search is local and bounded.
	for pass := 0; pass < 12 && high-low > 1; pass++ {
		mid := low + (high-low)/2
		candidate, tokens := fit(mid)
		if tokens <= limit {
			best, bestUsed, low = candidate, tokens, mid
		} else {
			high = mid
		}
	}
	return best, bestUsed, true
}
