package modelcap

import "strings"

const (
	DefaultContextWindowTokens     = 200_000
	DefaultEffectiveContextPercent = 95
	DefaultAutoCompactPercent      = 90
	MinimumContextWindowTokens     = 4_096
	MaximumContextWindowTokens     = 10_000_000
	ContextWindowSourceFallback    = "fallback"
	ContextWindowSourceProvider    = "provider"
	ContextWindowSourceManual      = "manual"
	ContextWindowSourceBuiltin     = "builtin"
)

// ContextBudget separates the provider-advertised window from the smaller
// request budget and automatic compaction threshold used by the agent.
type ContextBudget struct {
	WindowTokens      int    `json:"windowTokens"`
	EffectiveTokens   int    `json:"effectiveTokens"`
	AutoCompactTokens int    `json:"autoCompactTokens"`
	Source            string `json:"source"`
}

func ResolveContextBudget(windowTokens, autoCompactTokens int, source string) ContextBudget {
	if windowTokens < MinimumContextWindowTokens || windowTokens > MaximumContextWindowTokens {
		windowTokens = DefaultContextWindowTokens
		source = ContextWindowSourceFallback
	}
	source = NormalizeContextWindowSource(source)
	if source == "" {
		source = ContextWindowSourceFallback
	}
	effective := windowTokens * DefaultEffectiveContextPercent / 100
	maximumAutoCompact := windowTokens * DefaultAutoCompactPercent / 100
	if autoCompactTokens <= 0 || autoCompactTokens > maximumAutoCompact {
		autoCompactTokens = maximumAutoCompact
	}
	if autoCompactTokens > effective {
		autoCompactTokens = effective
	}
	return ContextBudget{
		WindowTokens:      windowTokens,
		EffectiveTokens:   effective,
		AutoCompactTokens: autoCompactTokens,
		Source:            source,
	}
}

func NormalizeContextWindowSource(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ContextWindowSourceProvider:
		return ContextWindowSourceProvider
	case ContextWindowSourceManual:
		return ContextWindowSourceManual
	case ContextWindowSourceBuiltin:
		return ContextWindowSourceBuiltin
	case ContextWindowSourceFallback:
		return ContextWindowSourceFallback
	default:
		return ""
	}
}
