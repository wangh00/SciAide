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
