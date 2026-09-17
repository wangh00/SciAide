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
	defaultMaxTextBytes       = 256 * 1024
	defaultMaxStructuredBytes = 256 * 1024
)

type ExecutorOptions struct {
	InvocationTimeout           time.Duration
	MaxTextBytes                int
	MaxStructuredBytes          int
	OnInvocationError           func(call Call, err error)
	OnArtifactRegistrationError func(callID string, err error)
}

type RunProjectResolver interface {
	ProjectIDForRun(ctx context.Context, runID string) (string, error)
}

type SubjectProjectResolver interface {
	ProjectIDForSubject(ctx context.Context, subjectKind SubjectKind, subjectID string) (string, error)
}

type SubjectScopeResolver interface {
	ResearchTaskIDForSubject(ctx context.Context, subjectKind SubjectKind, subjectID string) (string, error)
	WorkspaceRootForSubject(ctx context.Context, subjectKind SubjectKind, subjectID string) (string, error)
}

type CompositeProjectResolver struct {
	Runs      RunProjectResolver
	Workflows SubjectProjectResolver
}

func (r CompositeProjectResolver) ProjectIDForRun(ctx context.Context, runID string) (string, error) {
	if r.Runs == nil {
		return "", fmt.Errorf("chat Run project resolver is not configured")
	}
	return r.Runs.ProjectIDForRun(ctx, runID)
}

func (r CompositeProjectResolver) ProjectIDForSubject(ctx context.Context, subjectKind SubjectKind, subjectID string) (string, error) {
	if NormalizeSubjectKind(subjectKind) == SubjectChatRun {
		return r.ProjectIDForRun(ctx, subjectID)
	}
	if r.Workflows == nil {
		return "", fmt.Errorf("Workflow Run project resolver is not configured")
	}
	return r.Workflows.ProjectIDForSubject(ctx, subjectKind, subjectID)
}

func (r CompositeProjectResolver) ResearchTaskIDForSubject(ctx context.Context, subjectKind SubjectKind, subjectID string) (string, error) {
	subjectKind = NormalizeSubjectKind(subjectKind)
	if resolver, ok := r.Workflows.(interface {
		ResearchTaskIDForSubject(context.Context, SubjectKind, string) (string, error)
	}); ok {
		return resolver.ResearchTaskIDForSubject(ctx, subjectKind, subjectID)
	}
	if subjectKind != SubjectWorkflowRun {
		return "", nil
	}
	resolver, ok := r.Workflows.(interface {
		ResearchTaskIDForWorkflowRun(context.Context, string) (string, error)
	})
	if !ok {
		return "", nil
	}
	return resolver.ResearchTaskIDForWorkflowRun(ctx, subjectID)
}

// WorkspaceRootForSubject keeps the executor's scope contract intact when the
// resolver is composed from the chat-run and workflow-run repositories.  A
// missing forwarding method silently downgraded workflow tools to the project
// root, which allowed task outputs to leak into the shared workspace.
func (r CompositeProjectResolver) WorkspaceRootForSubject(ctx context.Context, subjectKind SubjectKind, subjectID string) (string, error) {
	subjectKind = NormalizeSubjectKind(subjectKind)
	resolver, ok := r.Workflows.(interface {
		WorkspaceRootForSubject(context.Context, SubjectKind, string) (string, error)
	})
	if !ok {
		if subjectKind == SubjectChatRun {
			return "", nil
		}
		return "", fmt.Errorf("Workflow Run workspace resolver is not configured")
	}
	return resolver.WorkspaceRootForSubject(ctx, subjectKind, subjectID)
}

type ArtifactRegistrar interface {
	BindToolArtifacts(ctx context.Context, projectID string, call Call, references []ArtifactRef) ([]ArtifactRef, error)
	RegisterToolArtifactsForExecutor(ctx context.Context, callID string) error
}

type Execution struct {
	CallID         string `json:"callId"`
	Result         Result `json:"result"`
	ErrorCode      string `json:"errorCode,omitempty"`
	DurationMillis int64  `json:"durationMillis"`
}

// ValidateExecutionEnvelope checks the boundary between the execution
// coordinator and a caller that consumes a ToolResult. A missing call ID or
// malformed result must never be interpreted as a successful batch item.
func ValidateExecutionEnvelope(value Execution) error {
	if strings.TrimSpace(value.CallID) == "" {
		return fmt.Errorf("tool execution is missing call id")
	}
	if value.DurationMillis < 0 {
		return fmt.Errorf("tool execution duration must not be negative")
	}
	if err := ValidateResult(value.Result); err != nil {
		return fmt.Errorf("tool execution result is invalid: %w", err)
	}
	if value.ErrorCode == ErrorCodeOutcomeUnknown && value.Result.Status != ResultError {
		return fmt.Errorf("outcome-unknown execution must carry an error result")
	}
	return nil
}

// ExecuteMany executes already-authorized calls and preserves input order in
// the returned slice. Idempotent calls may run concurrently; side-effecting
// calls remain sequential for deterministic recovery.
func (e *Executor) ExecuteMany(ctx context.Context, projectID string, callIDs []string) ([]Execution, error) {
	results := make([]Execution, len(callIDs))
	if len(callIDs) == 0 {
		return results, nil
	}
	parallel := true
	seen := make(map[string]struct{}, len(callIDs))
	for index, callID := range callIDs {
		callID = strings.TrimSpace(callID)
		if callID == "" {
			return nil, fmt.Errorf("tool call id is required")
		}
		if _, exists := seen[callID]; exists {
			return nil, fmt.Errorf("duplicate tool call %q in batch", callID)
		}
		seen[callID] = struct{}{}
		call, err := e.service.Get(ctx, strings.TrimSpace(callID))
		if err != nil {
			return nil, fmt.Errorf("load tool call %q for batch: %w", strings.TrimSpace(callID), err)
		}
		if call.Status != CallRunning {
			if call.Status.Terminal() && call.Result != nil {
				results[index] = Execution{CallID: call.ID, Result: *call.Result, ErrorCode: call.ErrorCode}
				if err := ValidateExecutionEnvelope(results[index]); err != nil {
					return nil, err
				}
				continue
			}
			return nil, fmt.Errorf("tool call %q is not ready to execute", strings.TrimSpace(callID))
		}
		if !call.Idempotent {
			parallel = false
		}
	}
	if !parallel {
		for index, callID := range callIDs {
			if results[index].CallID != "" {
				continue
			}
			result, err := e.executeForBatch(ctx, projectID, callID)
			if err != nil {
				return results, err
			}
			if err := ValidateExecutionEnvelope(result); err != nil {
				return results, err
			}
			results[index] = result
		}
		return results, nil
	}
	// A terminal call may have been supplied by a provider replay. It is safe
	// to return its durable result and skip invocation, matching Execute's
	// idempotent replay behavior.
	var wait sync.WaitGroup
	errorsByIndex := make([]error, len(callIDs))
	for index, callID := range callIDs {
		index, callID := index, callID
		if results[index].CallID != "" {
			continue
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[index], errorsByIndex[index] = e.executeForBatch(ctx, projectID, callID)
		}()
	}
	wait.Wait()
	for _, err := range errorsByIndex {
		if err != nil {
			return results, err
		}
	}
	for _, result := range results {
		if err := ValidateExecutionEnvelope(result); err != nil {
			return nil, err
		}
	}
	return results, nil
}

func (e *Executor) executeForBatch(ctx context.Context, projectID, callID string) (Execution, error) {
	result, err := e.Execute(ctx, projectID, callID)
	if err == nil {
		return result, nil
	}
	// Only a persisted outcome-unknown state means the invocation may have
	// happened without a durable result. Structural, authorization, registry and
	// invocation errors remain ordinary batch errors and must not be relabeled as
	// possible side effects.
	if current, getErr := e.service.Get(context.Background(), callID); getErr == nil && current.ErrorCode == ErrorCodeOutcomeUnknown {
		return Execution{
			CallID:    callID,
			Result:    Result{Status: ResultError, Text: "tool outcome is unknown and requires retry", Meta: ResultMeta{}},
			ErrorCode: ErrorCodeOutcomeUnknown,
		}, nil
	}
	return Execution{}, err
}

type Executor struct {
	registry                    Registry
	service                     *Service
	projects                    RunProjectResolver
	timeout                     time.Duration
	maxText                     int
	maxJSON                     int
	now                         func() time.Time
	artifacts                   ArtifactRegistrar
	onInvocationError           func(call Call, err error)
	onArtifactRegistrationError func(callID string, err error)

	mu     sync.Mutex
	active map[string]context.CancelFunc
}

func NewExecutor(registry Registry, service *Service, projects RunProjectResolver, options ExecutorOptions) *Executor {
	timeout := options.InvocationTimeout
	if timeout < 0 {
		timeout = 0
	}
	maxText := options.MaxTextBytes
	if maxText <= 0 {
		maxText = defaultMaxTextBytes
	}
	maxJSON := options.MaxStructuredBytes
	if maxJSON <= 0 {
		maxJSON = defaultMaxStructuredBytes
	}
	return &Executor{registry: registry, service: service, projects: projects, timeout: timeout, maxText: maxText, maxJSON: maxJSON, now: func() time.Time { return time.Now().UTC() }, artifacts: nil, onInvocationError: options.OnInvocationError, onArtifactRegistrationError: options.OnArtifactRegistrationError, active: map[string]context.CancelFunc{}}
}

func (e *Executor) SetArtifactRegistrar(registrar ArtifactRegistrar) error {
	if registrar == nil {
		return fmt.Errorf("Artifact registrar is not configured")
	}
	e.artifacts = registrar
	return nil
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
		if call.Status.Terminal() && call.Result != nil {
			return Execution{CallID: call.ID, Result: *call.Result, ErrorCode: call.ErrorCode}, nil
		}
		return Execution{}, fmt.Errorf("tool call is not ready to execute")
	}
	var actualProjectID string
	if NormalizeSubjectKind(call.SubjectKind) == SubjectWorkflowRun {
		resolver, ok := e.projects.(SubjectProjectResolver)
		if !ok {
			return e.finishFailure(ctx, call, ErrorCodeInvocationFailed, "tool execution scope is not configured", e.now(), nil)
		}
		actualProjectID, err = resolver.ProjectIDForSubject(ctx, call.SubjectKind, call.RunID)
	} else {
		actualProjectID, err = e.projects.ProjectIDForRun(ctx, call.RunID)
	}
	if err != nil {
		return e.finishFailure(ctx, call, ErrorCodeInvocationFailed, "unable to resolve tool project scope", e.now(), nil)
	}
	if actualProjectID != projectID {
		// The host-supplied project ID is a trust boundary, not model input.
		// Reject a substituted project without mutating the durable call.
		return Execution{}, fmt.Errorf("tool call does not belong to project")
	}
	researchTaskID := ""
	invocationWorkspaceRoot := ""
	if resolver, ok := e.projects.(SubjectScopeResolver); ok {
		researchTaskID, err = resolver.ResearchTaskIDForSubject(ctx, call.SubjectKind, call.RunID)
		if err != nil {
			return e.finishFailure(ctx, call, ErrorCodeInvocationFailed, "unable to validate research task scope", e.now(), nil)
		}
		workspaceRoot, rootErr := resolver.WorkspaceRootForSubject(ctx, call.SubjectKind, call.RunID)
		if rootErr != nil {
			return e.finishFailure(ctx, call, ErrorCodeInvocationFailed, "unable to validate workspace scope", e.now(), nil)
		}
		if workspaceRoot != "" {
			invocationWorkspaceRoot = workspaceRoot
		}
	}
	// Artifact binding happens before the completed ToolCall is persisted, so
	// retain the resolved task scope on this in-memory call for that phase.
	call.ResearchTaskID = researchTaskID
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
	invokeCtx, cancel := context.WithCancel(ctx)
	if e.timeout > 0 {
		invokeCtx, cancel = context.WithTimeout(ctx, e.timeout)
	}
	if !e.register(call.ID, cancel) {
		cancel()
		return Execution{}, fmt.Errorf("tool call is already executing")
	}
	defer func() { cancel(); e.unregister(call.ID) }()

	started := e.now()
	result, invokeErr, panicOccurred := invokeSafely(invokeCtx, implementation, Invocation{
		CallID: call.ID, RunID: call.RunID, SubjectKind: NormalizeSubjectKind(call.SubjectKind),
		ProviderCallID: call.ProviderCallID, IdempotencyKey: call.IdempotencyKey,
		ProjectID: projectID, ResearchTaskID: researchTaskID, WorkspaceRoot: invocationWorkspaceRoot, Arguments: append(json.RawMessage(nil), call.Arguments...),
	})
	duration := e.now().Sub(started)
	if duration < 0 {
		duration = 0
	}
	result.Meta.DurationMillis = duration.Milliseconds()
	// A Run may have been terminated while a tool ignored cancellation and was
	// still unwinding. Never persist a late ToolResult into that terminal Run.
	if current, getErr := e.service.Get(context.Background(), call.ID); getErr != nil {
		return Execution{}, getErr
	} else if current.Status != CallRunning {
		if current.Status.Terminal() {
			if current.Result != nil {
				return Execution{CallID: current.ID, Result: *current.Result, ErrorCode: current.ErrorCode, DurationMillis: duration.Milliseconds()}, nil
			}
			return Execution{CallID: current.ID, Result: Result{Status: ResultCancelled, Text: current.ErrorMessage, Meta: ResultMeta{DurationMillis: duration.Milliseconds()}}, ErrorCode: current.ErrorCode, DurationMillis: duration.Milliseconds()}, nil
		}
		return Execution{}, fmt.Errorf("tool call changed state while executing")
	}

	switch {
	case panicOccurred:
		return e.finishFailure(ctx, call, ErrorCodePanic, "工具执行异常。", started, &duration)
	case errors.Is(invokeCtx.Err(), context.DeadlineExceeded) || errors.Is(invokeErr, context.DeadlineExceeded):
		return e.finishFailure(ctx, call, ErrorCodeTimeout, "工具执行超时。", started, &duration)
	case (errors.Is(invokeCtx.Err(), context.Canceled) || errors.Is(invokeErr, context.Canceled)) && !(invokeErr == nil && result.Status == ResultCancelled):
		return e.finishCancelled(call, duration)
	case invokeErr != nil:
		if e.onInvocationError != nil {
			e.onInvocationError(call, invokeErr)
		}
		if message, safe := userFacingMessage(invokeErr); safe {
			return e.finishFailure(ctx, call, ErrorCodeInvocationFailed, message, started, &duration)
		}
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
	if code == "" && result.Status == ResultSuccess && hasWorkspaceArtifacts(result.Artifacts) {
		if e.artifacts == nil {
			result = Result{Status: ResultError, Text: "工具声明的产物无法固定内容身份。", Artifacts: []ArtifactRef{}, Citations: result.Citations, Meta: result.Meta}
			code, message = ErrorCodeResultInvalid, "工具产物登记服务未配置"
		} else if bound, bindErr := e.artifacts.BindToolArtifacts(context.Background(), projectID, call, result.Artifacts); bindErr != nil {
			result = Result{Status: ResultError, Text: "工具声明的产物无法绑定到执行完成时的文件内容。", Artifacts: []ArtifactRef{}, Citations: result.Citations, Meta: result.Meta}
			code, message = ErrorCodeResultInvalid, "工具产物内容身份校验失败"
		} else {
			result.Artifacts = bound
			if bounded, boundCode, boundMessage := e.limitResult(result); boundCode != "" {
				result, code, message = bounded, boundCode, boundMessage
			}
		}
	}
	updated, err := e.service.Finish(context.Background(), call.ID, result, code, message)
	if err != nil {
		return Execution{}, e.outcomeUnknown(call.ID, err)
	}
	if updated.Status == CallCompleted && updated.Result != nil && hasWorkspaceArtifacts(updated.Result.Artifacts) && e.artifacts != nil {
		if err := e.artifacts.RegisterToolArtifactsForExecutor(context.Background(), updated.ID); err != nil {
			if e.onArtifactRegistrationError != nil {
				e.onArtifactRegistrationError(updated.ID, err)
			}
		}
	}
	return Execution{CallID: updated.ID, Result: *updated.Result, ErrorCode: code, DurationMillis: duration.Milliseconds()}, nil
}

func hasWorkspaceArtifacts(values []ArtifactRef) bool {
	for _, value := range values {
		if strings.TrimSpace(value.WorkspacePath) != "" {
			return true
		}
	}
	return false
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
		return Execution{}, e.outcomeUnknown(call.ID, err)
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
		return Execution{}, e.outcomeUnknown(call.ID, err)
	}
	return Execution{CallID: updated.ID, Result: *updated.Result, ErrorCode: code, DurationMillis: elapsed.Milliseconds()}, nil
}

func (e *Executor) outcomeUnknown(callID string, persistErr error) error {
	current, getErr := e.service.Get(context.Background(), callID)
	if getErr == nil && current.Status.Terminal() {
		return persistErr
	}
	if _, markErr := e.service.MarkOutcomeUnknown(context.Background(), callID); markErr != nil {
		return fmt.Errorf("tool outcome may be unknown; persist result: %v; mark outcome: %w", persistErr, markErr)
	}
	return fmt.Errorf("tool outcome may be unknown: %w", persistErr)
}

func (e *Executor) limitResult(value Result) (Result, string, string) {
	if p := value.ModelProjection; p != nil {
		projection := *p
		if len(projection.Text) > e.maxText {
			projection.Text = truncateUTF8(projection.Text, e.maxText)
			value.Truncated = true
		}
		if len(projection.Structured) > e.maxJSON {
			value.ModelProjection = nil
			value, _, _ = e.limitResult(value)
			value.Status = ResultError
			return value, ErrorCodeResultTooLarge, "工具模型视图超过大小限制"
		}
		value.ModelProjection = &projection
	}
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
		value.ModelProjection = nil
		value.Truncated = true
		if value.Status == ResultSuccess {
			value.Status = ResultError
		}
		return value, ErrorCodeResultTooLarge, "工具结构化结果超过大小限制"
	}
	auxiliaryBytes := 0
	if len(value.Artifacts) > 0 {
		encoded, err := json.Marshal(value.Artifacts)
		if err != nil {
			auxiliaryBytes = e.maxJSON + 1
		} else {
			auxiliaryBytes += len(encoded)
		}
	}
	if len(value.Citations) > 0 && auxiliaryBytes <= e.maxJSON {
		encoded, err := json.Marshal(value.Citations)
		if err != nil {
			auxiliaryBytes = e.maxJSON + 1
		} else {
			auxiliaryBytes += len(encoded)
		}
	}
	if auxiliaryBytes > e.maxJSON {
		if int64(auxiliaryBytes) > value.Meta.OriginalBytes {
			value.Meta.OriginalBytes = int64(auxiliaryBytes)
		}
		value.Artifacts = nil
		value.Citations = nil
		value.ModelProjection = nil
		value.Truncated = true
		if value.Status == ResultSuccess {
			value.Status = ResultError
		}
		return value, ErrorCodeResultTooLarge, "工具产物或引用结果超过大小限制"
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
	if call.ContractSHA256 != "" && call.IdempotencyKey != "" && call.ContractSHA256 != DefinitionFingerprint(definition) {
		return false
	}
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
