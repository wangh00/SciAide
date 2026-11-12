package workflow

import (
	"strings"
	"testing"
)

func TestManualStageRetryDoesNotReviveOlderValidationFailure(t *testing.T) {
	for _, code := range []string{"WORKFLOW_AI_MODEL_REFUSED", "MODEL_OUTPUT_REFUSED", "MODEL_UNAVAILABLE", ""} {
		step := Step{ID: "step", Attempt: 3, Input: raw(`{"context":"frozen"}`), InputSHA256: strings.Repeat("a", 64)}
		detail := RunDetail{AIExecutions: []AIExecution{
			{WorkflowStepID: step.ID, Attempt: 2, InputSHA256: step.InputSHA256, ErrorCode: code},
			{WorkflowStepID: step.ID, Attempt: 1, InputSHA256: step.InputSHA256, ErrorCode: "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED", ErrorMessage: "OBSOLETE_QUOTE_ERROR"},
			{WorkflowStepID: "other-step", Attempt: 2, InputSHA256: step.InputSHA256, ErrorCode: "WORKFLOW_AI_OUTPUT_INVALID"},
		}}
		prompt := buildAIStagePrompt(detail, step, CompiledNode{ID: "explore", Prompt: "plan", OutputSchema: raw(`{"type":"object"}`)})
		if strings.Contains(prompt, "OBSOLETE_QUOTE_ERROR") || strings.Contains(prompt, "retried after a host validation rejection") {
			t.Fatal("latest attempt was replaced by an older validation failure", code)
		}
		if refused := code == "WORKFLOW_AI_MODEL_REFUSED" || code == "MODEL_OUTPUT_REFUSED"; refused != strings.Contains(prompt, "explicitly retried this same stage after a model refusal") {
			t.Fatal("manual refusal recovery was not classified", code)
		}
		step.InputSHA256 = "changed"
		if _, ok := latestAIOutputValidationFailure(detail, step); ok {
			t.Fatal("old failure crossed an input boundary")
		}
	}
}
