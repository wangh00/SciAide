package workflowai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/platform/localexec"
)

type conversationLoader interface {
	GetConversation(ctx context.Context, id string) (conversation.Conversation, error)
}

type Bridge struct {
	chats           *chat.Service
	conversations   conversationLoader
	approvals       approvalReader
	processes       processRuntimeReader
	processReaders  []processRuntimeReader
	activityCacheMu sync.Mutex
	activityCache   map[string]cachedActivity
	activityOrder   []string
	activityBytes   int
}

type approvalReader interface {
	ListPending(context.Context, string) ([]permission.Approval, error)
}

type processRuntimeReader interface {
	Runtime(string) (localexec.RuntimeSnapshot, bool)
}

func New(chats *chat.Service, conversations conversationLoader, approvals ...approvalReader) (*Bridge, error) {
	if chats == nil || conversations == nil {
		return nil, fmt.Errorf("Workflow AI bridge is not configured")
	}
	var reader approvalReader
	if len(approvals) > 0 {
		reader = approvals[0]
	}
	return &Bridge{chats: chats, conversations: conversations, approvals: reader}, nil
}

// SetProcessRuntimeReader connects the transient local process observer. It is
// optional so workflow tests and headless integrations remain lightweight.
func (b *Bridge) SetProcessRuntimeReader(reader processRuntimeReader) {
	if b != nil {
		b.processes = reader
		b.processReaders = nil
		if reader != nil {
			b.processReaders = append(b.processReaders, reader)
		}
	}
}

func (b *Bridge) AddProcessRuntimeReader(reader processRuntimeReader) {
	if b == nil || reader == nil {
		return
	}
	b.processReaders = append(b.processReaders, reader)
}

func (b *Bridge) runtimeForCall(callID string) (localexec.RuntimeSnapshot, bool) {
	if b == nil {
		return localexec.RuntimeSnapshot{}, false
	}
	readers := b.processReaders
	if len(readers) == 0 && b.processes != nil {
		readers = []processRuntimeReader{b.processes}
	}
	for _, reader := range readers {
		if snapshot, ok := reader.Runtime(callID); ok {
			return snapshot, true
		}
	}
	return localexec.RuntimeSnapshot{}, false
}

func (b *Bridge) Start(ctx context.Context, command workflow.AIStartCommand) (workflow.AIStageState, error) {
	selected, err := b.conversations.GetConversation(ctx, command.ConversationID)
	if err != nil {
		return workflow.AIStageState{}, fmt.Errorf("load research conversation model: %w", err)
	}
	if strings.TrimSpace(selected.ModelProfileID) == "" || strings.TrimSpace(selected.ModelID) == "" {
		return workflow.AIStageState{}, fmt.Errorf("科研会话尚未选择协作模型")
	}
	allowed, err := json.Marshal(command.AllowedTools)
	if err != nil {
		return workflow.AIStageState{}, err
	}
	run, err := b.chats.StartWorkflowAI(ctx, chat.StartCommand{
		ConversationID: command.ConversationID, ModelProfileID: selected.ModelProfileID, ModelID: selected.ModelID,
		ReasoningLevel: selected.ReasoningLevel, Text: command.PromptText,
	}, chat.WorkflowAIExecution{
		ID: command.ExecutionID, WorkflowRunID: command.WorkflowRunID, WorkflowStepID: command.WorkflowStepID, Attempt: command.Attempt,
		NodeKind: string(command.NodeKind), PromptVersion: command.PromptVersion, PromptText: command.PromptText, PromptSHA256: command.PromptSHA256,
		InputSHA256: command.InputSHA256, AllowedTools: allowed, OutputSchema: command.OutputSchema, OutputSchemaSHA256: command.OutputSchemaSHA256,
		Citations: append([]tool.CitationRef(nil), command.Citations...),
	})
	if err != nil {
		return workflow.AIStageState{}, err
	}
	return stateFromRun(command.ExecutionID, run, ""), nil
}

func (b *Bridge) Get(ctx context.Context, execution workflow.AIExecution) (workflow.AIStageState, error) {
	snapshot, err := b.chats.StageSnapshot(ctx, execution.ChatRunID)
	if err != nil {
		return workflow.AIStageState{}, err
	}
	text := stageText(snapshot)
	return stateFromRun(execution.ID, snapshot.Run, text), nil
}

func (b *Bridge) Latest(ctx context.Context, conversationID string) (workflow.AIStageState, bool, error) {
	snapshot, err := b.chats.LatestSnapshot(ctx, conversationID)
	if err != nil {
		return workflow.AIStageState{}, false, err
	}
	if snapshot == nil {
		return workflow.AIStageState{}, false, nil
	}
	text := stageText(*snapshot)
	return stateFromRun("", snapshot.Run, text), true, nil
}

// stageText reads the durable assistant message first. Older Workflow AI
// runs (and a few gateways) may have labelled the final visible answer as
// commentary; the agent persists that text in RunSteps, so use the latest
// completed step as a narrow recovery path rather than treating a completed
// run as an empty response. This does not affect ordinary chat recovery.
func stageText(snapshot chat.Snapshot) string {
	var text strings.Builder
	for _, message := range snapshot.Messages {
		if message.ID != snapshot.Run.AssistantMessageID {
			continue
		}
		for _, part := range message.Parts {
			if part.Type == "text" {
				text.WriteString(part.Text)
			}
		}
		break
	}
	if value := strings.TrimSpace(text.String()); value != "" {
		return value
	}
	if len(snapshot.RunSteps) == 0 {
		return ""
	}
	for index := len(snapshot.RunSteps) - 1; index >= 0; index-- {
		if value := strings.TrimSpace(snapshot.RunSteps[index].Commentary); value != "" {
			return value
		}
	}
	return ""
}

func (b *Bridge) Cancel(ctx context.Context, execution workflow.AIExecution) error {
	if strings.TrimSpace(execution.ChatRunID) == "" {
		return nil
	}
	return b.chats.Cancel(ctx, execution.ChatRunID)
}

// Activity reads durable status/tools without fetching unrelated conversation text.
// All tool calls are included, so the host need not load them again as a fallback.
func (b *Bridge) CompleteActivityToolHistory() bool { return true }

func (b *Bridge) Activity(ctx context.Context, execution workflow.AIExecution) (workflow.AIStageActivity, error) {
	if strings.TrimSpace(execution.ChatRunID) == "" {
		return workflow.AIStageActivity{}, fmt.Errorf("Workflow AI Chat Run is not bound")
	}
	revision, err := b.chats.ActivityRevision(ctx, execution.ChatRunID)
	if err != nil {
		return workflow.AIStageActivity{}, err
	}
	if revision != "" {
		metadata, _ := json.Marshal(struct {
			ID, StepID, Status, Error string
			Attempt                   int
			StartedAt, CompletedAt    *time.Time
			UpdatedAt                 time.Time
		}{execution.ID, execution.WorkflowStepID, execution.Status, execution.ErrorMessage, execution.Attempt, execution.StartedAt, execution.CompletedAt, execution.UpdatedAt})
		revision += ":" + string(metadata)
		if cached, ok := b.loadActivity(execution.ChatRunID, revision); ok {
			return cached, nil
		}
	}
	snapshot, err := b.chats.ActivitySnapshot(ctx, execution.ChatRunID)
	if err != nil {
		return workflow.AIStageActivity{}, err
	}
	activity := workflow.AIStageActivity{
		ExecutionID: execution.ID, WorkflowStepID: execution.WorkflowStepID, ChatRunID: execution.ChatRunID,
		Status: execution.Status, ChatStatus: string(snapshot.Run.Status), ModelID: snapshot.Run.ModelID,
		ModelTurns: snapshot.Run.ModelTurns, InputTokens: snapshot.Run.InputTokens,
		OutputTokens: snapshot.Run.OutputTokens, ReasoningTokens: snapshot.Run.ReasoningTokens,
		StartedAt: execution.StartedAt, UpdatedAt: snapshot.Run.UpdatedAt,
		ToolCalls: make([]workflow.AIStageToolActivity, 0, len(snapshot.ToolCalls)),
	}
	if snapshot.ModelTurnStreaming {
		activity.CurrentDraft = compactVisibleDraft(snapshot.ModelTurnDraft)
	}
	if activity.StartedAt == nil {
		activity.StartedAt = snapshot.Run.StartedAt
	}
	if activity.UpdatedAt.IsZero() {
		activity.UpdatedAt = execution.UpdatedAt
	}
	if activity.StartedAt != nil {
		end := time.Now().UTC()
		if snapshot.Run.CompletedAt != nil {
			end = *snapshot.Run.CompletedAt
		} else if execution.CompletedAt != nil {
			end = *execution.CompletedAt
		}
		activity.ElapsedSeconds = maxInt(0, int(end.Sub(*activity.StartedAt).Seconds()))
	}
	toolCalls := snapshot.ToolCalls
	labels := issuedActivityLabels(snapshot.ToolCalls)
	for _, call := range toolCalls {
		summary := safeToolSummary(call)
		if call.ToolName == "builtin.resource.open" || call.ToolName == "builtin.resource.search" {
			var a struct {
				ActionID string `json:"actionId"`
			}
			if json.Unmarshal(call.Arguments, &a) == nil && labels[a.ActionID] != "" {
				summary = labels[a.ActionID]
			}
		}
		item := workflow.AIStageToolActivity{
			ID: call.ID, ToolName: call.ToolName, Status: call.Status, Risk: call.Risk,
			Summary: tool.SafeActivityText(summary, 180), Arguments: tool.SafeActivityArguments(call.Arguments), Permissions: tool.SafeActivityPermissions(call.Permissions), CreatedAt: call.CreatedAt, StartedAt: call.StartedAt,
			CompletedAt: call.CompletedAt, ErrorCode: tool.SafeActivityText(call.ErrorCode, 120), ErrorMessage: tool.SafeActivityText(call.ErrorMessage, 500),
		}
		if call.Result != nil {
			item.DurationMillis = call.Result.Meta.DurationMillis
			item.Truncated = call.Result.Truncated
			item.OutputSummary = compactToolResult(call.Result.Text)
		} else if call.StartedAt != nil && call.Status == tool.CallRunning {
			item.DurationMillis = time.Since(*call.StartedAt).Milliseconds()
			// Persistent Kernel stdout is a private JSON protocol. It is consumed
			// by the Kernel runtime and must not be projected as raw live logs.
			if len(b.processReaders) > 0 && (call.ToolName == "builtin.python.execute" || call.ToolName == "builtin.shell.execute") {
				if live, exists := b.runtimeForCall(call.ID); exists {
					item.ProcessID = live.PID
					item.StdoutTail, item.StderrTail = compactLiveOutput(live.StdoutTail), compactLiveOutput(live.StderrTail)
					item.StdoutBytes, item.StderrBytes = live.StdoutBytes, live.StderrBytes
					updated := live.UpdatedAt
					item.LiveUpdatedAt = &updated
				}
			}
		}
		activity.ToolCalls = append(activity.ToolCalls, item)
	}
	if b.approvals != nil {
		if approvals, approvalErr := b.approvals.ListPending(ctx, execution.ChatRunID); approvalErr == nil {
			activity.PendingApprovals = permission.SafeApprovals(approvals)
		} else {
			activity.PendingApprovals = []permission.Approval{}
			revision = ""
		}
	}
	activity.CurrentAction = currentAction(snapshot, activity)
	if snapshot.Run.ErrorMessage != "" {
		activity.LastError = tool.SafeActivityText(snapshot.Run.ErrorMessage, 500)
	} else if execution.ErrorMessage != "" {
		activity.LastError = tool.SafeActivityText(execution.ErrorMessage, 500)
	}
	if revision != "" && !snapshot.ModelTurnStreaming && len(activity.PendingApprovals) == 0 {
		terminal := true
		for _, call := range snapshot.ToolCalls {
			terminal = terminal && call.Status.Terminal()
		}
		// Do not cache a snapshot across a concurrent durable-state transition.
		current, checkErr := b.chats.ActivityRevision(ctx, execution.ChatRunID)
		if terminal && checkErr == nil && strings.HasPrefix(revision, current+":") && current != "" {
			b.saveActivity(execution.ChatRunID, revision, activity)
		}
	}
	return activity, nil
}

func currentAction(snapshot chat.Snapshot, activity workflow.AIStageActivity) string {
	for index := len(activity.ToolCalls) - 1; index >= 0; index-- {
		call := activity.ToolCalls[index]
		switch call.Status {
		case tool.CallPending, tool.CallAwaitingApproval:
			return "等待工具授权：" + call.ToolName
		case tool.CallRunning:
			return "正在执行：" + call.ToolName
		}
	}
	switch snapshot.Run.Status {
	case chat.RunQueued:
		return "正在排队"
	case chat.RunRunning:
		return "正在等待 AI 响应"
	case chat.RunWaitingApproval:
		return "等待工具授权"
	case chat.RunCompleted:
		return "AI 阶段已返回，正在核验结果"
	case chat.RunFailed, chat.RunCancelled, chat.RunInterrupted:
		return "AI 阶段已停止"
	default:
		return "正在处理"
	}
}

func safeToolSummary(call tool.Call) string {
	var args map[string]json.RawMessage
	_ = json.Unmarshal(call.Arguments, &args)
	if call.ToolName == "builtin.resource.open" || call.ToolName == "builtin.resource.search" {
		if label := tool.ResourceActivityLabel(call); label != "" {
			return label
		}
		return "操作任务资源 · " + safeArgumentText(args, "actionId")
	}
	if call.ToolName == "builtin.python.execute" {
		if len(args["scriptPath"]) > 0 {
			var path string
			if json.Unmarshal(args["scriptPath"], &path) == nil && strings.TrimSpace(path) != "" {
				return "Python 脚本 · " + tool.RedactActivityText(path)
			}
		}
		return "Python 代码执行"
	}
	if call.ToolName == "builtin.python.kernel.execute" {
		return "Python Kernel 鎵ц"
	}
	if call.ToolName == "builtin.shell.execute" {
		var workdir string
		_ = json.Unmarshal(args["workdir"], &workdir)
		if strings.TrimSpace(workdir) != "" {
			return "Shell 命令 · " + workdir
		}
		return "Shell 命令执行"
	}
	if call.ToolName == "builtin.workspace.read_text" || call.ToolName == "builtin.workspace.list" {
		path := safeArgumentText(args, "path")
		if path != "" {
			if call.ToolName == "builtin.workspace.list" {
				return "浏览 Workspace · " + tool.RedactActivityText(path)
			}
			return "读取 Workspace 文件 · " + tool.RedactActivityText(path)
		}
		return "读取 Workspace"
	}
	if strings.HasPrefix(call.ToolName, "builtin.document.") {
		attachmentID := safeArgumentText(args, "attachmentId")
		locator := safeArgumentText(args, "locator")
		if locator != "" {
			return "处理文档 · " + tool.RedactActivityText(locator)
		}
		if attachmentID != "" {
			return "处理文档 · " + tool.RedactActivityText(attachmentID)
		}
		return "处理项目文档"
	}
	if call.ToolName == "builtin.skill.load" {
		name := safeArgumentText(args, "name")
		if name != "" {
			return "加载 Skill · " + name
		}
		return "浏览 Skill 目录"
	}
	if strings.HasPrefix(call.ToolName, "builtin.skill.resource.") {
		name := safeArgumentText(args, "name")
		resource := safeArgumentText(args, "resourcePath")
		if resource == "" {
			resource = safeArgumentText(args, "section")
		}
		if name != "" && resource != "" {
			return "读取 Skill 资料 · " + tool.RedactActivityText(name) + " / " + tool.RedactActivityText(resource)
		}
		if name != "" {
			return "读取 Skill 资料 · " + tool.RedactActivityText(name)
		}
		return "读取 Skill 资料"
	}
	if call.ToolName == "builtin.knowledge.search" || call.ToolName == "builtin.research.search" || call.ToolName == "builtin.research.workflow.search" {
		label := "在线学术检索"
		if call.ToolName == "builtin.knowledge.search" {
			label = "本地知识库检索"
		}
		query := safeArgumentText(args, "query")
		if query != "" {
			return label + " · " + tool.RedactActivityText(query)
		}
		return label
	}
	if call.ToolName == "builtin.research.workflow.review.gate" {
		return "核验研究交付条件"
	}
	if index := strings.LastIndex(call.ToolName, "."); index >= 0 && index+1 < len(call.ToolName) {
		return "调用 " + call.ToolName[index+1:]
	}
	return "调用工具"
}

func safeArgumentText(args map[string]json.RawMessage, key string) string {
	raw, ok := args[key]
	if !ok {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	value = tool.RedactActivityText(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
	if len([]rune(value)) > 120 {
		value = string([]rune(value)[:120]) + "…"
	}
	return value
}

func compactToolResult(value string) string {
	value = tool.RedactActivityText(value)
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	runes := []rune(value)
	if len(runes) > 180 {
		return string(runes[:180]) + "…"
	}
	return value
}

func compactLiveOutput(value string) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "\uFFFD"))
	if value == "" {
		return ""
	}
	// Preserve line breaks for readable stdout/stderr tails while bounding the
	// projection independently from the durable tool result.
	lines := strings.Split(value, "\n")
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	value = strings.Join(lines, "\n")
	value = tool.SafeActivityText(value, 900)
	if len([]rune(value)) > 900 {
		runes := []rune(value)
		value = string(runes[len(runes)-900:])
	}
	return value
}

func compactVisibleDraft(value string) string {
	value = tool.RedactActivityText(value)
	lower := strings.ToLower(value)
	if marker := strings.LastIndex(lower, "```json"); marker >= 0 {
		value = value[:marker]
	}
	return compactToolResult(value)
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func stateFromRun(executionID string, run chat.Run, text string) workflow.AIStageState {
	return workflow.AIStageState{
		ExecutionID: executionID, ChatRunID: run.ID, Status: string(run.Status), ModelProfileID: run.ModelProfileID, ModelID: run.ModelID,
		ReasoningLevel: run.RequestedReasoningLevel, Text: text, InputTokens: run.InputTokens, OutputTokens: run.OutputTokens,
		ReasoningTokens: run.ReasoningTokens, ModelTurns: run.ModelTurns, ErrorCode: run.ErrorCode, ErrorMessage: run.ErrorMessage,
	}
}

// Recover labels from issued menus too, so pending and rejected calls are not
// presented as generic "open" operations. Labels remain untrusted display data.
func issuedActivityLabels(calls []tool.Call) map[string]string {
	labels := map[string]string{}
	for _, call := range calls {
		if call.Result == nil || (call.ToolName != "builtin.resource.open" && call.ToolName != "builtin.resource.search") {
			continue
		}
		var result struct {
			ActionID string `json:"actionId"`
			Label    string `json:"label"`
			Actions  []struct {
				ActionID string `json:"actionId"`
				Label    string `json:"label"`
			} `json:"actions"`
		}
		if json.Unmarshal(call.Result.Structured, &result) != nil {
			continue
		}
		if result.ActionID != "" {
			labels[result.ActionID] = tool.SafeActivityText(result.Label, 180)
		}
		for _, a := range result.Actions {
			if a.ActionID != "" {
				labels[a.ActionID] = tool.SafeActivityText(a.Label, 180)
			}
		}
	}
	return labels
}
