package workflow

import (
	"strings"
	"testing"
)

func TestAutomaticPythonRepairContextSurvivesFailedSubmissionRetryButNotCompletedReplacement(t *testing.T) {
	producer := Step{ID: "producer", NodeID: "method_implementation", Status: StepQueued, Attempt: 5}
	detail := RunDetail{Steps: []Step{producer}, Events: []RuntimeEvent{{Type: "workflow.upstream_revision_queued", Payload: raw(`{"producerStepId":"producer","producerNodeId":"method_implementation","failedStepId":"python","failedNodeId":"python_analysis","failureSummary":"KeyError: label_counts","toolCallId":"call","automatic":true,"nextProducerAttempt":5,"priorImplementation":{"code":"original failed source"}}`)}}}
	for _, status := range []string{"failed", "cancelled", "interrupted", "completed", "running"} {
		t.Run(status, func(t *testing.T) {
			d := detail
			d.AIExecutions = []AIExecution{{WorkflowStepID: "producer", Attempt: 5, Status: status}}
			repair, found := pendingAutomaticPythonRepair(d, "method_implementation")
			want := status == "failed" || status == "cancelled" || status == "interrupted"
			if found != want || found && !strings.Contains(string(repair.PriorImplementation), "original failed source") {
				t.Fatal(repair, found)
			}
		})
	}
	for _, eventType := range []string{"workflow.user_revision_queued", "workflow.review_revision_queued"} {
		d := detail
		d.Events = append(append([]RuntimeEvent(nil), detail.Events...), RuntimeEvent{Type: eventType, Payload: raw(`{}`)})
		if _, found := pendingAutomaticPythonRepair(d, "method_implementation"); found {
			t.Fatal("old source crossed explicit newer revision")
		}
	}
}

func TestAutomaticPythonRepairContextAcrossSeveralInterruptedAttemptsUsesOnlyLatestSource(t *testing.T) {
	detail := RunDetail{
		Steps:  []Step{{ID: "producer", NodeID: "method_implementation", Status: StepQueued, Attempt: 7}},
		Events: []RuntimeEvent{{Type: "workflow.upstream_revision_queued", Payload: raw(`{"producerStepId":"producer","producerNodeId":"method_implementation","automatic":true,"nextProducerAttempt":5,"priorImplementation":{"code":"latest failed source"}}`)}},
		AIExecutions: []AIExecution{
			{WorkflowStepID: "producer", Attempt: 5, Status: "failed"},
			{WorkflowStepID: "producer", Attempt: 6, Status: "cancelled"},
			{WorkflowStepID: "producer", Attempt: 7, Status: "interrupted"},
		},
	}
	if repair, found := pendingAutomaticPythonRepair(detail, "method_implementation"); !found || !strings.Contains(string(repair.PriorImplementation), "latest failed source") {
		t.Fatal("lost baseline over multiple failed format attempts", repair, found)
	}
	for index := range detail.AIExecutions {
		d := detail
		d.AIExecutions = append([]AIExecution(nil), detail.AIExecutions...)
		d.AIExecutions[index].Status = "completed"
		if _, found := pendingAutomaticPythonRepair(d, "method_implementation"); found {
			t.Fatal("accepted replacement resurrected an older failed source", index)
		}
	}
}
