package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/tools/pathguard"
)

type RunStatus string

const (
	RunQueued                   RunStatus = "queued"
	RunRunning                  RunStatus = "running"
	RunWaitingApproval          RunStatus = "waiting_approval"
	RunWaitingHumanConfirmation RunStatus = "waiting_human_confirmation"
	RunPaused                   RunStatus = "paused"
	RunCompleted                RunStatus = "completed"
	RunFailed                   RunStatus = "failed"
	RunCancelled                RunStatus = "cancelled"
	RunInterrupted              RunStatus = "interrupted"
)

type StepStatus string

const (
	StepQueued                   StepStatus = "queued"
	StepRunning                  StepStatus = "running"
	StepWaitingApproval          StepStatus = "waiting_approval"
	StepWaitingHumanConfirmation StepStatus = "waiting_human_confirmation"
	StepCompleted                StepStatus = "completed"
	StepFailed                   StepStatus = "failed"
	StepCancelled                StepStatus = "cancelled"
	StepInterrupted              StepStatus = "interrupted"
	StepOutcomeUnknown           StepStatus = "outcome_unknown"
)

func (value RunStatus) Terminal() bool {
	return value == RunCompleted || value == RunFailed || value == RunCancelled || value == RunInterrupted
}

type Run struct {
	ID                string                      `json:"id"`
	ProjectID         string                      `json:"projectId"`
	WorkflowID        string                      `json:"workflowId"`
	WorkflowVersionID string                      `json:"workflowVersionId"`
	Status            RunStatus                   `json:"status"`
	PermissionMode    conversation.PermissionMode `json:"permissionMode"`
	Inputs            json.RawMessage             `json:"inputs"`
	InputsSHA256      string                      `json:"inputsSha256"`
	Compilation       Compilation                 `json:"compilation"`
	CompilationSHA256 string                      `json:"compilationSha256"`
	Outputs           json.RawMessage             `json:"outputs"`
	CurrentStep       int                         `json:"currentStep"`
	ErrorCode         string                      `json:"errorCode,omitempty"`
	ErrorMessage      string                      `json:"errorMessage,omitempty"`
	CancelRequested   bool                        `json:"cancelRequested"`
	ResumeStatus      RunStatus                   `json:"resumeStatus,omitempty"`
	CreatedAt         time.Time                   `json:"createdAt"`
	StartedAt         *time.Time                  `json:"startedAt,omitempty"`
	CompletedAt       *time.Time                  `json:"completedAt,omitempty"`
	UpdatedAt         time.Time                   `json:"updatedAt"`
}

type Step struct {
	ID             string          `json:"id"`
	WorkflowRunID  string          `json:"workflowRunId"`
	NodeID         string          `json:"nodeId"`
	Ordinal        int             `json:"ordinal"`
	NodeKind       NodeKind        `json:"nodeKind"`
	Status         StepStatus      `json:"status"`
	Attempt        int             `json:"attempt"`
	Input          json.RawMessage `json:"input"`
	InputSHA256    string          `json:"inputSha256,omitempty"`
	Output         json.RawMessage `json:"output"`
	ToolCallID     string          `json:"toolCallId,omitempty"`
	IdempotencyKey string          `json:"idempotencyKey,omitempty"`
	ErrorCode      string          `json:"errorCode,omitempty"`
	ErrorMessage   string          `json:"errorMessage,omitempty"`
	StartedAt      *time.Time      `json:"startedAt,omitempty"`
	CompletedAt    *time.Time      `json:"completedAt,omitempty"`
	UpdatedAt      time.Time       `json:"updatedAt"`
}

type RuntimeEvent struct {
	ID            string          `json:"id"`
	WorkflowRunID string          `json:"workflowRunId"`
	Sequence      int64           `json:"sequence"`
	Type          string          `json:"type"`
	Payload       json.RawMessage `json:"payload"`
	CreatedAt     time.Time       `json:"createdAt"`
}

type HumanDecision struct {
	ID             string          `json:"id"`
	WorkflowRunID  string          `json:"workflowRunId"`
	WorkflowStepID string          `json:"workflowStepId"`
	Kind           string          `json:"kind"`
	Attempt        int             `json:"attempt"`
	Approved       bool            `json:"approved"`
	Note           string          `json:"note,omitempty"`
	Context        json.RawMessage `json:"context"`
	CreatedAt      time.Time       `json:"createdAt"`
}

type RunDetail struct {
	Run              Run                   `json:"run"`
	Steps            []Step                `json:"steps"`
	Events           []RuntimeEvent        `json:"events"`
	PendingApprovals []permission.Approval `json:"pendingApprovals"`
}

type StartCommand struct {
	ProjectID         string                      `json:"projectId"`
	WorkflowID        string                      `json:"workflowId"`
	WorkflowVersionID string                      `json:"workflowVersionId,omitempty"`
	Inputs            json.RawMessage             `json:"inputs"`
	PermissionMode    conversation.PermissionMode `json:"permissionMode,omitempty"`
}

type HumanDecisionCommand struct {
	ProjectID string          `json:"projectId"`
	RunID     string          `json:"runId"`
	StepID    string          `json:"stepId"`
	Approved  bool            `json:"approved"`
	Note      string          `json:"note,omitempty"`
	Context   json.RawMessage `json:"context,omitempty"`
}

type RetryCommand struct {
	ProjectID         string `json:"projectId"`
	RunID             string `json:"runId"`
	StepID            string `json:"stepId"`
	ConfirmSideEffect bool   `json:"confirmSideEffect"`
	Note              string `json:"note,omitempty"`
}

type RuntimeRepository interface {
	CreateRun(ctx context.Context, run Run, steps []Step, event RuntimeEvent) error
	GetRun(ctx context.Context, projectID, runID string) (RunDetail, error)
	ListRuns(ctx context.Context, projectID, workflowID string, limit int) ([]Run, error)
	MarkRunStarted(ctx context.Context, runID string, at time.Time, event RuntimeEvent) error
	BeginStep(ctx context.Context, runID, stepID string, input json.RawMessage, inputSHA256, idempotencyKey string, attempt int, at time.Time, event RuntimeEvent) error
	AttachToolCall(ctx context.Context, runID, stepID, callID string, at time.Time, event RuntimeEvent) error
	WaitStep(ctx context.Context, runID, stepID string, stepStatus StepStatus, runStatus RunStatus, at time.Time, event RuntimeEvent) error
	ResumeApprovalStep(ctx context.Context, runID, stepID string, at time.Time, event RuntimeEvent) error
	CompleteStep(ctx context.Context, runID, stepID string, output json.RawMessage, nextOrdinal int, final bool, finalOutputs json.RawMessage, at time.Time, event RuntimeEvent) error
	FailStep(ctx context.Context, runID, stepID string, stepStatus StepStatus, runStatus RunStatus, code, message string, at time.Time, event RuntimeEvent) error
	FailBlockedStep(ctx context.Context, runID, stepID, code, message string, at time.Time, event RuntimeEvent) error
	SetRunStatus(ctx context.Context, runID string, expected []RunStatus, next, resume RunStatus, at time.Time, event RuntimeEvent) error
	RecordDecision(ctx context.Context, decision HumanDecision, output json.RawMessage, nextOrdinal int, final bool, finalOutputs json.RawMessage, at time.Time, event RuntimeEvent) error
	RejectDecision(ctx context.Context, decision HumanDecision, stepStatus StepStatus, runStatus RunStatus, code, message string, at time.Time, event RuntimeEvent) error
	ResetStepForRetry(ctx context.Context, runID, stepID string, decision *HumanDecision, at time.Time, event RuntimeEvent) error
	RequestCancel(ctx context.Context, runID string, at time.Time, event RuntimeEvent) error
	RecoverableRuns(ctx context.Context) ([]string, error)
	ProjectIDForWorkflowRun(ctx context.Context, runID string) (string, error)
}

type subjectToolCalls interface {
	ListBySubject(ctx context.Context, subjectKind tool.SubjectKind, subjectID string) ([]tool.Call, error)
}

type RuntimeService struct {
	repository  RuntimeRepository
	workflows   Repository
	projects    ProjectLoader
	registry    tool.Registry
	tools       *tool.Service
	permissions *permission.Engine
	executor    *tool.Executor
	now         func() time.Time
	newID       func() (string, error)

	mu       sync.Mutex
	active   map[string]context.CancelFunc
	relaunch map[string]bool
	closed   bool
	wg       sync.WaitGroup
}

func NewRuntimeService(repository RuntimeRepository, workflows Repository, projects ProjectLoader, registry tool.Registry, tools *tool.Service, permissions *permission.Engine, executor *tool.Executor) (*RuntimeService, error) {
	if repository == nil || workflows == nil || projects == nil || registry == nil || tools == nil || permissions == nil || executor == nil {
		return nil, fmt.Errorf("Workflow Runtime is not configured")
	}
	return &RuntimeService{repository: repository, workflows: workflows, projects: projects, registry: registry, tools: tools, permissions: permissions, executor: executor, now: func() time.Time { return time.Now().UTC() }, newID: id.New, active: map[string]context.CancelFunc{}, relaunch: map[string]bool{}}, nil
}

func (s *RuntimeService) Start(ctx context.Context, command StartCommand) (RunDetail, error) {
	command.ProjectID, command.WorkflowID, command.WorkflowVersionID = strings.TrimSpace(command.ProjectID), strings.TrimSpace(command.WorkflowID), strings.TrimSpace(command.WorkflowVersionID)
	if command.ProjectID == "" || command.WorkflowID == "" {
		return RunDetail{}, fmt.Errorf("project and Workflow are required")
	}
	if command.PermissionMode == "" {
		command.PermissionMode = conversation.PermissionPlan
	}
	if !command.PermissionMode.Valid() {
		return RunDetail{}, fmt.Errorf("invalid Workflow permission mode")
	}
	detail, err := s.workflows.Get(ctx, command.ProjectID, command.WorkflowID)
	if err != nil {
		return RunDetail{}, err
	}
	version, ok := selectVersion(detail, command.WorkflowVersionID)
	if !ok {
		return RunDetail{}, fmt.Errorf("Workflow version not found")
	}
	if err := VerifyVersionSnapshot(version); err != nil {
		return RunDetail{}, err
	}
	runtimePorts := effectiveRuntimePorts(version.Compilation)
	inputs, _, err := validateRuntimeInputs(runtimePorts, command.Inputs)
	if err != nil {
		return RunDetail{}, err
	}
	if err := s.validateWorkspaceInputFiles(ctx, command.ProjectID, runtimePorts, inputs); err != nil {
		return RunDetail{}, err
	}
	inputs, hash, err := s.freezeWorkspaceInputFiles(ctx, command.ProjectID, runtimePorts, inputs)
	if err != nil {
		return RunDetail{}, err
	}
	if err := s.validateWorkspaceInputFiles(ctx, command.ProjectID, runtimePorts, inputs); err != nil {
		return RunDetail{}, err
	}
	runID, err := s.newID()
	if err != nil {
		return RunDetail{}, err
	}
	now := s.now()
	run := Run{ID: runID, ProjectID: command.ProjectID, WorkflowID: command.WorkflowID, WorkflowVersionID: version.ID, Status: RunQueued, PermissionMode: command.PermissionMode, Inputs: inputs, InputsSHA256: hash, Compilation: version.Compilation, CompilationSHA256: version.CompilationSHA256, Outputs: json.RawMessage(`{}`), CreatedAt: now, UpdatedAt: now}
	steps := make([]Step, 0, len(version.Compilation.Order))
	nodes := compilationNodeMap(version.Compilation)
	for ordinal, nodeID := range version.Compilation.Order {
		node := nodes[nodeID]
		stepID, idErr := s.newID()
		if idErr != nil {
			return RunDetail{}, idErr
		}
		steps = append(steps, Step{ID: stepID, WorkflowRunID: runID, NodeID: nodeID, Ordinal: ordinal, NodeKind: node.Kind, Status: StepQueued, Input: json.RawMessage(`{}`), Output: json.RawMessage(`{}`), UpdatedAt: now})
	}
	event, err := s.event(runID, "workflow.created", map[string]any{"workflowId": command.WorkflowID, "workflowVersionId": version.ID, "inputsSha256": hash, "permissionMode": command.PermissionMode}, now)
	if err != nil {
		return RunDetail{}, err
	}
	if err := s.repository.CreateRun(ctx, run, steps, event); err != nil {
		return RunDetail{}, err
	}
	result, err := s.repository.GetRun(ctx, command.ProjectID, runID)
	if err == nil {
		s.launch(runID)
	}
	return result, err
}

func selectVersion(detail Detail, versionID string) (Version, bool) {
	if versionID == "" {
		versionID = detail.Workflow.CurrentVersionID
	}
	for _, value := range detail.Versions {
		if value.ID == versionID {
			return value, true
		}
	}
	return Version{}, false
}

func (s *RuntimeService) Get(ctx context.Context, projectID, runID string) (RunDetail, error) {
	result, err := s.repository.GetRun(ctx, strings.TrimSpace(projectID), strings.TrimSpace(runID))
	if err != nil {
		return result, err
	}
	result.PendingApprovals, _ = s.permissions.ListPendingForSubject(ctx, tool.SubjectWorkflowRun, result.Run.ID)
	return result, nil
}

func (s *RuntimeService) List(ctx context.Context, projectID, workflowID string, limit int) ([]Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.repository.ListRuns(ctx, strings.TrimSpace(projectID), strings.TrimSpace(workflowID), limit)
}

func (s *RuntimeService) Pause(ctx context.Context, projectID, runID string) (RunDetail, error) {
	detail, err := s.Get(ctx, projectID, runID)
	if err != nil {
		return detail, err
	}
	if detail.Run.Status == RunPaused {
		return detail, nil
	}
	if detail.Run.Status.Terminal() {
		return detail, fmt.Errorf("terminal Workflow Run cannot be paused")
	}
	if detail.Run.Status == RunRunning {
		return detail, fmt.Errorf("Workflow Run can only pause at a committed checkpoint; wait for the current step or cancel it")
	}
	resume := detail.Run.Status
	event, _ := s.event(runID, "workflow.paused", map[string]any{"resumeStatus": resume}, s.now())
	if err := s.repository.SetRunStatus(ctx, runID, []RunStatus{detail.Run.Status}, RunPaused, resume, s.now(), event); err != nil {
		return RunDetail{}, err
	}
	return s.Get(ctx, projectID, runID)
}

func (s *RuntimeService) Resume(ctx context.Context, projectID, runID string) (RunDetail, error) {
	detail, err := s.Get(ctx, projectID, runID)
	if err != nil {
		return detail, err
	}
	if detail.Run.Status != RunPaused {
		return detail, fmt.Errorf("Workflow Run is not paused")
	}
	if code, validationErr := s.validateRunSnapshots(ctx, detail); validationErr != nil {
		if err := s.failBlockedStep(ctx, detail, code, validationErr); err != nil {
			return RunDetail{}, err
		}
		return s.Get(ctx, projectID, runID)
	}
	next := detail.Run.ResumeStatus
	if next == "" || next == RunRunning {
		next = RunQueued
	}
	event, _ := s.event(runID, "workflow.resumed", map[string]any{"status": next}, s.now())
	if err := s.repository.SetRunStatus(ctx, runID, []RunStatus{RunPaused}, next, "", s.now(), event); err != nil {
		return RunDetail{}, err
	}
	if next == RunQueued {
		s.launch(runID)
	}
	return s.Get(ctx, projectID, runID)
}

func (s *RuntimeService) Cancel(ctx context.Context, projectID, runID string) (RunDetail, error) {
	detail, err := s.Get(ctx, projectID, runID)
	if err != nil {
		return detail, err
	}
	if detail.Run.Status.Terminal() {
		return detail, nil
	}
	event, _ := s.event(runID, "workflow.cancel_requested", map[string]any{}, s.now())
	if err := s.repository.RequestCancel(ctx, runID, s.now(), event); err != nil {
		return RunDetail{}, err
	}
	if step := currentStep(detail); step != nil && step.ToolCallID != "" {
		s.executor.Cancel(step.ToolCallID)
	}
	s.mu.Lock()
	if cancel := s.active[runID]; cancel != nil {
		cancel()
	}
	s.mu.Unlock()
	return s.Get(ctx, projectID, runID)
}

func (s *RuntimeService) Decide(ctx context.Context, command HumanDecisionCommand) (RunDetail, error) {
	command.ProjectID, command.RunID, command.StepID, command.Note = strings.TrimSpace(command.ProjectID), strings.TrimSpace(command.RunID), strings.TrimSpace(command.StepID), strings.TrimSpace(command.Note)
	if len([]rune(command.Note)) > 10_000 {
		return RunDetail{}, fmt.Errorf("decision note exceeds 10000 characters")
	}
	detail, err := s.Get(ctx, command.ProjectID, command.RunID)
	if err != nil {
		return detail, err
	}
	step := findStep(detail.Steps, command.StepID)
	if step == nil || step.Status != StepWaitingHumanConfirmation || detail.Run.Status != RunWaitingHumanConfirmation {
		return RunDetail{}, fmt.Errorf("Workflow step is not waiting for a human decision")
	}
	if command.Approved {
		if code, validationErr := s.validateRunSnapshots(ctx, detail); validationErr != nil {
			if err := s.failBlockedStep(ctx, detail, code, validationErr); err != nil {
				return RunDetail{}, err
			}
			return s.Get(ctx, command.ProjectID, command.RunID)
		}
	}
	node := compilationNodeMap(detail.Run.Compilation)[step.NodeID]
	if node.Kind != NodeHumanConfirmation && node.Kind != NodeCandidateSelection && node.Kind != NodeCitationSelection {
		return RunDetail{}, fmt.Errorf("Workflow step does not accept a human decision")
	}
	contextJSON := normalizeJSON(command.Context, json.RawMessage(`{}`))
	if len(contextJSON) > 256*1024 || !json.Valid(contextJSON) {
		return RunDetail{}, fmt.Errorf("decision context is invalid or too large")
	}
	output := json.RawMessage(`{"approved":false}`)
	if node.Kind == NodeCandidateSelection {
		if !command.Approved {
			return s.rejectHumanDecision(ctx, detail, *step, command, contextJSON, "CANDIDATE_SELECTION_REJECTED", "用户取消了候选筛选")
		}
		var selection struct {
			SelectedCandidateIDs []string `json:"selectedCandidateIds"`
		}
		if json.Unmarshal(contextJSON, &selection) != nil || len(selection.SelectedCandidateIDs) == 0 || len(selection.SelectedCandidateIDs) > 100 {
			return RunDetail{}, fmt.Errorf("candidate selection must contain 1-100 selected candidate ids")
		}
		var input struct {
			Candidates []struct {
				ID string `json:"id"`
			} `json:"candidates"`
		}
		if json.Unmarshal(step.Input, &input) != nil || len(input.Candidates) > 100 {
			return RunDetail{}, fmt.Errorf("candidate selection candidates are invalid")
		}
		available := make(map[string]struct{}, len(input.Candidates))
		for _, value := range input.Candidates {
			if id := strings.TrimSpace(value.ID); id != "" {
				available[id] = struct{}{}
			}
		}
		seen := make(map[string]struct{}, len(selection.SelectedCandidateIDs))
		for index, value := range selection.SelectedCandidateIDs {
			value = strings.TrimSpace(value)
			if _, exists := available[value]; !exists {
				return RunDetail{}, fmt.Errorf("candidate selection contains an id that was not offered by the previous step")
			}
			if _, duplicate := seen[value]; duplicate {
				return RunDetail{}, fmt.Errorf("candidate selection ids must be unique")
			}
			seen[value] = struct{}{}
			selection.SelectedCandidateIDs[index] = value
		}
		output, _ = json.Marshal(selection)
	} else if node.Kind == NodeCitationSelection {
		if !command.Approved {
			return s.rejectHumanDecision(ctx, detail, *step, command, contextJSON, "CITATION_SELECTION_REJECTED", "用户取消了引用选择")
		}
		var citations []tool.CitationRef
		if json.Unmarshal(contextJSON, &citations) != nil || len(citations) > 256 {
			return RunDetail{}, fmt.Errorf("citation selection must be an array of at most 256 citation snapshots")
		}
		var input struct {
			Candidates []tool.CitationRef `json:"candidates"`
		}
		if json.Unmarshal(step.Input, &input) != nil || len(input.Candidates) > 256 {
			return RunDetail{}, fmt.Errorf("citation selection candidates are invalid")
		}
		if err := validateCitationSubset(citations, input.Candidates); err != nil {
			return RunDetail{}, err
		}
		output, _ = json.Marshal(map[string]any{"citations": citations})
	} else {
		output, _ = json.Marshal(map[string]any{"approved": command.Approved, "context": json.RawMessage(contextJSON)})
		if !command.Approved {
			return s.rejectHumanDecision(ctx, detail, *step, command, contextJSON, "HUMAN_CONFIRMATION_REJECTED", "用户拒绝了人工确认节点")
		}
	}
	decisionID, _ := s.newID()
	decision := HumanDecision{ID: decisionID, WorkflowRunID: command.RunID, WorkflowStepID: step.ID, Kind: "node_confirmation", Attempt: step.Attempt, Approved: command.Approved, Note: command.Note, Context: contextJSON, CreatedAt: s.now()}
	nextOrdinal := step.Ordinal + 1
	final, outputs, err := finalOutputs(detail.Run, detail.Steps, step.ID, output, nextOrdinal)
	if err != nil {
		return RunDetail{}, err
	}
	event, _ := s.event(command.RunID, "workflow.human_decided", map[string]any{"stepId": step.ID, "approved": command.Approved}, s.now())
	if final {
		nextOrdinal = len(detail.Steps)
	}
	if err := s.repository.RecordDecision(ctx, decision, output, nextOrdinal, final, outputs, s.now(), event); err != nil {
		return RunDetail{}, err
	}
	if !final {
		s.launch(command.RunID)
	}
	return s.Get(ctx, command.ProjectID, command.RunID)
}

func validateCitationSubset(selected, candidates []tool.CitationRef) error {
	available := make(map[string]int, len(candidates))
	for _, value := range candidates {
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("citation selection candidates are invalid")
		}
		available[string(encoded)]++
	}
	for _, value := range selected {
		encoded, err := json.Marshal(value)
		if err != nil || available[string(encoded)] == 0 {
			return fmt.Errorf("citation selection contains a snapshot that was not offered by the previous step")
		}
		available[string(encoded)]--
	}
	return nil
}

func (s *RuntimeService) rejectHumanDecision(ctx context.Context, detail RunDetail, step Step, command HumanDecisionCommand, contextJSON json.RawMessage, code, message string) (RunDetail, error) {
	decisionID, err := s.newID()
	if err != nil {
		return RunDetail{}, err
	}
	decision := HumanDecision{ID: decisionID, WorkflowRunID: detail.Run.ID, WorkflowStepID: step.ID, Kind: "node_confirmation", Attempt: step.Attempt, Approved: false, Note: command.Note, Context: contextJSON, CreatedAt: s.now()}
	event, _ := s.event(detail.Run.ID, "workflow.failed", map[string]any{"stepId": step.ID, "errorCode": code}, s.now())
	if err := s.repository.RejectDecision(ctx, decision, StepFailed, RunFailed, code, message, s.now(), event); err != nil {
		return RunDetail{}, err
	}
	return s.Get(ctx, detail.Run.ProjectID, detail.Run.ID)
}

func (s *RuntimeService) ResolveApproval(ctx context.Context, command permission.ResolveCommand) (RunDetail, error) {
	pending, err := s.permissions.Get(ctx, command.ApprovalID)
	if err != nil {
		return RunDetail{}, err
	}
	if tool.NormalizeSubjectKind(pending.SubjectKind) != tool.SubjectWorkflowRun {
		return RunDetail{}, fmt.Errorf("approval does not belong to a Workflow Run")
	}
	detail, err := s.Get(ctx, pending.ProjectID, pending.RunID)
	if err != nil {
		return RunDetail{}, err
	}
	step := stepByToolCall(detail.Steps, pending.ToolCallID)
	if step == nil || detail.Run.Status != RunWaitingApproval || step.Status != StepWaitingApproval {
		return RunDetail{}, fmt.Errorf("approval state does not match Workflow step")
	}
	call, err := s.tools.Get(ctx, pending.ToolCallID)
	if err != nil || call.Status != tool.CallAwaitingApproval {
		return RunDetail{}, fmt.Errorf("approval state does not match ToolCall")
	}
	if command.Allow {
		if code, validationErr := s.validateRunSnapshots(ctx, detail); validationErr != nil {
			if err := s.failBlockedStep(ctx, detail, code, validationErr); err != nil {
				return RunDetail{}, err
			}
			return s.Get(ctx, pending.ProjectID, pending.RunID)
		}
	}
	resolved, _, err := s.permissions.Resolve(ctx, command)
	if err != nil {
		return RunDetail{}, err
	}
	if resolved.Status == permission.ApprovalDenied {
		if err := s.failBlockedStep(ctx, detail, "TOOL_PERMISSION_DENIED", errors.New("用户拒绝了 Workflow 工具权限")); err != nil {
			return RunDetail{}, err
		}
		return s.Get(ctx, pending.ProjectID, pending.RunID)
	}
	request := permission.EvaluationRequest{ProjectID: detail.Run.ProjectID, RunID: detail.Run.ID, SubjectKind: tool.SubjectWorkflowRun, Call: call}
	evaluation, err := s.permissions.EvaluateWorkflow(ctx, request, detail.Run.PermissionMode)
	if err != nil {
		return RunDetail{}, err
	}
	if evaluation.Decision == permission.DecisionAsk {
		if _, err := s.permissions.RequestApproval(ctx, request, evaluation); err != nil {
			return RunDetail{}, err
		}
		return s.Get(ctx, pending.ProjectID, pending.RunID)
	}
	event, _ := s.event(detail.Run.ID, "workflow.approval_completed", map[string]any{"stepId": step.ID, "toolCallId": call.ID}, s.now())
	if err := s.repository.ResumeApprovalStep(ctx, detail.Run.ID, step.ID, s.now(), event); err != nil {
		return RunDetail{}, err
	}
	s.launch(detail.Run.ID)
	return s.Get(ctx, pending.ProjectID, pending.RunID)
}

func (s *RuntimeService) Retry(ctx context.Context, command RetryCommand) (RunDetail, error) {
	command.ProjectID, command.RunID, command.StepID, command.Note = strings.TrimSpace(command.ProjectID), strings.TrimSpace(command.RunID), strings.TrimSpace(command.StepID), strings.TrimSpace(command.Note)
	detail, err := s.Get(ctx, command.ProjectID, command.RunID)
	if err != nil {
		return detail, err
	}
	if detail.Run.Status != RunFailed && detail.Run.Status != RunInterrupted {
		return detail, fmt.Errorf("only failed or interrupted Workflow Runs can retry")
	}
	if _, validationErr := s.validateRunSnapshots(ctx, detail); validationErr != nil {
		return detail, validationErr
	}
	step := findStep(detail.Steps, command.StepID)
	if step == nil || (step.Status != StepFailed && step.Status != StepInterrupted && step.Status != StepOutcomeUnknown) {
		return RunDetail{}, fmt.Errorf("Workflow step is not retryable")
	}
	node := compilationNodeMap(detail.Run.Compilation)[step.NodeID]
	var decision *HumanDecision
	if node.SideEffect {
		if !command.ConfirmSideEffect {
			return RunDetail{}, fmt.Errorf("retrying a non-idempotent step requires explicit side-effect confirmation")
		}
		decisionID, _ := s.newID()
		decision = &HumanDecision{ID: decisionID, WorkflowRunID: detail.Run.ID, WorkflowStepID: step.ID, Kind: "retry_side_effect", Attempt: step.Attempt + 1, Approved: true, Note: command.Note, Context: json.RawMessage(`{}`), CreatedAt: s.now()}
	}
	event, _ := s.event(detail.Run.ID, "workflow.step_retry_queued", map[string]any{"stepId": step.ID, "attempt": step.Attempt + 1}, s.now())
	if err := s.repository.ResetStepForRetry(ctx, detail.Run.ID, step.ID, decision, s.now(), event); err != nil {
		return RunDetail{}, err
	}
	s.launch(detail.Run.ID)
	return s.Get(ctx, command.ProjectID, command.RunID)
}

func (s *RuntimeService) Recover(ctx context.Context) (int, error) {
	runIDs, err := s.repository.RecoverableRuns(ctx)
	if err != nil {
		return 0, err
	}
	for _, runID := range runIDs {
		projectID, err := s.repository.ProjectIDForWorkflowRun(ctx, runID)
		if err != nil {
			return 0, err
		}
		detail, err := s.repository.GetRun(ctx, projectID, runID)
		if err != nil {
			return 0, err
		}
		if detail.Run.Status == RunWaitingHumanConfirmation || detail.Run.Status == RunWaitingApproval || detail.Run.Status == RunPaused {
			if code, validationErr := s.validateRunSnapshots(ctx, detail); validationErr != nil {
				if err := s.failBlockedStep(ctx, detail, code, validationErr); err != nil {
					return 0, err
				}
				continue
			}
			if detail.Run.Status == RunWaitingApproval {
				if err := s.recoverApprovalCheckpoint(ctx, detail); err != nil {
					return 0, err
				}
			}
			continue
		}
		step := currentStep(detail)
		if step != nil && (step.Status == StepRunning || step.Status == StepWaitingApproval) {
			if step.ToolCallID == "" {
				if code, validationErr := s.validateRunSnapshots(ctx, detail); validationErr != nil {
					if err := s.failBlockedStep(ctx, detail, code, validationErr); err != nil {
						return 0, err
					}
					continue
				}
				// A crash may occur after the deterministic ToolCall is proposed but
				// before its id is linked to the step. driveTool reuses that call.
				s.launch(runID)
				continue
			}
			call, callErr := s.tools.Get(ctx, step.ToolCallID)
			switch {
			case callErr == nil && call.StartedAt != nil && !call.Idempotent && (call.Status == tool.CallRunning || call.Status == tool.CallInterrupted || call.Status == tool.CallCancelled):
				if call.Status == tool.CallRunning {
					_, _ = s.tools.MarkOutcomeUnknown(ctx, call.ID)
				}
				event, _ := s.event(runID, "workflow.outcome_unknown", map[string]any{"stepId": step.ID, "toolCallId": call.ID}, s.now())
				if err := s.repository.FailStep(ctx, runID, step.ID, StepOutcomeUnknown, RunInterrupted, tool.ErrorCodeOutcomeUnknown, "应用重启时非幂等步骤可能已经产生副作用，必须人工确认后重试", s.now(), event); err != nil {
					return 0, err
				}
				continue
			case callErr == nil:
				if code, validationErr := s.validateRunSnapshots(ctx, detail); validationErr != nil {
					if err := s.failBlockedStep(ctx, detail, code, validationErr); err != nil {
						return 0, err
					}
					continue
				}
			}
			switch {
			case callErr == nil && call.Status == tool.CallCompleted && call.Result != nil:
				if err := s.commitToolResult(ctx, detail, *step, call); err != nil {
					return 0, err
				}
			case callErr == nil && call.Idempotent && (call.Status == tool.CallRunning || call.Status == tool.CallInterrupted || call.Status == tool.CallCancelled):
				if call.Status == tool.CallRunning {
					if _, err := s.tools.Interrupt(ctx, call.ID, "应用重启后重新执行未提交的幂等 Workflow 步骤"); err != nil && !errors.Is(err, tool.ErrTransitionConflict) {
						return 0, err
					}
				}
				event, _ := s.event(runID, "workflow.step_recovered", map[string]any{"stepId": step.ID}, s.now())
				if err := s.repository.ResetStepForRetry(ctx, runID, step.ID, nil, s.now(), event); err != nil {
					return 0, err
				}
			case callErr == nil:
				// Pending, awaiting and terminal calls are deterministic checkpoints.
				// Let driveTool either continue the same call or project its terminal result.
				s.launch(runID)
				continue
			default:
				event, _ := s.event(runID, "workflow.step_recovered", map[string]any{"stepId": step.ID}, s.now())
				if err := s.repository.ResetStepForRetry(ctx, runID, step.ID, nil, s.now(), event); err != nil {
					return 0, err
				}
			}
		} else if code, validationErr := s.validateRunSnapshots(ctx, detail); validationErr != nil {
			if err := s.failBlockedStep(ctx, detail, code, validationErr); err != nil {
				return 0, err
			}
			continue
		}
		s.launch(runID)
	}
	return len(runIDs), nil
}

func (s *RuntimeService) recoverApprovalCheckpoint(ctx context.Context, detail RunDetail) error {
	step := currentStep(detail)
	if step == nil || step.Status != StepWaitingApproval || step.ToolCallID == "" {
		return s.failBlockedStep(ctx, detail, "WORKFLOW_APPROVAL_CHECKPOINT_INVALID", errors.New("Workflow 授权检查点不完整，请重试该步骤"))
	}
	call, err := s.tools.Get(ctx, step.ToolCallID)
	if err != nil {
		return s.failBlockedStep(ctx, detail, "WORKFLOW_APPROVAL_CHECKPOINT_INVALID", errors.New("Workflow 授权对应的工具调用不存在，请重试该步骤"))
	}
	pending, err := s.permissions.ListPendingForSubject(ctx, tool.SubjectWorkflowRun, detail.Run.ID)
	if err != nil {
		return err
	}
	approvals, err := s.permissions.ListBySubject(ctx, tool.SubjectWorkflowRun, detail.Run.ID)
	if err != nil {
		return err
	}
	hasPending := false
	hasGranted := false
	hasDenied := false
	hasExpired := false
	for _, approval := range pending {
		if approval.ToolCallID == call.ID {
			hasPending = true
			break
		}
	}
	for _, approval := range approvals {
		if approval.ToolCallID != call.ID {
			continue
		}
		switch approval.Status {
		case permission.ApprovalGranted:
			hasGranted = true
		case permission.ApprovalDenied:
			hasDenied = true
		case permission.ApprovalExpired:
			hasExpired = true
		}
	}
	if hasDenied {
		return s.failBlockedStep(ctx, detail, "TOOL_PERMISSION_DENIED", errors.New("用户拒绝了 Workflow 工具权限"))
	}
	if hasExpired && !hasPending && !hasGranted {
		return s.failBlockedStep(ctx, detail, "WORKFLOW_APPROVAL_EXPIRED", errors.New("Workflow 工具授权已失效，请重试该步骤"))
	}
	switch call.Status {
	case tool.CallPending:
		if !hasPending {
			return s.failBlockedStep(ctx, detail, "WORKFLOW_APPROVAL_CHECKPOINT_INVALID", errors.New("Workflow 授权记录不完整，请重试该步骤"))
		}
		_, err = s.tools.AwaitApproval(ctx, call.ID)
		return err
	case tool.CallAwaitingApproval:
		if hasPending {
			return nil
		}
		request := permission.EvaluationRequest{ProjectID: detail.Run.ProjectID, RunID: detail.Run.ID, SubjectKind: tool.SubjectWorkflowRun, Call: call}
		evaluation, evaluateErr := s.permissions.EvaluateWorkflow(ctx, request, detail.Run.PermissionMode)
		if evaluateErr != nil {
			return evaluateErr
		}
		if evaluation.Decision == permission.DecisionAsk {
			if hasGranted {
				return s.failBlockedStep(ctx, detail, "WORKFLOW_APPROVAL_CHECKPOINT_INVALID", errors.New("Workflow 已有授权未覆盖当前工具要求，请重试该步骤"))
			}
			_, requestErr := s.permissions.RequestApproval(ctx, request, evaluation)
			return requestErr
		}
		// Resume the Workflow checkpoint first. driveTool will start the same
		// approved ToolCall, so a crash cannot leave a running call behind a
		// waiting Workflow Run.
	case tool.CallRunning:
		if call.StartedAt != nil && !call.Idempotent {
			_, _ = s.tools.MarkOutcomeUnknown(ctx, call.ID)
			event, _ := s.event(detail.Run.ID, "workflow.outcome_unknown", map[string]any{"stepId": step.ID, "toolCallId": call.ID}, s.now())
			return s.repository.FailStep(ctx, detail.Run.ID, step.ID, StepOutcomeUnknown, RunInterrupted, tool.ErrorCodeOutcomeUnknown, "应用重启时非幂等步骤可能已经产生副作用，必须人工确认后重试", s.now(), event)
		}
		if _, err := s.tools.Interrupt(ctx, call.ID, "应用重启后重新执行未提交的幂等 Workflow 步骤"); err != nil && !errors.Is(err, tool.ErrTransitionConflict) {
			return err
		}
		event, _ := s.event(detail.Run.ID, "workflow.step_recovered", map[string]any{"stepId": step.ID}, s.now())
		if err := s.repository.ResetStepForRetry(ctx, detail.Run.ID, step.ID, nil, s.now(), event); err != nil {
			return err
		}
		s.launch(detail.Run.ID)
		return nil
	case tool.CallCompleted:
		// Approval was committed before the Workflow checkpoint advanced.
	case tool.CallInterrupted, tool.CallCancelled:
		if call.StartedAt != nil && !call.Idempotent {
			event, _ := s.event(detail.Run.ID, "workflow.outcome_unknown", map[string]any{"stepId": step.ID, "toolCallId": call.ID}, s.now())
			return s.repository.FailStep(ctx, detail.Run.ID, step.ID, StepOutcomeUnknown, RunInterrupted, tool.ErrorCodeOutcomeUnknown, "应用重启时非幂等步骤可能已经产生副作用，必须人工确认后重试", s.now(), event)
		}
		event, _ := s.event(detail.Run.ID, "workflow.step_recovered", map[string]any{"stepId": step.ID}, s.now())
		if err := s.repository.ResetStepForRetry(ctx, detail.Run.ID, step.ID, nil, s.now(), event); err != nil {
			return err
		}
		s.launch(detail.Run.ID)
		return nil
	case tool.CallFailed, tool.CallDenied:
		// Resume the Workflow state so driveTool can persist the terminal call.
	default:
		return s.failBlockedStep(ctx, detail, "WORKFLOW_APPROVAL_CHECKPOINT_INVALID", errors.New("Workflow 授权对应的工具调用状态无效，请重试该步骤"))
	}
	event, _ := s.event(detail.Run.ID, "workflow.approval_recovered", map[string]any{"stepId": step.ID, "toolCallId": call.ID}, s.now())
	if err := s.repository.ResumeApprovalStep(ctx, detail.Run.ID, step.ID, s.now(), event); err != nil {
		return err
	}
	s.launch(detail.Run.ID)
	return nil
}

func (s *RuntimeService) Close() error {
	s.BeginShutdown()
	s.Wait()
	return nil
}

// BeginShutdown prevents new drives and broadcasts cancellation without
// waiting for tools or transports to finish unwinding.
func (s *RuntimeService) BeginShutdown() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closed = true
	for _, cancel := range s.active {
		cancel()
	}
	s.mu.Unlock()
}

// Wait drains all Workflow drivers after their execution resources have been
// stopped. The caller must keep storage open until Wait returns.
func (s *RuntimeService) Wait() {
	if s == nil {
		return
	}
	s.wg.Wait()
}

func (s *RuntimeService) launch(runID string) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if s.active[runID] != nil {
		s.relaunch[runID] = true
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.active[runID] = cancel
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.active, runID)
			relaunch := s.relaunch[runID]
			delete(s.relaunch, runID)
			s.mu.Unlock()
			if relaunch {
				s.launch(runID)
			}
		}()
		_ = s.drive(ctx, runID)
	}()
}

func (s *RuntimeService) drive(ctx context.Context, runID string) error {
	projectID, err := s.repository.ProjectIDForWorkflowRun(ctx, runID)
	if err != nil {
		return err
	}
	for {
		detail, err := s.repository.GetRun(ctx, projectID, runID)
		if err != nil {
			return err
		}
		if detail.Run.Status.Terminal() || detail.Run.Status == RunPaused || detail.Run.Status == RunWaitingApproval || detail.Run.Status == RunWaitingHumanConfirmation {
			return nil
		}
		if detail.Run.Status == RunQueued {
			event, _ := s.event(runID, "workflow.running", map[string]any{"stepOrdinal": detail.Run.CurrentStep}, s.now())
			if err := s.repository.MarkRunStarted(ctx, runID, s.now(), event); err != nil {
				return err
			}
			continue
		}
		step := currentStep(detail)
		if step == nil {
			return nil
		}
		node := compilationNodeMap(detail.Run.Compilation)[step.NodeID]
		if step.Status == StepQueued {
			input, err := bindNodeInput(detail.Run, detail.Steps, node)
			if err != nil {
				return s.failDrive(ctx, detail, *step, "WORKFLOW_INPUT_BINDING_FAILED", err.Error(), StepFailed, RunFailed)
			}
			inputHash := hashJSON(input)
			key := hashStrings(runID, step.NodeID, fmt.Sprint(step.Attempt+1), inputHash)
			event, _ := s.event(runID, "workflow.step_started", map[string]any{"stepId": step.ID, "nodeId": step.NodeID, "inputSha256": inputHash}, s.now())
			if err := s.repository.BeginStep(ctx, runID, step.ID, input, inputHash, key, step.Attempt+1, s.now(), event); err != nil {
				return err
			}
			continue
		}
		if step.Status != StepRunning {
			return nil
		}
		switch node.Kind {
		case NodeHumanConfirmation, NodeCandidateSelection, NodeCitationSelection:
			event, _ := s.event(runID, "workflow.human_confirmation_requested", map[string]any{"stepId": step.ID, "prompt": node.Prompt, "kind": node.Kind}, s.now())
			return s.repository.WaitStep(ctx, runID, step.ID, StepWaitingHumanConfirmation, RunWaitingHumanConfirmation, s.now(), event)
		case NodeTool, NodeShell, NodePython:
			if err := s.driveTool(ctx, detail, *step, node); err != nil {
				return err
			}
		default:
			return s.failDrive(ctx, detail, *step, "WORKFLOW_NODE_UNSUPPORTED", "unsupported Workflow node kind", StepFailed, RunFailed)
		}
	}
}

func (s *RuntimeService) driveTool(ctx context.Context, detail RunDetail, step Step, node CompiledNode) error {
	if code, validationErr := s.validateRunSnapshots(ctx, detail); validationErr != nil {
		return s.failBlockedStep(ctx, detail, code, validationErr)
	}
	definition := definitionFromSnapshot(*node.Tool)
	current, currentErr := s.registry.Definition(ctx, definition.QualifiedName)
	if currentErr != nil || !definitionSnapshotEqual(definition, current) {
		return s.failBlockedStep(ctx, detail, "WORKFLOW_TOOL_DEFINITION_CHANGED", errors.New("工具定义已变化，必须重新校验并保存 Workflow 版本"))
	}
	callID := step.ToolCallID
	var call tool.Call
	var err error
	if callID == "" {
		call, err = s.tools.Propose(ctx, definition, tool.CreateCommand{RunID: detail.Run.ID, SubjectKind: tool.SubjectWorkflowRun, ProviderCallID: fmt.Sprintf("workflow:%s:%d", step.NodeID, step.Attempt), Arguments: step.Input, IdempotencyKey: step.IdempotencyKey})
		if err != nil {
			// A crash may have persisted the deterministic call before the step link.
			if calls, listErr := s.tools.ListBySubject(ctx, tool.SubjectWorkflowRun, detail.Run.ID); listErr == nil {
				for _, candidate := range calls {
					if candidate.ProviderCallID == fmt.Sprintf("workflow:%s:%d", step.NodeID, step.Attempt) {
						call, err = candidate, nil
						break
					}
				}
			}
		}
		if err != nil {
			return s.failDrive(ctx, detail, step, "WORKFLOW_TOOL_PROPOSAL_FAILED", err.Error(), StepFailed, RunFailed)
		}
		event, _ := s.event(detail.Run.ID, "workflow.tool_proposed", map[string]any{"stepId": step.ID, "toolCallId": call.ID}, s.now())
		if err := s.repository.AttachToolCall(ctx, detail.Run.ID, step.ID, call.ID, s.now(), event); err != nil {
			return err
		}
		step.ToolCallID = call.ID
	} else {
		call, err = s.tools.Get(ctx, callID)
		if err != nil {
			return err
		}
	}
	if call.Status == tool.CallPending || call.Status == tool.CallAwaitingApproval {
		request := permission.EvaluationRequest{ProjectID: detail.Run.ProjectID, RunID: detail.Run.ID, SubjectKind: tool.SubjectWorkflowRun, Call: call}
		evaluation, err := s.permissions.EvaluateWorkflow(ctx, request, detail.Run.PermissionMode)
		if err != nil {
			return err
		}
		if evaluation.Decision == permission.DecisionAsk {
			pending, listErr := s.permissions.ListPendingForSubject(ctx, tool.SubjectWorkflowRun, detail.Run.ID)
			if listErr != nil {
				return listErr
			}
			hasPending := false
			for _, approval := range pending {
				if approval.ToolCallID == call.ID {
					hasPending = true
					break
				}
			}
			if !hasPending {
				if _, err := s.permissions.RequestApproval(ctx, request, evaluation); err != nil {
					return err
				}
			}
			if call.Status == tool.CallPending {
				if _, err := s.tools.AwaitApproval(ctx, call.ID); err != nil {
					return err
				}
			}
			event, _ := s.event(detail.Run.ID, "workflow.approval_requested", map[string]any{"stepId": step.ID, "toolCallId": call.ID}, s.now())
			return s.repository.WaitStep(ctx, detail.Run.ID, step.ID, StepWaitingApproval, RunWaitingApproval, s.now(), event)
		}
		if call.Status == tool.CallPending || call.Status == tool.CallAwaitingApproval {
			call, err = s.tools.Start(ctx, call.ID)
			if err != nil {
				return err
			}
		}
	}
	if call.Status != tool.CallRunning {
		if call.Status == tool.CallCompleted && call.Result != nil {
			return s.commitToolResult(ctx, detail, step, call)
		}
		return s.failDrive(ctx, detail, step, "WORKFLOW_TOOL_NOT_EXECUTABLE", call.ErrorMessage, StepFailed, RunFailed)
	}
	_, executeErr := s.executor.Execute(ctx, detail.Run.ProjectID, call.ID)
	call, getErr := s.tools.Get(context.Background(), call.ID)
	if getErr != nil {
		return getErr
	}
	if s.isClosed() {
		// Leave the durable Workflow step active. Startup recovery distinguishes
		// idempotent replay from an unknown non-idempotent outcome.
		return nil
	}
	if executeErr != nil && call.Status == tool.CallInterrupted && !call.Idempotent {
		return s.failDrive(context.Background(), detail, step, tool.ErrorCodeOutcomeUnknown, executeErr.Error(), StepOutcomeUnknown, RunInterrupted)
	}
	if call.Status != tool.CallCompleted || call.Result == nil || call.Result.Status != tool.ResultSuccess {
		message := call.ErrorMessage
		if call.Result != nil && strings.TrimSpace(call.Result.Text) != "" {
			message = call.Result.Text
		}
		status, runStatus := StepFailed, RunFailed
		if call.Status == tool.CallCancelled || errors.Is(ctx.Err(), context.Canceled) {
			status, runStatus = StepCancelled, RunCancelled
		}
		return s.failDrive(context.Background(), detail, step, call.ErrorCode, message, status, runStatus)
	}
	return s.commitToolResult(context.Background(), detail, step, call)
}

func (s *RuntimeService) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *RuntimeService) commitToolResult(ctx context.Context, detail RunDetail, step Step, call tool.Call) error {
	output, err := encodeToolOutput(*call.Result)
	if err != nil {
		return err
	}
	nextOrdinal := step.Ordinal + 1
	final, outputs, err := finalOutputs(detail.Run, detail.Steps, step.ID, output, nextOrdinal)
	if err != nil {
		return err
	}
	eventType := "workflow.step_completed"
	if final {
		eventType = "workflow.completed"
	}
	event, _ := s.event(detail.Run.ID, eventType, map[string]any{"stepId": step.ID, "toolCallId": call.ID}, s.now())
	return s.repository.CompleteStep(ctx, detail.Run.ID, step.ID, output, nextOrdinal, final, outputs, s.now(), event)
}

func (s *RuntimeService) failDrive(ctx context.Context, detail RunDetail, step Step, code, message string, stepStatus StepStatus, runStatus RunStatus) error {
	if strings.TrimSpace(message) == "" {
		message = "Workflow step failed"
	}
	event, _ := s.event(detail.Run.ID, "workflow."+string(runStatus), map[string]any{"stepId": step.ID, "errorCode": code}, s.now())
	return s.repository.FailStep(ctx, detail.Run.ID, step.ID, stepStatus, runStatus, strings.TrimSpace(code), strings.TrimSpace(message), s.now(), event)
}

func (s *RuntimeService) validateRunSnapshots(ctx context.Context, detail RunDetail) (string, error) {
	if hashJSON(detail.Run.Inputs) != detail.Run.InputsSHA256 {
		return "WORKFLOW_INPUT_SNAPSHOT_INVALID", fmt.Errorf("Workflow 输入快照摘要不匹配；请用同一研究方案重新发起任务")
	}
	compilation := detail.Run.Compilation
	if compilation.CompilationSHA256 != detail.Run.CompilationSHA256 {
		return "WORKFLOW_COMPILATION_SNAPSHOT_INVALID", fmt.Errorf("Workflow 编译快照摘要不匹配；请重新保存研究方案并发起任务")
	}
	compilation.CompilationSHA256 = ""
	encoded, err := canonicalJSON(compilation)
	if err != nil || hashBytes(encoded) != detail.Run.CompilationSHA256 {
		return "WORKFLOW_COMPILATION_SNAPSHOT_INVALID", fmt.Errorf("Workflow 编译快照已失效；请重新保存研究方案并发起任务")
	}
	ports := effectiveRuntimePorts(detail.Run.Compilation)
	if err := validateFrozenWorkflowInputs(ports, detail.Run.Inputs); err != nil {
		return "WORKFLOW_INPUT_SNAPSHOT_REQUIRED", err
	}
	if err := s.validateWorkspaceInputFiles(ctx, detail.Run.ProjectID, ports, detail.Run.Inputs); err != nil {
		return "WORKFLOW_INPUT_SNAPSHOT_INVALID", err
	}
	return "", nil
}

func (s *RuntimeService) failBlockedStep(ctx context.Context, detail RunDetail, code string, cause error) error {
	step := currentStep(detail)
	if step == nil {
		return fmt.Errorf("cannot fail Workflow Run without a current step: %w", cause)
	}
	if step.ToolCallID != "" {
		s.executor.Cancel(step.ToolCallID)
	}
	event, _ := s.event(detail.Run.ID, "workflow.failed", map[string]any{"stepId": step.ID, "errorCode": code}, s.now())
	return s.repository.FailBlockedStep(ctx, detail.Run.ID, step.ID, code, cause.Error(), s.now(), event)
}

func (s *RuntimeService) event(runID, eventType string, payload any, at time.Time) (RuntimeEvent, error) {
	eventID, err := s.newID()
	if err != nil {
		return RuntimeEvent{}, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return RuntimeEvent{}, err
	}
	return RuntimeEvent{ID: eventID, WorkflowRunID: runID, Type: eventType, Payload: encoded, CreatedAt: at}, nil
}

func validateRuntimeInputs(ports []Port, raw json.RawMessage) (json.RawMessage, string, error) {
	value := normalizeJSON(raw, json.RawMessage(`{}`))
	if len(value) > 1<<20 || !json.Valid(value) {
		return nil, "", fmt.Errorf("Workflow inputs are invalid or exceed 1 MiB")
	}
	var inputs map[string]json.RawMessage
	if json.Unmarshal(value, &inputs) != nil || inputs == nil {
		return nil, "", fmt.Errorf("Workflow inputs must be a JSON object")
	}
	declared := map[string]Port{}
	for _, port := range ports {
		declared[port.Name] = port
		if _, exists := inputs[port.Name]; !exists && len(port.Default) > 0 {
			inputs[port.Name] = cloneRaw(port.Default)
		}
	}
	for name, input := range inputs {
		port, ok := declared[name]
		if !ok {
			return nil, "", fmt.Errorf("Workflow input %q is not declared", name)
		}
		if !runtimeTypeMatches(input, port.Type) {
			return nil, "", fmt.Errorf("Workflow input %q does not match type %s", name, port.Type)
		}
		if port.Required && runtimeRequiredValueMissing(input, port.Type) {
			return nil, "", fmt.Errorf("Workflow input %q cannot be empty", name)
		}
		if err := validateRuntimePortConstraints(port, input); err != nil {
			return nil, "", err
		}
		if isPathName(name) {
			if err := runtimePathError(input, "inputs."+name); err != nil {
				return nil, "", err
			}
		}
	}
	for _, port := range ports {
		if _, exists := inputs[port.Name]; port.Required && !exists {
			return nil, "", fmt.Errorf("Workflow input %q is required", port.Name)
		}
	}
	encoded, _ := json.Marshal(inputs)
	return encoded, hashJSON(encoded), nil
}

func effectiveRuntimePorts(compilation Compilation) []Port {
	ports := append([]Port(nil), compilation.Inputs...)
	if compilation.CompilerVersion != "p7.4-v1" {
		return ports
	}
	fileKind := legacyAnalysisDefinitionKinds[compilation.DefinitionSHA256]
	if fileKind == "" {
		return ports
	}
	pythonAnalysis := false
	structuredRequest := false
	for _, node := range compilation.Nodes {
		pythonAnalysis = pythonAnalysis || node.ID == "analysis" && node.Kind == NodePython
	}
	if !pythonAnalysis {
		return ports
	}
	for _, edge := range compilation.Edges {
		if edge.FromNode == "$input" && edge.FromPort == "analysis_request" && edge.ToNode == "analysis" && edge.ToPort == "inputData" {
			structuredRequest = true
		}
	}
	for index := range ports {
		port := &ports[index]
		for _, edge := range compilation.Edges {
			if edge.FromNode == "$input" && edge.FromPort == port.Name && edge.ToNode == "analysis" && edge.ToPort == "inputPaths" && port.Type == TypeArray {
				port.MinItems, port.MaxItems = 1, 1
				port.FileKind = fileKind
			}
		}
		if structuredRequest && port.Name == "analysis_request" && port.Type == TypeObject {
			port.Control = "analysis_request"
		}
	}
	return ports
}

var legacyAnalysisDefinitionKinds = func() map[string]string {
	result := map[string]string{}
	for _, item := range []struct {
		template Template
		kind     string
	}{{xlsxAnalysisTemplate(), "xlsx"}, {pythonAnalysisTemplate(), "delimited"}} {
		definition := item.template.Definition
		for index := range definition.Inputs {
			definition.Inputs[index].FileKind = ""
			definition.Inputs[index].Control = ""
			definition.Inputs[index].MinItems = 0
			definition.Inputs[index].MaxItems = 0
		}
		encoded, err := canonicalJSON(definition)
		if err == nil {
			result[hashBytes(encoded)] = item.kind
		}
	}
	return result
}()

func validateRuntimePortConstraints(port Port, raw json.RawMessage) error {
	if port.MinItems > 0 || port.MaxItems > 0 {
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return fmt.Errorf("Workflow input %q must be an array", port.Name)
		}
		if port.MinItems > 0 && len(values) < port.MinItems {
			return fmt.Errorf("Workflow input %q must contain at least %d item(s)", port.Name, port.MinItems)
		}
		if port.MaxItems > 0 && len(values) > port.MaxItems {
			return fmt.Errorf("Workflow input %q must contain at most %d item(s)", port.Name, port.MaxItems)
		}
	}
	if port.FileKind != "" {
		values := workflowInputFileValues(raw)
		if len(values) == 0 {
			return fmt.Errorf("Workflow input %q must contain project data file path(s)", port.Name)
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("Workflow input %q contains an empty file path", port.Name)
			}
		}
	}
	if port.Control == "analysis_request" {
		var request struct {
			Goal   string `json:"goal"`
			Method string `json:"method"`
		}
		if json.Unmarshal(raw, &request) != nil || strings.TrimSpace(request.Goal) == "" || len([]rune(request.Goal)) > 2_000 {
			return fmt.Errorf("Workflow analysis goal must contain 1-2000 characters")
		}
		switch strings.TrimSpace(request.Method) {
		case "overview", "data_quality", "numeric_distribution":
		default:
			return fmt.Errorf("Workflow analysis method is unsupported")
		}
	}
	return nil
}

func runtimeRequiredValueMissing(raw json.RawMessage, dataType DataType) bool {
	switch dataType {
	case TypeString:
		var value string
		return json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == ""
	case TypeArray, TypeArtifacts, TypeCitations:
		var value []json.RawMessage
		return json.Unmarshal(raw, &value) != nil || len(value) == 0
	case TypeObject:
		var value map[string]json.RawMessage
		return json.Unmarshal(raw, &value) != nil || len(value) == 0
	default:
		return false
	}
}

func (s *RuntimeService) validateWorkspaceInputFiles(ctx context.Context, projectID string, ports []Port, inputs json.RawMessage) error {
	selected, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return err
	}
	guard, err := pathguard.Open(selected.WorkspacePath)
	if err != nil {
		return err
	}
	defer guard.Close()
	var values map[string]json.RawMessage
	if json.Unmarshal(inputs, &values) != nil {
		return fmt.Errorf("Workflow inputs are unavailable for file validation")
	}
	for _, port := range ports {
		if port.FileKind == "" {
			continue
		}
		for _, value := range workflowInputFileValues(values[port.Name]) {
			clean, err := guard.Relative(value)
			if err != nil || clean == "." || workflowPrivatePath(clean) {
				return fmt.Errorf("Workflow input %q must reference a regular file inside the project Workspace", port.Name)
			}
			file, _, err := guard.OpenFile(clean)
			if err != nil {
				return fmt.Errorf("Workflow input file %q is unavailable: %w", filepath.ToSlash(clean), err)
			}
			info, statErr := file.Stat()
			if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				file.Close()
				return fmt.Errorf("Workflow input file %q is not a regular file", filepath.ToSlash(clean))
			}
			if err := workflowFileKindError(port.FileKind, clean); err != nil {
				file.Close()
				return fmt.Errorf("Workflow input file %q: %w", filepath.ToSlash(clean), err)
			}
			if err := verifyContentAddressedWorkflowInput(file, clean); err != nil {
				file.Close()
				return fmt.Errorf("Workflow input file %q: %w", filepath.ToSlash(clean), err)
			}
			if err := file.Close(); err != nil {
				return fmt.Errorf("close Workflow input file %q: %w", filepath.ToSlash(clean), err)
			}
		}
	}
	return nil
}

func (s *RuntimeService) freezeWorkspaceInputFiles(ctx context.Context, projectID string, ports []Port, inputs json.RawMessage) (json.RawMessage, string, error) {
	var values map[string]json.RawMessage
	if json.Unmarshal(inputs, &values) != nil {
		return nil, "", fmt.Errorf("Workflow inputs are unavailable for file freezing")
	}
	selected, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return nil, "", err
	}
	guard, err := pathguard.Open(selected.WorkspacePath)
	if err != nil {
		return nil, "", err
	}
	defer guard.Close()
	stager := &Service{projects: s.projects}
	for _, port := range ports {
		if port.FileKind == "" {
			continue
		}
		paths := workflowInputFileValues(values[port.Name])
		frozen := make([]string, 0, len(paths))
		for _, value := range paths {
			clean, cleanErr := guard.Relative(value)
			if cleanErr != nil || clean == "." || workflowPrivatePath(clean) {
				return nil, "", fmt.Errorf("Workflow input %q must reference a regular file inside the project Workspace", port.Name)
			}
			staged, stageErr := stager.StageInputFile(ctx, projectID, filepath.Join(selected.WorkspacePath, clean), port.FileKind)
			if stageErr != nil {
				return nil, "", fmt.Errorf("freeze Workflow input %q: %w", port.Name, stageErr)
			}
			frozen = append(frozen, staged.RelativePath)
		}
		if port.Type == TypeString {
			if len(frozen) != 1 {
				return nil, "", fmt.Errorf("Workflow file input %q must contain one path", port.Name)
			}
			values[port.Name], _ = json.Marshal(frozen[0])
		} else {
			values[port.Name], _ = json.Marshal(frozen)
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, "", err
	}
	return encoded, hashJSON(encoded), nil
}

func workflowFileKindError(kind, path string) error {
	extension := strings.ToLower(filepath.Ext(path))
	switch kind {
	case "", "tabular":
		if extension == ".csv" || extension == ".tsv" || extension == ".xlsx" {
			return nil
		}
	case "delimited":
		if extension == ".csv" || extension == ".tsv" {
			return nil
		}
	case "xlsx":
		if extension == ".xlsx" {
			return nil
		}
	}
	return fmt.Errorf("does not match the required %s data format", kind)
}

func workflowInputFileValues(raw json.RawMessage) []string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []string{text}
	}
	var values []string
	_ = json.Unmarshal(raw, &values)
	return values
}

func validateFrozenWorkflowInputs(ports []Port, inputs json.RawMessage) error {
	var values map[string]json.RawMessage
	if json.Unmarshal(inputs, &values) != nil {
		return fmt.Errorf("Workflow file inputs are unavailable")
	}
	for _, port := range ports {
		if port.FileKind == "" {
			continue
		}
		for _, value := range workflowInputFileValues(values[port.Name]) {
			if _, ok := workflowSnapshotExpectedHash(value); !ok {
				return fmt.Errorf("此任务的数据输入创建于旧版本，没有不可变内容快照；请用同一研究方案重新发起任务")
			}
		}
	}
	return nil
}

// FrozenInputPaths returns the content-addressed Workspace files referenced by
// explicit Workflow file ports. Archive code uses the same legacy-port
// compatibility and snapshot validation as Runtime execution.
func FrozenInputPaths(compilation Compilation, inputs json.RawMessage) ([]string, error) {
	ports := effectiveRuntimePorts(compilation)
	if err := validateFrozenWorkflowInputs(ports, inputs); err != nil {
		return nil, err
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(inputs, &values) != nil {
		return nil, fmt.Errorf("Workflow file inputs are unavailable")
	}
	seen := map[string]struct{}{}
	result := []string{}
	for _, port := range ports {
		if port.FileKind == "" {
			continue
		}
		for _, value := range workflowInputFileValues(values[port.Name]) {
			clean := filepath.ToSlash(filepath.Clean(value))
			if _, ok := workflowSnapshotExpectedHash(clean); !ok {
				return nil, fmt.Errorf("Workflow input is not a content-addressed snapshot")
			}
			if _, duplicate := seen[clean]; duplicate {
				continue
			}
			seen[clean] = struct{}{}
			result = append(result, clean)
		}
	}
	sort.Strings(result)
	return result, nil
}

// FrozenInputSHA256 returns the digest encoded in a Runtime snapshot path.
func FrozenInputSHA256(relative string) (string, bool) {
	return workflowSnapshotExpectedHash(relative)
}

func verifyContentAddressedWorkflowInput(file *os.File, relative string) error {
	expected, ok := workflowSnapshotExpectedHash(relative)
	if !ok {
		// Snapshots created by earlier versions did not carry a full digest.
		return nil
	}
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("rewind snapshot: %w", err)
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return fmt.Errorf("hash snapshot: %w", err)
	}
	if hex.EncodeToString(digest.Sum(nil)) != expected {
		return fmt.Errorf("content-addressed snapshot was modified after selection")
	}
	return nil
}

func workflowSnapshotExpectedHash(relative string) (string, bool) {
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) != 2 || !strings.EqualFold(parts[0], "research-inputs") {
		return "", false
	}
	base := strings.TrimSuffix(parts[1], filepath.Ext(parts[1]))
	separator := strings.LastIndexByte(base, '-')
	if separator < 0 || len(base)-separator-1 != sha256.Size*2 {
		return "", false
	}
	expected := strings.ToLower(base[separator+1:])
	if _, err := hex.DecodeString(expected); err != nil {
		return "", false
	}
	return expected, true
}

func workflowPrivatePath(relative string) bool {
	first := strings.Split(filepath.ToSlash(relative), "/")[0]
	return strings.EqualFold(first, ".sciaide")
}

func runtimeTypeMatches(raw json.RawMessage, dataType DataType) bool {
	if dataType == TypeAny {
		return json.Valid(raw)
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	switch dataType {
	case TypeString:
		_, ok := value.(string)
		return ok
	case TypeNumber:
		_, ok := value.(float64)
		return ok
	case TypeInteger:
		number, ok := value.(float64)
		return ok && number == float64(int64(number))
	case TypeBoolean:
		_, ok := value.(bool)
		return ok
	case TypeObject:
		_, ok := value.(map[string]any)
		return ok
	case TypeArray, TypeArtifacts, TypeCitations:
		_, ok := value.([]any)
		return ok
	default:
		return false
	}
}

func bindNodeInput(run Run, steps []Step, node CompiledNode) (json.RawMessage, error) {
	arguments := decodeObject(node.Arguments)
	var inputs map[string]json.RawMessage
	_ = json.Unmarshal(run.Inputs, &inputs)
	stepMap := map[string]Step{}
	for _, step := range steps {
		stepMap[step.NodeID] = step
	}
	for _, edge := range run.Compilation.Edges {
		if edge.ToNode != node.ID {
			continue
		}
		var value json.RawMessage
		if edge.FromNode == "$input" {
			value = inputs[edge.FromPort]
		} else {
			step, ok := stepMap[edge.FromNode]
			if !ok || step.Status != StepCompleted {
				return nil, fmt.Errorf("dependency %s has no committed output", edge.FromNode)
			}
			var err error
			value, err = outputPort(step.Output, edge.FromPort)
			if err != nil {
				return nil, err
			}
		}
		if len(value) == 0 {
			return nil, fmt.Errorf("edge %s.%s produced no value", edge.FromNode, edge.FromPort)
		}
		arguments[edge.ToPort] = cloneRaw(value)
	}
	encoded, err := json.Marshal(arguments)
	if err != nil || len(encoded) > 256*1024 {
		return nil, fmt.Errorf("bound Workflow node input is invalid or too large")
	}
	if diagnostics := validatePathArguments(encoded, "arguments", nil); len(diagnostics) > 0 {
		return nil, fmt.Errorf("%s: %s", diagnostics[0].Path, diagnostics[0].Message)
	}
	return encoded, nil
}

func outputPort(raw json.RawMessage, port string) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return nil, fmt.Errorf("committed Workflow output is invalid")
	}
	if strings.HasPrefix(port, "structured.") {
		var structured map[string]json.RawMessage
		if json.Unmarshal(object["structured"], &structured) != nil {
			return nil, fmt.Errorf("structured Workflow output is unavailable")
		}
		value, ok := structured[strings.TrimPrefix(port, "structured.")]
		if !ok {
			return nil, fmt.Errorf("structured Workflow output field %q is unavailable", port)
		}
		return value, nil
	}
	value, ok := object[port]
	if !ok {
		return nil, fmt.Errorf("Workflow output port %q is unavailable", port)
	}
	return value, nil
}

func encodeToolOutput(result tool.Result) (json.RawMessage, error) {
	structured := json.RawMessage(`{}`)
	if len(result.Structured) > 0 {
		structured = result.Structured
	}
	return json.Marshal(map[string]any{"status": result.Status, "text": result.Text, "structured": structured, "artifacts": nonNilArtifacts(result.Artifacts), "citations": nonNilCitationRefs(result.Citations)})
}

func finalOutputs(run Run, steps []Step, currentStepID string, currentOutput json.RawMessage, nextOrdinal int) (bool, json.RawMessage, error) {
	if nextOrdinal < len(steps) {
		return false, json.RawMessage(`{}`), nil
	}
	stepMap := map[string]Step{}
	for _, step := range steps {
		if step.ID == currentStepID {
			step.Output, step.Status = currentOutput, StepCompleted
		}
		stepMap[step.NodeID] = step
	}
	outputs := map[string]json.RawMessage{}
	for _, output := range run.Compilation.Outputs {
		step, ok := stepMap[output.FromNode]
		if !ok {
			return false, nil, fmt.Errorf("Workflow output source is missing")
		}
		value, err := outputPort(step.Output, output.FromPort)
		if err != nil {
			if output.Required {
				return false, nil, err
			}
			continue
		}
		outputs[output.Name] = value
	}
	encoded, _ := json.Marshal(outputs)
	return true, encoded, nil
}

func definitionFromSnapshot(value ToolSnapshot) tool.Definition {
	permissions := []tool.PermissionRequirement{}
	_ = json.Unmarshal(value.Permissions, &permissions)
	return tool.Definition{QualifiedName: value.QualifiedName, Description: "Frozen Workflow Tool", InputSchema: cloneRaw(value.InputSchema), OutputSchema: cloneRaw(value.OutputSchema), Risk: tool.RiskLevel(value.Risk), Permissions: permissions, Idempotent: value.Idempotent, Version: value.Version}
}

func definitionSnapshotEqual(frozen, current tool.Definition) bool {
	frozen, current = tool.SnapshotDefinition(frozen), tool.SnapshotDefinition(current)
	if frozen.QualifiedName != current.QualifiedName || frozen.Version != current.Version || frozen.Risk != current.Risk || frozen.Idempotent != current.Idempotent || len(frozen.Permissions) != len(current.Permissions) {
		return false
	}
	for index := range frozen.Permissions {
		if frozen.Permissions[index] != current.Permissions[index] {
			return false
		}
	}
	return rawJSONEqual(frozen.InputSchema, current.InputSchema) && rawJSONEqual(frozen.OutputSchema, current.OutputSchema)
}

func rawJSONEqual(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftJSON, _ := json.Marshal(leftValue)
	rightJSON, _ := json.Marshal(rightValue)
	return string(leftJSON) == string(rightJSON)
}

func compilationNodeMap(compilation Compilation) map[string]CompiledNode {
	result := make(map[string]CompiledNode, len(compilation.Nodes))
	for _, node := range compilation.Nodes {
		result[node.ID] = node
	}
	return result
}

func currentStep(detail RunDetail) *Step {
	for index := range detail.Steps {
		if detail.Steps[index].Ordinal == detail.Run.CurrentStep {
			return &detail.Steps[index]
		}
	}
	return nil
}

func findStep(steps []Step, stepID string) *Step {
	for index := range steps {
		if steps[index].ID == stepID {
			return &steps[index]
		}
	}
	return nil
}

func stepByToolCall(steps []Step, callID string) *Step {
	for index := range steps {
		if steps[index].ToolCallID == callID {
			return &steps[index]
		}
	}
	return nil
}

func normalizeJSON(value, fallback json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return cloneRaw(fallback)
	}
	return cloneRaw(value)
}

func hashJSON(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func hashStrings(values ...string) string { return hashJSON([]byte(strings.Join(values, "\x00"))) }

func nonNilArtifacts(values []tool.ArtifactRef) []tool.ArtifactRef {
	if values == nil {
		return []tool.ArtifactRef{}
	}
	return values
}
func nonNilCitationRefs(values []tool.CitationRef) []tool.CitationRef {
	if values == nil {
		return []tool.CitationRef{}
	}
	return values
}
