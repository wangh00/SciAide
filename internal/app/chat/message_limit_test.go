package chat

import (
	"strings"
	"testing"
)

func TestWorkflowPromptHasSeparateBoundWithoutTruncation(t *testing.T) {
	for _, tc := range []struct {
		size     int
		workflow bool
		valid    bool
	}{
		{100000, false, true}, {100001, false, false}, {101532, true, true}, {262144, true, true}, {262145, true, false},
	} {
		text := strings.Repeat("数", tc.size)
		var workflow *WorkflowAIExecution
		if tc.workflow {
			workflow = &WorkflowAIExecution{}
		}
		if err := validateStartMessageLength(text, workflow); (err == nil) != tc.valid {
			t.Fatalf("size=%d workflow=%v err=%v", tc.size, tc.workflow, err)
		}
	}
}

func TestWorkflowPromptUnicodeValidation(t *testing.T) {
	for _, unit := range []string{"a", "数", "🧪", "e\u0301"} {
		text := strings.Repeat(unit, MaxWorkflowPromptChars/len([]rune(unit)))
		if err := ValidateWorkflowPrompt(text); err != nil {
			t.Fatal(err)
		}
		if err := ValidateWorkflowPrompt(text + "a"); err == nil {
			t.Fatal("character bound ignored")
		}
	}
	for _, text := range []string{"", string([]byte{0xff})} {
		if ValidateWorkflowPrompt(text) == nil {
			t.Fatal("invalid prompt accepted")
		}
	}
}
