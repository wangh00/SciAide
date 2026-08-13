package modelcap

import "testing"

func TestResolveReasoningLevelFallsDownToNearestSupportedLevel(t *testing.T) {
	supported := []ReasoningLevel{ReasoningMedium, ReasoningHigh, ReasoningXHigh}
	if got := ResolveReasoningLevel(ReasoningMax, supported); got != ReasoningXHigh {
		t.Fatalf("resolved max = %q, want xhigh", got)
	}
	if got := ResolveReasoningLevel(ReasoningHigh, supported); got != ReasoningHigh {
		t.Fatalf("resolved exact high = %q", got)
	}
}

func TestResolveReasoningLevelUsesLowestWhenNoLowerLevelExists(t *testing.T) {
	supported := []ReasoningLevel{ReasoningHigh, ReasoningMax}
	if got := ResolveReasoningLevel(ReasoningLow, supported); got != ReasoningHigh {
		t.Fatalf("resolved low = %q, want high", got)
	}
}

func TestResolveReasoningLevelDisablesUnsupportedModels(t *testing.T) {
	if got := ResolveReasoningLevel(ReasoningMax, nil); got != "" {
		t.Fatalf("resolved unsupported model = %q", got)
	}
}
