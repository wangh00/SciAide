package workflow

import (
	"testing"
)

func TestResearchExecutionRequiresCurrentCode(t *testing.T) {
	for _, tc := range []struct {
		name, code, status, hash string
		valid                    bool
	}{
		{"matching", "result = {'rows': 2}", "success", hashBytes([]byte("result = {'rows': 2}")), true},
		{"whitespace", "\nresult = {'rows': 2}\n", "success", hashBytes([]byte("result = {'rows': 2}")), true},
		{"old computation", "result = {'rows': 3}", "success", hashBytes([]byte("result = {'rows': 2}")), false},
		{"failed", "result = {}", "error", hashBytes([]byte("result = {}")), false},
		{"missing hash", "result = {}", "success", "", false},
		{"missing code", "", "success", hashBytes(nil), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := rawObject(map[string]any{"implementationContext": map[string]any{"code": tc.code}, "computedResults": map[string]any{"status": tc.status, "codeSha256": tc.hash}})
			if err := validateResearchExecutionConsistency(input); (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if _, err := bindNodeInput(RunDetail{}, CompiledNode{Kind: NodeAgentStage, Arguments: input}); (err == nil) != tc.valid {
				t.Fatalf("binding valid=%v err=%v", tc.valid, err)
			}
		})
	}
	if err := validateResearchExecutionConsistency(raw(`{"methodContext":{},"context":{}}`)); err != nil {
		t.Fatalf("non-computational route: %v", err)
	}
}
