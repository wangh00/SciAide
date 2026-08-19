package modelcap

import "testing"

func TestResolveContextBudgetUsesConservativeFallback(t *testing.T) {
	budget := ResolveContextBudget(0, 0, "")
	if budget.WindowTokens != 200_000 || budget.EffectiveTokens != 190_000 || budget.AutoCompactTokens != 180_000 || budget.Source != ContextWindowSourceFallback {
		t.Fatalf("fallback budget = %#v", budget)
	}
}

func TestResolveContextBudgetClampsProviderThreshold(t *testing.T) {
	budget := ResolveContextBudget(128_000, 127_000, ContextWindowSourceProvider)
	if budget.EffectiveTokens != 121_600 || budget.AutoCompactTokens != 115_200 || budget.Source != ContextWindowSourceProvider {
		t.Fatalf("provider budget = %#v", budget)
	}
}
