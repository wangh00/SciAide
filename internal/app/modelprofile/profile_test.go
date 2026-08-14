package modelprofile

import (
	"testing"

	"github.com/wangh00/SciAide/internal/modelcap"
)

func TestSensitiveCustomHeadersRejected(t *testing.T) {
	for _, name := range []string{"Authorization", "X-Goog-Api-Key", "X-Lab-Token", "X-Client-Secret", "Cookie"} {
		command := SaveCommand{Name: "test", BaseURL: "https://example.test/v1", ModelID: "model", TimeoutSeconds: 60, CustomHeaders: map[string]string{name: "leak"}}
		if err := validateCommand(command); err == nil {
			t.Fatalf("validateCommand() accepted %s header", name)
		}
	}
}

func TestNormalizeModelsPreservesExplicitReasoningCapabilities(t *testing.T) {
	models, _ := normalizeModels([]ProfileModel{{ID: "custom", Enabled: true, ReasoningLevels: []modelcap.ReasoningLevel{modelcap.ReasoningMax}, ReasoningCapabilitySource: "manual"}}, "custom", ProtocolOpenAIChat)
	if len(models) != 1 || len(models[0].ReasoningLevels) != 1 || models[0].ReasoningLevels[0] != modelcap.ReasoningMax || models[0].ReasoningCapabilitySource != "manual" {
		t.Fatalf("models = %#v", models)
	}
}

func TestNormalizeModelsUsesProviderDefaultForFixedReasoningModel(t *testing.T) {
	models, _ := normalizeModels([]ProfileModel{{ID: "deepseek-reasoner", Enabled: true}}, "deepseek-reasoner", ProtocolOpenAIChat)
	if len(models[0].ReasoningLevels) != 0 || models[0].ReasoningCapabilitySource != "unsupported" {
		t.Fatalf("fixed model capabilities = %#v", models[0])
	}
}

func TestReasoningObservationsArePreservedOnlyForSameEndpoint(t *testing.T) {
	existing := []ProfileModel{{
		ID: "future-model", ReasoningVerifiedLevels: []modelcap.ReasoningLevel{modelcap.ReasoningXHigh},
		ReasoningRejectedLevels: []modelcap.ReasoningLevel{modelcap.ReasoningMax}, ReasoningLastRequestedLevel: modelcap.ReasoningMax,
		ReasoningLastResolvedLevel: modelcap.ReasoningXHigh, ReasoningWireMode: "openai_effort",
	}}
	preserved := preserveReasoningObservations([]ProfileModel{{ID: "future-model"}}, existing)
	if len(preserved[0].ReasoningVerifiedLevels) != 1 || len(preserved[0].ReasoningRejectedLevels) != 1 || preserved[0].ReasoningWireMode != "openai_effort" {
		t.Fatalf("preserved = %#v", preserved[0])
	}
	reset := resetReasoningObservations(preserved)
	if len(reset[0].ReasoningVerifiedLevels) != 0 || len(reset[0].ReasoningRejectedLevels) != 0 || reset[0].ReasoningLastRequestedLevel != "" || reset[0].ReasoningLastResolvedLevel != "" || reset[0].ReasoningWireMode != "" {
		t.Fatalf("reset = %#v", reset[0])
	}
}

func TestHeaderNewlinesRejected(t *testing.T) {
	command := SaveCommand{Name: "test", BaseURL: "https://example.test/v1", ModelID: "model", TimeoutSeconds: 60, CustomHeaders: map[string]string{"X-Lab": "ok\r\nX-Leak: true"}}
	if err := validateCommand(command); err == nil {
		t.Fatal("validateCommand() accepted CRLF")
	}
}

func TestBaseURLRejectsEmbeddedCredentials(t *testing.T) {
	if err := validateBaseURL("https://user:password@example.test/v1"); err == nil {
		t.Fatal("validateBaseURL accepted credentials")
	}
}
