package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type executorFixtureTool struct {
	definition Definition
	invoke     func(context.Context, Invocation) (Result, error)
}

type runProjectFixture struct{ projectID string }

func (r runProjectFixture) ProjectIDForRun(context.Context, string) (string, error) {
	return r.projectID, nil
}

func (t executorFixtureTool) Definition(context.Context) (Definition, error) {
	return t.definition, nil
}
func (t executorFixtureTool) Invoke(ctx context.Context, invocation Invocation) (Result, error) {
	return t.invoke(ctx, invocation)
}

func executorDefinition() Definition {
	return Definition{QualifiedName: "builtin.test", Description: "executor fixture", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: RiskLow, Permissions: []PermissionRequirement{{Kind: PermissionWorkspaceRead, Resource: "."}}, Idempotent: true, Version: "1"}
}

func readyExecutor(t *testing.T, implementation Tool, options ExecutorOptions) (*Executor, *memoryToolRepository, Call) {
	t.Helper()
	ctx := context.Background()
	registry := NewRegistry()
	if err := registry.Register(ctx, implementation); err != nil {
		t.Fatal(err)
	}
	repository := newMemoryToolRepository()
	service := NewService(repository, JSONSchemaValidator{})
	definition, _ := implementation.Definition(ctx)
	call, err := service.Propose(ctx, definition, CreateCommand{RunID: "run", ProviderCallID: "provider", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	call, err = service.Start(ctx, call.ID)
	if err != nil {
		t.Fatal(err)
	}
	return NewExecutor(registry, service, runProjectFixture{projectID: "project"}, options), repository, call
}

func TestExecutorCompletesAndPersistsBoundedResult(t *testing.T) {
	definition := executorDefinition()
	implementation := executorFixtureTool{definition: definition, invoke: func(_ context.Context, invocation Invocation) (Result, error) {
		if invocation.ProjectID != "project" || invocation.CallID == "" {
			t.Fatalf("invocation = %#v", invocation)
		}
		return Result{Status: ResultSuccess, Text: "科研结果", Structured: json.RawMessage(`{"ok":true}`)}, nil
	}}
	executor, repository, call := readyExecutor(t, implementation, ExecutorOptions{})
	execution, err := executor.Execute(context.Background(), "project", call.ID)
	if err != nil || execution.Result.Status != ResultSuccess || execution.Result.Text != "科研结果" || execution.ErrorCode != "" {
		t.Fatalf("Execute() = %#v, %v", execution, err)
	}
	loaded := repository.calls[call.ID]
	if loaded.Status != CallCompleted || loaded.Result == nil || loaded.Result.Meta.DurationMillis < 0 {
		t.Fatalf("persisted call = %#v", loaded)
	}
}

func TestExecutorContainsPanicAndDoesNotLeakDetails(t *testing.T) {
	implementation := executorFixtureTool{definition: executorDefinition(), invoke: func(context.Context, Invocation) (Result, error) {
		panic("secret panic details")
	}}
	executor, repository, call := readyExecutor(t, implementation, ExecutorOptions{})
	execution, err := executor.Execute(context.Background(), "project", call.ID)
	if err != nil || execution.ErrorCode != ErrorCodePanic || execution.Result.Status != ResultError || strings.Contains(execution.Result.Text, "secret") {
		t.Fatalf("Execute(panic) = %#v, %v", execution, err)
	}
	if repository.calls[call.ID].Status != CallFailed {
		t.Fatalf("call status = %s", repository.calls[call.ID].Status)
	}
}

func TestExecutorTimeoutAndExplicitCancellation(t *testing.T) {
	for _, test := range []struct {
		name       string
		timeout    time.Duration
		cancel     bool
		wantCode   string
		wantStatus CallStatus
	}{
		{"timeout", 15 * time.Millisecond, false, ErrorCodeTimeout, CallFailed},
		{"cancel", time.Second, true, ErrorCodeCancelled, CallCancelled},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{})
			implementation := executorFixtureTool{definition: executorDefinition(), invoke: func(ctx context.Context, _ Invocation) (Result, error) {
				close(started)
				<-ctx.Done()
				return Result{}, ctx.Err()
			}}
			executor, repository, call := readyExecutor(t, implementation, ExecutorOptions{InvocationTimeout: test.timeout})
			type outcome struct {
				value Execution
				err   error
			}
			done := make(chan outcome, 1)
			go func() {
				value, err := executor.Execute(context.Background(), "project", call.ID)
				done <- outcome{value, err}
			}()
			<-started
			if test.cancel && !executor.Cancel(call.ID) {
				t.Fatal("Cancel() did not find active call")
			}
			result := <-done
			if result.err != nil || result.value.ErrorCode != test.wantCode || repository.calls[call.ID].Status != test.wantStatus {
				t.Fatalf("result = %#v, %v; call=%#v", result.value, result.err, repository.calls[call.ID])
			}
		})
	}
}

func TestExecutorLimitsTextAndRejectsOversizedStructuredResult(t *testing.T) {
	for _, test := range []struct {
		name     string
		result   Result
		wantCode string
		status   CallStatus
	}{
		{"text", Result{Status: ResultSuccess, Text: "研究结果很长"}, "", CallCompleted},
		{"json", Result{Status: ResultSuccess, Structured: json.RawMessage(`{"veryLong":"value"}`)}, ErrorCodeResultTooLarge, CallFailed},
		{"citations", Result{Status: ResultSuccess, Citations: []CitationRef{{Quote: strings.Repeat("evidence", 4)}}}, ErrorCodeResultTooLarge, CallFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			implementation := executorFixtureTool{definition: executorDefinition(), invoke: func(context.Context, Invocation) (Result, error) { return test.result, nil }}
			executor, repository, call := readyExecutor(t, implementation, ExecutorOptions{MaxTextBytes: 7, MaxStructuredBytes: 8})
			execution, err := executor.Execute(context.Background(), "project", call.ID)
			if err != nil || execution.ErrorCode != test.wantCode || repository.calls[call.ID].Status != test.status || !execution.Result.Truncated {
				t.Fatalf("Execute() = %#v, %v; call=%#v", execution, err, repository.calls[call.ID])
			}
			if !utf8Valid(execution.Result.Text) {
				t.Fatal("text truncation split UTF-8")
			}
		})
	}
}

func TestExecutorRejectsChangedSecuritySnapshot(t *testing.T) {
	definition := executorDefinition()
	implementation := executorFixtureTool{definition: definition, invoke: func(context.Context, Invocation) (Result, error) {
		return Result{Status: ResultSuccess}, nil
	}}
	executor, repository, call := readyExecutor(t, implementation, ExecutorOptions{})
	mutated := repository.calls[call.ID]
	mutated.ToolVersion = "stale"
	repository.calls[call.ID] = mutated
	execution, err := executor.Execute(context.Background(), "project", call.ID)
	if err != nil || execution.ErrorCode != ErrorCodeInvocationFailed || repository.calls[call.ID].Status != CallFailed {
		t.Fatalf("Execute(stale) = %#v, %v", execution, err)
	}
}

func TestExecutorRejectsProjectSubstitutionBeforeInvoke(t *testing.T) {
	invoked := false
	implementation := executorFixtureTool{definition: executorDefinition(), invoke: func(context.Context, Invocation) (Result, error) {
		invoked = true
		return Result{Status: ResultSuccess}, nil
	}}
	executor, repository, call := readyExecutor(t, implementation, ExecutorOptions{})
	if _, err := executor.Execute(context.Background(), "other-project", call.ID); err == nil {
		t.Fatal("project substitution was accepted")
	}
	if invoked || repository.calls[call.ID].Status != CallRunning {
		t.Fatal("executor mutated state before project ownership validation")
	}
}

func TestExecutorReturnsInvocationErrorAsPublicFailure(t *testing.T) {
	implementation := executorFixtureTool{definition: executorDefinition(), invoke: func(context.Context, Invocation) (Result, error) {
		return Result{}, errors.New("private filesystem detail")
	}}
	executor, _, call := readyExecutor(t, implementation, ExecutorOptions{})
	execution, err := executor.Execute(context.Background(), "project", call.ID)
	if err != nil || execution.ErrorCode != ErrorCodeInvocationFailed || strings.Contains(execution.Result.Text, "filesystem") {
		t.Fatalf("Execute(error) = %#v, %v", execution, err)
	}
}

func utf8Valid(value string) bool { return utf8.ValidString(value) }
