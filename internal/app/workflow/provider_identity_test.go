package workflow

import (
	"strings"
	"testing"
)

func TestResearchPromptsKeepProviderIdentity(t *testing.T) {
	for name, text := range map[string]string{"opening": researchOpeningRules, "discussion": ResearchDiscussionSystemRules, "execution": ResearchModeSystemRules} {
		t.Run(name, func(t *testing.T) {
			text = strings.ToLower(text)
			if strings.Contains(text, "you are sciaide") || !strings.Contains(text, "identity") || !strings.Contains(text, "provider policies") || !strings.Contains(text, "sciaide") {
				t.Fatal("research prompt confuses application context with model identity")
			}
		})
	}
}
