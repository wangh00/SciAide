package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	defaultInvocationTimeout  = 30 * time.Second
	defaultMaxTextBytes       = 256 * 1024
	defaultMaxStructuredBytes = 256 * 1024
)

type ExecutorOptions struct {
	InvocationTimeout  time.Duration
	MaxTextBytes       int
	MaxStructuredBytes int
}

type RunProjectResolver interface {
	ProjectIDForRun(ctx context.Context, runID string) (string, error)
}

type Execution struct {
	CallID         string `json:"callId"`
	Result         Result `json:"result"`
	ErrorCode      string `json:"errorCode,omitempty"`
	DurationMillis int64  `json:"durationMillis"`
}

type Executor struct {
	registry Registry
	service  *Service
	projects RunProjectResolver
	timeout  time.Duration
	maxText  int
	maxJSON  int
	now      func() time.Time

	mu     sync.Mutex
	active map[string]context.CancelFunc
}

func NewExecutor(registry Registry, service *Service, projects RunProjectResolver, options ExecutorOptions) *Executor {
	timeout := options.InvocationTimeout
	if timeout <= 0 {
		timeout = defaultInvocationTimeout
	}
	maxText := options.MaxTextBytes
	if maxText <= 0 {
		maxText = defaultMaxTextBytes
	}
	maxJSON := options.MaxStructuredBytes
	if maxJSON <= 0 {
		maxJSON = defaultMaxStructuredBytes
	}
	return &Executor{registry: registry, service: service, projects: projects, timeout: timeout, maxText: maxText, maxJSON: maxJSON, now: func() time.Time { return time.Now().UTC() }, active: map[string]context.CancelFunc{}}
}

func (e *Executor) Execute(ctx context.Context, projectID, callID string) (Execution, error) {
	if e.registry == nil || e.service == nil || e.projects == nil {
		return Execution{}, fmt.Errorf("tool executor is not configured")
	}
	projectID, callID = strings.TrimSpace(projectID), strings.TrimSpace(callID)
	if projectID == "" || callID == "" {
		return Execution{}, fmt.Errorf("project and tool call are required")
	}
	call, err := e.service.Get(ctx, callID)
	if err != nil {
		return Execution{}, err
	}
	if call.Status != CallRunning {
		return Execution{}, fmt.Errorf("tool call is not ready to execute")
	}
	actualProjectID, err := e.projects.ProjectIDForRun(ctx, call.RunID)
	if err != nil {
		return Execution{}, err
	}
	if actualProjectID != projectID {
		return Execution{}, fmt.Errorf("tool call does not belong to project")
	}
	registered, err := e.registry.Definition(ctx, call.ToolName)
	if err != nil {
		return e.finishFailure(ctx, call, ErrorCodeInvocationFailed, "工具当前不可用。", e.now(), nil)
	}
	if !definitionMatchesSnapshot(registered, call) {
		return e.finishFailure(ctx, call, ErrorCodeInvocationFailed, "工具定义已变化，拒绝执行旧调用。", e.now(), nil)
	}
	implementation, err := e.registry.Resolve(ctx, call.ToolName)
	if err != nil {
		return e.finishFailure(ctx, call, ErrorCodeInvocationFailed, "工具当前不可用。", e.now(), nil)
	}
	invokeCtx, cancel := context.WithTimeout(ctx, e.timeout)
	if !e.register(call.ID, cancel) {
		cancel()
		return Execution{}, fmt.Errorf("tool call is already executing")
	}
	defer func() { cancel(); e.unregister(call.ID) }()

	started := e.now()
	result, invokeErr, panicOccurred := invokeSafely(invokeCtx, implementation, Invocation{CallID: call.ID, RunID: call.RunID, ProjectID: projectID, Arguments: append(json.RawMessage(nil), call.Arguments...)})
	duration := e.now().Sub(started)
	if duration < 0 {
		duration = 0
	}
	result.Meta.DurationMillis = duration.Milliseconds()

	switch {
	case panicOccurred:
		return e.finishFailure(ctx, call, ErrorCodePanic, "工具执行异常。", started, &duration)
	case errors.Is(invokeCtx.Err(), context.DeadlineExceeded):
		return e.finishFailure(ctx, call, ErrorCodeTimeout, "工具执行超时。", started, &duration)
	case errors.Is(invokeCtx.Err(), context.Canceled) || errors.Is(invokeErr, context.Canceled):
		return e.finishCancelled(call, duration)
	case invokeErr != nil:
		return e.finishFailure(ctx, call, ErrorCodeInvocationFailed, "工具执行失败。", started, &duration)
	}

	result, code, message := e.limitResult(result)
	if code == "" {
		if err := ValidateResult(result); err != nil {
			result = Result{Status: ResultError, Text: "工具返回了无效结果。", Meta: ResultMeta{DurationMillis: duration.Milliseconds()}}
			code, message = ErrorCodeResultInvalid, "工具返回了无效结果"
		} else if len(registered.OutputSchema) > 0 && (len(result.Structured) == 0 || e.service.validator.Validate(registered.OutputSchema, result.Structured) != nil) {
			result = Result{Status: ResultError, Text: "工具返回了不符合契约的结果。", Meta: ResultMeta{DurationMillis: duration.Milliseconds()}}
			code, message = ErrorCodeResultInvalid, "工具返回结果不符合输出 Schema"
		}
	}
	updated, err := e.service.Finish(context.Background(), call.ID, result, code, message)
	if err != nil {
		return Execution{}, err
	}
	return Execution{CallID: updated.ID, Result: *updated.Result, ErrorCode: code, DurationMillis: duration.Milliseconds()}, nil
}

func (e *Executor) Cancel(callID string) bool {
	e.mu.Lock()
	cancel := e.active[strings.TrimSpace(callID)]
	e.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func (e *Executor) register(callID string, cancel context.CancelFunc) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.active[callID]; exists {
		return false
	}
	e.active[callID] = cancel
	return true
}

func (e *Executor) unregister(callID string) {
	e.mu.Lock()
	delete(e.active, callID)
	e.mu.Unlock()
}

func (e *Executor) finishCancelled(call Call, duration time.Duration) (Execution, error) {
	result := Result{Status: ResultCancelled, Text: "工具执行已取消。", Meta: ResultMeta{DurationMillis: duration.Milliseconds()}}
	updated, err := e.service.Finish(context.Background(), call.ID, result, ErrorCodeCancelled, "工具执行已取消")
	if err != nil {
		return Execution{}, err
	}
	return Execution{CallID: updated.ID, Result: *updated.Result, ErrorCode: ErrorCodeCancelled, DurationMillis: duration.Milliseconds()}, nil
}

func (e *Executor) finishFailure(_ context.Context, call Call, code, publicMessage string, started time.Time, duration *time.Duration) (Execution, error) {
	elapsed := e.now().Sub(started)
	if duration != nil {
		elapsed = *duration
	}
	if elapsed < 0 {
		elapsed = 0
	}
	result := Result{Status: ResultError, Text: publicMessage, Meta: ResultMeta{DurationMillis: elapsed.Milliseconds()}}
	updated, err := e.service.Finish(context.Background(), call.ID, result, code, strings.TrimSuffix(publicMessage, "。"))
	if err != nil {
		return Execution{}, err
	}
	return Execution{CallID: updated.ID, Result: *updated.Result, ErrorCode: code, DurationMillis: elapsed.Milliseconds()}, nil
}

func (e *Executor) limitResult(value Result) (Result, string, string) {
	if len(value.Text) > e.maxText {
		value.Meta.OriginalBytes = int64(len(value.Text))
		value.Text = truncateUTF8(value.Text, e.maxText)
		value.Truncated = true
	}
	if len(value.Structured) > e.maxJSON {
		if int64(len(value.Structured)) > value.Meta.OriginalBytes {
			value.Meta.OriginalBytes = int64(len(value.Structured))
		}
		value.Structured = nil
		value.Truncated = true
		if value.Status == ResultSuccess {
			value.Status = ResultError
		}
		return value, ErrorCodeResultTooLarge, "工具结构化结果超过大小限制"
	}
	return value, "", ""
}

func invokeSafely(ctx context.Context, value Tool, invocation Invocation) (result Result, err error, panicked bool) {
	defer func() {
		if recover() != nil {
			result, err, panicked = Result{}, nil, true
		}
	}()
	result, err = value.Invoke(ctx, invocation)
	return result, err, false
}

func definitionMatchesSnapshot(definition Definition, call Call) bool {
	if strings.TrimSpace(definition.QualifiedName) != call.ToolName || strings.TrimSpace(definition.Version) != call.ToolVersion || definition.Risk != call.Risk || definition.Idempotent != call.Idempotent {
		return false
	}
	actual := snapshotPermissions(strings.TrimSpace(definition.QualifiedName), definition.Permissions)
	if len(actual) != len(call.Permissions) {
		return false
	}
	for index := range actual {
		if actual[index].Kind != call.Permissions[index].Kind || actual[index].Resource != strings.TrimSpace(call.Permissions[index].Resource) {
			return false
		}
	}
	return true
}

func truncateUTF8(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	for limit > 0 && (value[limit]&0xc0) == 0x80 {
		limit--
	}
	return value[:limit]
}
