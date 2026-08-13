package modelcap

import "strings"

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

// InferredReasoningLevels is deliberately conservative. /v1/models usually
// does not publish reasoning metadata, so uncertain models stay disabled until
// the user explicitly selects their supported levels in model settings.
func InferredReasoningLevels(modelID string) []ReasoningLevel {
	id := strings.ToLower(strings.TrimSpace(modelID))
	switch {
	case strings.HasPrefix(id, "o1"):
		return []ReasoningLevel{ReasoningMedium, ReasoningHigh}
	case strings.HasPrefix(id, "o3"), strings.HasPrefix(id, "o4"):
		return []ReasoningLevel{ReasoningLow, ReasoningMedium, ReasoningHigh}
	case strings.Contains(id, "gpt-5"):
		return []ReasoningLevel{ReasoningLow, ReasoningMedium, ReasoningHigh}
	default:
		return nil
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
