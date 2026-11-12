package tool

import (
	"context"
	"testing"
)

func TestPendingRevisionProposeDoesNotAcquireVersion8WriteIsolation(t *testing.T) {
	for _, version := range []string{"6", "7"} {
		t.Run(version, func(t *testing.T) {
			definition := executorDefinition()
			definition.QualifiedName, definition.Version = "builtin.research.revision.propose", "8"
			invoked := false
			implementation := executorFixtureTool{definition: definition, invoke: func(context.Context, Invocation) (Result, error) {
				invoked = true
				return Result{Status: ResultSuccess}, nil
			}}
			executor, repository, call := readyExecutor(t, implementation, ExecutorOptions{})
			legacy := definition
			legacy.Version = version
			frozen := repository.calls[call.ID]
			frozen.ToolVersion, frozen.ContractSHA256 = version, DefinitionFingerprint(legacy)
			repository.calls[call.ID] = frozen
			execution, err := executor.Execute(context.Background(), "project", call.ID)
			if err != nil || invoked || execution.ErrorCode != ErrorCodeInvocationFailed || repository.calls[call.ID].Status != CallFailed {
				t.Fatal("old proposal call was silently upgraded or executed", execution, err, invoked)
			}
		})
	}
}
