package chat

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestImplementationSourcePatchIsExactAtomicAndNonCascading(t *testing.T) {
	base := "alpha = '重复'\nbeta = alpha\nresult = beta\nresult"
	patch := ImplementationSourcePatch{BaseCodeSHA256: ImplementationCodeSHA256(base), Replacements: []ImplementationReplacement{
		{Old: "alpha = '重复'", New: "alpha = '保留'"},
		{Old: "beta = alpha", New: "beta = '重复'"},
	}}
	got, err := ApplyImplementationSourcePatch(base, patch)
	if err != nil || got != "alpha = '保留'\nbeta = '重复'\nresult = beta\nresult" {
		t.Fatal(got, err)
	}
	for _, mode := range []string{"wrong_base", "ambiguous", "overlapping", "not_found", "noop", "empty", "too_many", "oversize", "invalid_utf8"} {
		t.Run(mode, func(t *testing.T) {
			p := ImplementationSourcePatch{BaseCodeSHA256: ImplementationCodeSHA256(base), Replacements: []ImplementationReplacement{{Old: "beta = alpha", New: "beta = 1"}}}
			switch mode {
			case "wrong_base":
				p.BaseCodeSHA256 = strings.Repeat("0", 64)
			case "ambiguous":
				p.Replacements[0].Old = "alpha"
			case "overlapping":
				p.Replacements = append(p.Replacements, ImplementationReplacement{Old: "= alpha", New: "= 2"})
			case "not_found":
				p.Replacements[0].Old = "missing_source"
			case "noop":
				p.Replacements[0].New = p.Replacements[0].Old
			case "empty":
				p.Replacements = nil
			case "too_many":
				p.Replacements = make([]ImplementationReplacement, 25)
			case "oversize":
				p.Replacements[0].New = strings.Repeat("x", 120_000)
			case "invalid_utf8":
				p.Replacements[0].New = string([]byte{0xff})
			}
			if got, err := ApplyImplementationSourcePatch(base, p); err == nil || got != "" {
				t.Fatal("partial or invalid patch applied", got, err)
			}
		})
	}
	// Overlapping occurrences must not be mistaken for a unique old substring.
	if _, err := ApplyImplementationSourcePatch("aaa", ImplementationSourcePatch{BaseCodeSHA256: ImplementationCodeSHA256("aaa"), Replacements: []ImplementationReplacement{{Old: "aa", New: "b"}}}); err == nil {
		t.Fatal("overlapping matches accepted")
	}
}

func TestFrozenImplementationPatchBaseKeepsFullMetadataAndChecksInputDigest(t *testing.T) {
	input := json.RawMessage(`{"_pythonRepair":{"priorImplementation":{"methodSummary":"原方法","dependencies":["numpy==2.0"],"analysisInput":{"seed":123},"assumptions":["A"],"outputDeclarations":[],"code":"result = 1\nresult","researchChanges":[{"before":"a","after":"b"}]}}}`)
	c := WorkflowAIExecution{NodeID: "method_implementation", PromptVersion: "impl-parts-v1-impl-patch-v1", Input: input, InputSHA256: ImplementationCodeSHA256(string(input))}
	base, found, err := FrozenImplementationPatchBase(c)
	if err != nil || !found || base.Code != "result = 1\nresult" {
		t.Fatal(base, found, err)
	}
	var metadata map[string]json.RawMessage
	_ = json.Unmarshal(base.Metadata, &metadata)
	if string(metadata["researchChanges"]) != "[]" || string(metadata["dependencies"]) != `["numpy==2.0"]` || string(metadata["analysisInput"]) != `{"seed":123}` || metadata["code"] != nil {
		t.Fatal(string(base.Metadata))
	}
	c.Input = json.RawMessage(`{"_pythonRepair":{}}`)
	if _, _, err := FrozenImplementationPatchBase(c); err == nil {
		t.Fatal("changed input allowed to select a patch baseline")
	}
}
