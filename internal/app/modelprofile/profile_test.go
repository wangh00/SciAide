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

func TestNormalizeModelsPreservesManualReasoningCapabilities(t *testing.T) {
	models, _ := normalizeModels([]ProfileModel{{ID: "custom", Enabled: true, ReasoningLevels: []modelcap.ReasoningLevel{modelcap.ReasoningMax, modelcap.ReasoningHigh}, ReasoningCapabilitySource: "manual"}}, "custom")
	if len(models) != 1 || len(models[0].ReasoningLevels) != 2 || models[0].ReasoningLevels[0] != modelcap.ReasoningHigh || models[0].ReasoningCapabilitySource != "manual" {
		t.Fatalf("models = %#v", models)
	}
}

func TestNormalizeModelsDoesNotEnableReasoningForUnknownModel(t *testing.T) {
	models, _ := normalizeModels([]ProfileModel{{ID: "private-model", Enabled: true}}, "private-model")
	if len(models[0].ReasoningLevels) != 0 || models[0].ReasoningCapabilitySource != "unsupported" {
		t.Fatalf("unknown model capabilities = %#v", models[0])
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
