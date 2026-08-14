package modelcap

import (
	"context"
	"strings"
)

// ReasoningLevel is the provider-independent reasoning preference exposed by
// SciAide. Adapters translate the resolved value into provider-specific request
// fields; the user's requested value is kept separately from the resolved one.
type ReasoningLevel string

const (
	ReasoningLow    ReasoningLevel = "low"
	ReasoningMedium ReasoningLevel = "medium"
	ReasoningHigh   ReasoningLevel = "high"
	ReasoningXHigh  ReasoningLevel = "xhigh"
	ReasoningMax    ReasoningLevel = "max"
)

var orderedReasoningLevels = []ReasoningLevel{
	ReasoningLow,
	ReasoningMedium,
	ReasoningHigh,
	ReasoningXHigh,
	ReasoningMax,
}

// AllReasoningLevels returns SciAide's provider-independent effort ladder.
// Callers receive a copy so capability negotiation cannot mutate the global
// order shared by other conversations.
func AllReasoningLevels() []ReasoningLevel {
	return append([]ReasoningLevel(nil), orderedReasoningLevels...)
}

func (l ReasoningLevel) Valid() bool { return reasoningRank(l) >= 0 }

func NormalizeReasoningLevels(values []ReasoningLevel) []ReasoningLevel {
	seen := make(map[ReasoningLevel]bool, len(values))
	for _, value := range values {
		if value.Valid() {
			seen[value] = true
		}
	}
	result := make([]ReasoningLevel, 0, len(seen))
	for _, value := range orderedReasoningLevels {
		if seen[value] {
			result = append(result, value)
		}
	}
	return result
}

// ResolveReasoningLevel uses the nearest supported level at or below the
// requested level. If no lower level exists, the model's lowest supported
// level is used. An empty result means that reasoning must not be sent.
func ResolveReasoningLevel(requested ReasoningLevel, supported []ReasoningLevel) ReasoningLevel {
	levels := NormalizeReasoningLevels(supported)
	if len(levels) == 0 {
		return ""
	}
	if !requested.Valid() {
		requested = ReasoningMedium
	}
	requestedRank := reasoningRank(requested)
	for index := len(levels) - 1; index >= 0; index-- {
		if reasoningRank(levels[index]) <= requestedRank {
			return levels[index]
		}
	}
	return levels[0]
}

// ReasoningAttempts returns the initial effort followed by progressively
// cheaper values. It is used only after an endpoint explicitly rejects an
// effort value; parameter-level rejection skips directly to provider default.
func ReasoningAttempts(initial ReasoningLevel) []ReasoningLevel {
	if !initial.Valid() {
		return nil
	}
	result := make([]ReasoningLevel, 0, len(orderedReasoningLevels))
	for index := reasoningRank(initial); index >= 0; index-- {
		result = append(result, orderedReasoningLevels[index])
	}
	return result
}

// WithoutRejectedReasoningLevels removes effort values that this exact
// profile/protocol/model tuple has already rejected at runtime.
func WithoutRejectedReasoningLevels(supported, rejected []ReasoningLevel) []ReasoningLevel {
	rejectedSet := make(map[ReasoningLevel]bool, len(rejected))
	for _, level := range rejected {
		if level.Valid() {
			rejectedSet[level] = true
		}
	}
	result := make([]ReasoningLevel, 0, len(supported))
	for _, level := range NormalizeReasoningLevels(supported) {
		if !rejectedSet[level] {
			result = append(result, level)
		}
	}
	return result
}

// ReasoningResult is a successful request-time capability observation. A
// valid Resolved value means the endpoint accepted that effort. An empty
// Resolved value with ControlUnsupported means the endpoint rejected the
// entire optional reasoning/thinking control and provider defaults were used.
type ReasoningResult struct {
	Requested          ReasoningLevel
	Resolved           ReasoningLevel
	Rejected           []ReasoningLevel
	ControlUnsupported bool
	WireMode           string
}

// ReasoningRecorder persists runtime negotiation without coupling provider
// adapters to SQLite or the model-profile application service.
type ReasoningRecorder interface {
	RecordReasoningResult(ctx context.Context, profileID, modelID string, result ReasoningResult) error
}

// InferredReasoningLevels keeps the legacy OpenAI-compatible inference entry
// point. New code should use InferredReasoningLevelsForProtocol so adapters can
// expose one stable five-step preference while still mapping it to the subset
// understood by the selected protocol/model family.
func InferredReasoningLevels(modelID string) []ReasoningLevel {
	return InferredReasoningLevelsForProtocol(ProtocolOpenAIChat, modelID)
}

// InferredReasoningLevelsForProtocol returns adjustable reasoning tiers from
// weakest to strongest. An empty result does not mean that the model cannot
// reason: it means SciAide must omit an explicit effort parameter and let the
// provider/model use its native default (for example a fixed-thinking model).
//
// /v1/models rarely publishes this metadata. These defaults deliberately
// mirror provider variants for known model families. Unknown compatible
// models receive the complete ladder: future models must be allowed to try
// max instead of being permanently capped at high by an old client. Protocol
// adapters negotiate downward only after an explicit request-time rejection.
func InferredReasoningLevelsForProtocol(protocol APIProtocol, modelID string) []ReasoningLevel {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return nil
	}
	if protocol == ProtocolAnthropic {
		// Extended/adaptive thinking is available on Claude 3.7 and the Claude
		// 4 family. Budget-based Anthropic transports can represent all five
		// SciAide preferences even when the provider only exposes token budgets.
		if strings.Contains(id, "claude-3-7") || strings.Contains(id, "claude-3.7") ||
			strings.Contains(id, "claude-4") || strings.Contains(id, "claude-opus-4") ||
			strings.Contains(id, "claude-sonnet-4") || strings.Contains(id, "claude-haiku-4") {
			return append([]ReasoningLevel(nil), orderedReasoningLevels...)
		}
		// Custom Anthropic-compatible model IDs are optimistic; a provider that
		// does not implement `thinking` is handled by the adapter fallback.
		if !strings.Contains(id, "claude-") {
			return append([]ReasoningLevel(nil), orderedReasoningLevels...)
		}
		return nil
	}

	// Models with fixed/native thinking do not need an effort field. Keeping
	// the supported list empty lets them follow their own always-thinking mode.
	if strings.Contains(id, "deepseek-reasoner") || strings.Contains(id, "deepseek-r1") ||
		strings.Contains(id, "kimi") && strings.Contains(id, "thinking") {
		return nil
	}
	// Clearly non-chat/non-reasoning OpenAI families should not pay a failed
	// probe on every request.
	if strings.Contains(id, "embedding") || strings.Contains(id, "whisper") ||
		strings.Contains(id, "tts") || strings.Contains(id, "dall-e") ||
		strings.Contains(id, "gpt-3.5") || strings.Contains(id, "gpt-4o") ||
		strings.Contains(id, "gpt-4.1") {
		return nil
	}
	switch {
	case strings.HasPrefix(id, "o1"):
		return []ReasoningLevel{ReasoningMedium, ReasoningHigh}
	case strings.HasPrefix(id, "o3"), strings.HasPrefix(id, "o4"):
		return []ReasoningLevel{ReasoningLow, ReasoningMedium, ReasoningHigh}
	case strings.Contains(id, "gpt-5"):
		if strings.Contains(id, "-chat") {
			return []ReasoningLevel{ReasoningMedium}
		}
		if strings.Contains(id, "-pro") && !strings.Contains(id, "5.2") && !strings.Contains(id, "5.3") && !strings.Contains(id, "5.4") {
			return []ReasoningLevel{ReasoningHigh}
		}
		if strings.Contains(id, "5.2") || strings.Contains(id, "5.3") || strings.Contains(id, "5.4") || strings.Contains(id, "codex-max") {
			return []ReasoningLevel{ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningXHigh}
		}
		return []ReasoningLevel{ReasoningLow, ReasoningMedium, ReasoningHigh}
	case strings.Contains(id, "grok-3-mini"):
		return []ReasoningLevel{ReasoningLow, ReasoningHigh}
	case strings.Contains(id, "deepseek-v4"):
		return []ReasoningLevel{ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningMax}
	default:
		return AllReasoningLevels()
	}
}

func reasoningRank(value ReasoningLevel) int {
	for index, candidate := range orderedReasoningLevels {
		if value == candidate {
			return index
		}
	}
	return -1
}
