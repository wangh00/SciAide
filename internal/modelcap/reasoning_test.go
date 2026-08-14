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

func TestReasoningAttemptsDescendOneTierAtATime(t *testing.T) {
	want := []ReasoningLevel{ReasoningMax, ReasoningXHigh, ReasoningHigh, ReasoningMedium, ReasoningLow}
	got := ReasoningAttempts(ReasoningMax)
	if len(got) != len(want) {
		t.Fatalf("attempts = %#v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("attempts = %#v", got)
		}
	}
}

func TestProtocolReasoningInferenceMirrorsProviderVariants(t *testing.T) {
	tests := []struct {
		name     string
		protocol APIProtocol
		model    string
		want     []ReasoningLevel
	}{
		{name: "new GPT supports xhigh", protocol: ProtocolOpenAIResponses, model: "gpt-5.4", want: []ReasoningLevel{ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningXHigh}},
		{name: "Claude thinking budget maps five tiers", protocol: ProtocolAnthropic, model: "claude-sonnet-4-5", want: orderedReasoningLevels},
		{name: "fixed reasoning uses provider default", protocol: ProtocolOpenAIChat, model: "deepseek-reasoner", want: nil},
		{name: "DeepSeek v4 supports max", protocol: ProtocolOpenAIChat, model: "deepseek-v4-flash", want: []ReasoningLevel{ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningMax}},
		{name: "custom compatible optimistically tries full ladder", protocol: ProtocolOpenAIChat, model: "lab-model", want: orderedReasoningLevels},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InferredReasoningLevelsForProtocol(tt.protocol, tt.model)
			if len(got) != len(tt.want) {
				t.Fatalf("levels = %#v, want %#v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("levels = %#v, want %#v", got, tt.want)
				}
			}
		})
	}
}
