package tool

import (
	"context"
	"fmt"
	"testing"
)

func TestBusinessRejectionIsNotInvocationFailure(t *testing.T) {
	impl := executorFixtureTool{definition: executorDefinition(), invoke: func(context.Context, Invocation) (Result, error) {
		return Result{}, fmt.Errorf("wrapped: %w", BusinessDecisionError{Code: "RESEARCH_REVIEW_NOT_APPROVED", Message: "报告仍有问题，等待返修"})
	}}
	e, repo, call := readyExecutor(t, impl, ExecutorOptions{})
	execution, err := e.Execute(context.Background(), "project", call.ID)
	if err != nil || execution.ErrorCode != "RESEARCH_REVIEW_NOT_APPROVED" || repo.calls[call.ID].ErrorCode == ErrorCodeInvocationFailed {
		t.Fatal(execution, err)
	}
	if repo.calls[call.ID].Status != CallFailed || repo.calls[call.ID].ErrorMessage != "报告仍有问题，等待返修" {
		t.Fatal("scientific rejection was not retained")
	}
}
