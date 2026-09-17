package skill

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestHistoricalRunContextRoundTripAndTamperDetection(t *testing.T) {
	instructions := "Read the immutable evidence."
	digest := sha256.Sum256([]byte(instructions))
	manifest := Manifest{SchemaVersion: CurrentSchemaVersion, ID: "legacy-review", Name: "Legacy review", Version: "1.0.0", Description: "Historical fixture", Entry: "SKILL.md", Activation: Activation{Mode: ActivationExplicit}, Compatibility: Compatibility{SciAide: ">=0.2.0 <1.0.0"}, Context: ContextPolicy{MaxTokens: 1000}}
	value := RunContext{SchemaVersion: LegacyRunContextSchemaVersion, RunID: "run", ProjectID: "project", ContextWindowTokens: 200_000, CatalogBudgetTokens: 4_000, InstructionBudgetTokens: 40_000, Catalog: []RunCatalogSkill{}, SelectionNotices: []RunSkillNotice{}, Skills: []RunSkill{{Manifest: manifest, Priority: 10, Reason: SelectionExplicit, PackagePath: "legacy-review/1.0.0", ManifestHash: strings.Repeat("a", 64), ContentHash: fmt.Sprintf("%x", digest), PackageHash: strings.Repeat("b", 64), Instructions: instructions}}, CreatedAt: time.Now().UTC()}
	encoded, hash, err := EncodeRunContext(value)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRunContext(encoded, hash)
	if err != nil || decoded.Skills[0].Instructions != instructions {
		t.Fatalf("DecodeRunContext = %#v, %v", decoded, err)
	}
	encoded[len(encoded)-2] ^= 1
	if _, err := DecodeRunContext(encoded, hash); err == nil {
		t.Fatal("tampered historical snapshot was accepted")
	}
	messages, err := RenderContextMessages(decoded)
	if err != nil || !strings.Contains(strings.Join(messages, "\n"), instructions) {
		t.Fatalf("RenderContextMessages = %#v, %v", messages, err)
	}
}
