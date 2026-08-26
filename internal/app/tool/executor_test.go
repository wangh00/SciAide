package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type artifactRegistrarFixture struct {
	calls []string
	err   error
}

func (r *artifactRegistrarFixture) BindToolArtifacts(_ context.Context, _ string, _ Call, references []ArtifactRef) ([]ArtifactRef, error) {
	bound := append([]ArtifactRef(nil), references...)
	for index := range bound {
		if bound[index].WorkspacePath != "" {
			bound[index].SizeBytes = 7
			if bound[index].SHA256 == "" {
				bound[index].SHA256 = strings.Repeat("a", 64)
			}
		}
	}
	return bound, nil
}

func TestExecutorPersistsToolComputedArtifactHash(t *testing.T) {
	wantHash := strings.Repeat("b", 64)
	implementation := executorFixtureTool{definition: executorDefinition(), invoke: func(context.Context, Invocation) (Result, error) {
		return Result{Status: ResultSuccess, Artifacts: []ArtifactRef{{WorkspacePath: "outputs/result.csv", SHA256: wantHash}}}, nil
	}}
	executor, repository, call := readyExecutor(t, implementation, ExecutorOptions{})
	if err := executor.SetArtifactRegistrar(&artifactRegistrarFixture{}); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(context.Background(), "project", call.ID); err != nil {
		t.Fatal(err)
	}
	if repository.calls[call.ID].Result == nil || repository.calls[call.ID].Result.Artifacts[0].SHA256 != wantHash {
		t.Fatalf("persisted Artifact hash = %#v", repository.calls[call.ID].Result)
	}
}

func (r *artifactRegistrarFixture) RegisterToolArtifactsForExecutor(_ context.Context, callID string) error {
	r.calls = append(r.calls, callID)
	return r.err
}

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

func TestExecutorRegistersOnlyExplicitWorkspaceArtifacts(t *testing.T) {
	for _, test := range []struct {
		name string
		ref  ArtifactRef
		want int
	}{
		{name: "source reference", ref: ArtifactRef{ID: "attachment"}, want: 0},
		{name: "produced file", ref: ArtifactRef{Name: "result.csv", WorkspacePath: "outputs/result.csv"}, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			implementation := executorFixtureTool{definition: executorDefinition(), invoke: func(context.Context, Invocation) (Result, error) {
				return Result{Status: ResultSuccess, Artifacts: []ArtifactRef{test.ref}}, nil
			}}
			executor, _, call := readyExecutor(t, implementation, ExecutorOptions{})
			registrar := &artifactRegistrarFixture{}
			if err := executor.SetArtifactRegistrar(registrar); err != nil {
				t.Fatal(err)
			}
			if _, err := executor.Execute(context.Background(), "project", call.ID); err != nil {
				t.Fatal(err)
			}
			if len(registrar.calls) != test.want {
				t.Fatalf("registrar calls = %#v", registrar.calls)
			}
		})
	}
}

func TestExecutorKeepsSuccessfulToolOutcomeWhenArtifactRegistrationIsDeferred(t *testing.T) {
	wantErr := errors.New("fixture Artifact registration failure")
	var observedCall string
	var observedErr error
	implementation := executorFixtureTool{definition: executorDefinition(), invoke: func(context.Context, Invocation) (Result, error) {
		return Result{Status: ResultSuccess, Text: "tool output", Artifacts: []ArtifactRef{{WorkspacePath: "outputs/result.csv"}}}, nil
	}}
	executor, repository, call := readyExecutor(t, implementation, ExecutorOptions{OnArtifactRegistrationError: func(callID string, err error) {
		observedCall, observedErr = callID, err
	}})
	registrar := &artifactRegistrarFixture{err: wantErr}
	if err := executor.SetArtifactRegistrar(registrar); err != nil {
		t.Fatal(err)
	}
	execution, err := executor.Execute(context.Background(), "project", call.ID)
	if err != nil || execution.Result.Status != ResultSuccess || execution.Result.Text != "tool output" {
		t.Fatalf("Execute() = %#v, %v", execution, err)
	}
	loaded := repository.calls[call.ID]
	if loaded.Status != CallCompleted || loaded.Result == nil || loaded.Result.Status != ResultSuccess {
		t.Fatalf("persisted call = %#v", loaded)
	}
	if loaded.Result.Artifacts[0].SizeBytes != 7 || loaded.Result.Artifacts[0].SHA256 != strings.Repeat("a", 64) {
		t.Fatalf("persisted Artifact byte identity = %#v", loaded.Result.Artifacts[0])
	}
	if observedCall != call.ID || !errors.Is(observedErr, wantErr) {
		t.Fatalf("registration callback = %q, %v", observedCall, observedErr)
	}
}

func TestExecutorReappliesArtifactSizeLimitAfterBindingByteIdentity(t *testing.T) {
	implementation := executorFixtureTool{definition: executorDefinition(), invoke: func(context.Context, Invocation) (Result, error) {
		return Result{Status: ResultSuccess, Artifacts: []ArtifactRef{{Name: strings.Repeat("n", 60), WorkspacePath: "output.csv"}}}, nil
	}}
	executor, repository, call := readyExecutor(t, implementation, ExecutorOptions{MaxStructuredBytes: 120})
	registrar := &artifactRegistrarFixture{}
	if err := executor.SetArtifactRegistrar(registrar); err != nil {
		t.Fatal(err)
	}
	execution, err := executor.Execute(context.Background(), "project", call.ID)
	if err != nil {
		t.Fatal(err)
	}
	loaded := repository.calls[call.ID]
	if execution.Result.Status != ResultError || execution.ErrorCode != ErrorCodeResultTooLarge || loaded.Status != CallFailed || loaded.Result == nil || len(loaded.Result.Artifacts) != 0 || len(registrar.calls) != 0 {
		t.Fatalf("bound oversized Artifact result = %#v, execution=%#v, calls=%#v", loaded, execution, registrar.calls)
	}
}

func TestExecutorMarksOutcomeUnknownWhenResultCannotBePersisted(t *testing.T) {
	invoked := false
	implementation := executorFixtureTool{definition: executorDefinition(), invoke: func(context.Context, Invocation) (Result, error) {
		invoked = true
		return Result{Status: ResultSuccess, Text: "side effect may have happened"}, nil
	}}
	executor, repository, call := readyExecutor(t, implementation, ExecutorOptions{})
	repository.finishErr = errors.New("fixture persistence failure")
	_, err := executor.Execute(context.Background(), "project", call.ID)
	if err == nil || !strings.Contains(err.Error(), "outcome may be unknown") {
		t.Fatalf("Execute() error = %v", err)
	}
	loaded := repository.calls[call.ID]
	if !invoked || loaded.Status != CallInterrupted || loaded.ErrorCode != ErrorCodeOutcomeUnknown || loaded.Result != nil {
		t.Fatalf("unknown outcome call = %#v", loaded)
	}
}

func TestExecutorDefaultInvocationHasNoGlobalDeadline(t *testing.T) {
	implementation := executorFixtureTool{definition: executorDefinition(), invoke: func(context.Context, Invocation) (Result, error) {
		return Result{Status: ResultSuccess}, nil
	}}
	executor, _, _ := readyExecutor(t, implementation, ExecutorOptions{})
	if executor.timeout != 0 {
		t.Fatalf("default invocation timeout = %s", executor.timeout)
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

func TestExecutorPreservesStructuredCancellationReturnedByTool(t *testing.T) {
	started := make(chan struct{})
	implementation := executorFixtureTool{definition: executorDefinition(), invoke: func(ctx context.Context, _ Invocation) (Result, error) {
		close(started)
		<-ctx.Done()
		return Result{Status: ResultCancelled, Text: "process audit retained", Structured: json.RawMessage(`{"terminationReason":"cancelled"}`)}, nil
	}}
	executor, repository, call := readyExecutor(t, implementation, ExecutorOptions{})
	done := make(chan Execution, 1)
	go func() {
		value, _ := executor.Execute(context.Background(), "project", call.ID)
		done <- value
	}()
	<-started
	if !executor.Cancel(call.ID) {
		t.Fatal("Cancel() did not find active call")
	}
	result := <-done
	if result.Result.Status != ResultCancelled || result.Result.Text != "process audit retained" || string(result.Result.Structured) != `{"terminationReason":"cancelled"}` || repository.calls[call.ID].Status != CallCancelled {
		t.Fatalf("structured cancellation = %#v; call=%#v", result, repository.calls[call.ID])
	}
}

func TestExecutorClassifiesImplementationDeadlineAsTimeout(t *testing.T) {
	implementation := executorFixtureTool{definition: executorDefinition(), invoke: func(context.Context, Invocation) (Result, error) {
		return Result{}, fmt.Errorf("MCP tool call failed: %w", context.DeadlineExceeded)
	}}
	executor, repository, call := readyExecutor(t, implementation, ExecutorOptions{})
	execution, err := executor.Execute(context.Background(), "project", call.ID)
	if err != nil || execution.ErrorCode != ErrorCodeTimeout || repository.calls[call.ID].Status != CallFailed {
		t.Fatalf("Execute(deadline) = %#v, %v; call=%#v", execution, err, repository.calls[call.ID])
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
	wantErr := errors.New(`private filesystem detail: D:\Users\researcher\secret.csv`)
	var observedCall Call
	var observedErr error
	implementation := executorFixtureTool{definition: executorDefinition(), invoke: func(context.Context, Invocation) (Result, error) {
		return Result{}, wantErr
	}}
	executor, repository, call := readyExecutor(t, implementation, ExecutorOptions{OnInvocationError: func(call Call, err error) {
		observedCall, observedErr = call, err
	}})
	execution, err := executor.Execute(context.Background(), "project", call.ID)
	if err != nil || execution.ErrorCode != ErrorCodeInvocationFailed || execution.Result.Status != ResultError {
		t.Fatalf("Execute(error) = %#v, %v", execution, err)
	}
	if observedCall.ID != call.ID || !errors.Is(observedErr, wantErr) {
		t.Fatalf("invocation error callback = call %#v, error %v", observedCall, observedErr)
	}
	loaded := repository.calls[call.ID]
	public := execution.Result.Text + " " + loaded.ErrorMessage
	if loaded.Status != CallFailed || loaded.ErrorCode != ErrorCodeInvocationFailed || strings.Contains(public, "filesystem") || strings.Contains(public, "secret.csv") {
		t.Fatalf("persisted public failure leaked private details: execution=%#v call=%#v", execution, loaded)
	}
}

func utf8Valid(value string) bool { return utf8.ValidString(value) }
