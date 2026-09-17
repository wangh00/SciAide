package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/document"
	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/modelcap"
)

type memoryRepo struct {
	mu                  sync.Mutex
	run                 Run
	messages            []conversation.Message
	envelopes           []events.Envelope
	conversationMode    conversation.PermissionMode
	modelProfileID      string
	modelID             string
	workflowAI          bool
	workflowChatBlocked bool
}

func buildRequest(messages []conversation.Message, excludedMessageID string, maxChars int) model.ChatRequest {
	reversed := make([]model.Message, 0, len(messages))
	used := 0
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.ID == excludedMessageID {
			continue
		}
		var text strings.Builder
		for _, part := range message.Parts {
			if part.Type == "text" {
				text.WriteString(part.Text)
			}
		}
		if text.Len() == 0 {
			continue
		}
		length := len([]rune(text.String()))
		if used > 0 && used+length > maxChars {
			break
		}
		reversed = append(reversed, model.Message{Role: model.Role(message.Role), Content: text.String()})
		used += length
	}
	request := model.ChatRequest{Messages: make([]model.Message, len(reversed))}
	for index := range reversed {
		request.Messages[len(reversed)-1-index] = reversed[index]
	}
	return request
}

func TestNormalizeUsageQueryValidatesSingleDayTimeRange(t *testing.T) {
	valid := UsageQuery{StartDate: "2026-08-20", EndDate: "2026-08-20", StartTime: "15:50", EndTime: "17:30"}
	if err := normalizeUsageQuery(&valid); err != nil {
		t.Fatalf("valid time range: %v", err)
	}
	defaultStart := UsageQuery{StartDate: "2026-08-20", EndDate: "2026-08-20", EndTime: "17:30"}
	if err := normalizeUsageQuery(&defaultStart); err != nil || defaultStart.StartTime != "00:00" {
		t.Fatalf("defaulted time range = %#v, %v", defaultStart, err)
	}
	for _, invalid := range []UsageQuery{
		{StartDate: "2026-08-20", EndDate: "2026-08-21", StartTime: "15:50", EndTime: "17:30"},
		{StartDate: "2026-08-20", EndDate: "2026-08-20", StartTime: "17:31", EndTime: "17:30"},
		{StartDate: "2026-08-20", EndDate: "2026-08-20", StartTime: "25:00", EndTime: "26:00"},
	} {
		if err := normalizeUsageQuery(&invalid); err == nil {
			t.Fatalf("invalid time range accepted: %#v", invalid)
		}
	}
}

func (m *memoryRepo) CreateWithMessages(_ context.Context, run Run, user, assistant conversation.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.run = run
	m.messages = []conversation.Message{user, assistant}
	return nil
}
func (m *memoryRepo) Get(context.Context, string) (Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.run, nil
}
func (m *memoryRepo) LatestForConversation(context.Context, string) (Run, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.run, m.run.ID != "", nil
}
func (m *memoryRepo) IsWorkflowAIRun(context.Context, string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.workflowAI, nil
}
func (m *memoryRepo) BlocksOrdinaryChatForWorkflowConversation(context.Context, string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.workflowChatBlocked, nil
}
func (m *memoryRepo) Update(_ context.Context, run Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.run = run
	return nil
}
func (m *memoryRepo) IncrementModelTurns(_ context.Context, _ string, at time.Time) (Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.run.Status != RunRunning {
		return Run{}, fmt.Errorf("run is not running")
	}
	m.run.ModelTurns++
	m.run.UpdatedAt = at
	return m.run, nil
}
func (m *memoryRepo) InterruptActive(context.Context, time.Time) (int64, error) { return 0, nil }
func (m *memoryRepo) RecordModelUsage(context.Context, RequestUsage) (Run, bool, error) {
	return m.run, true, nil
}
func (m *memoryRepo) UsageDashboard(context.Context, UsageQuery) (UsageDashboard, error) {
	return UsageDashboard{}, nil
}
func (m *memoryRepo) UsageRequests(context.Context, UsageRequestQuery) (UsageRequestPage, error) {
	return UsageRequestPage{}, nil
}
func (m *memoryRepo) CancelRun(_ context.Context, runID, code, message string, at time.Time, event events.Envelope) (Run, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if isTerminal(m.run.Status) {
		return m.run, false, nil
	}
	m.run.Status, m.run.ErrorCode, m.run.ErrorMessage, m.run.CompletedAt, m.run.UpdatedAt = RunCancelled, code, message, &at, at
	for index := range m.messages {
		if m.messages[index].ID == m.run.AssistantMessageID {
			m.messages[index].Status = conversation.MessageIncomplete
			m.messages[index].UpdatedAt = at
		}
	}
	event.Sequence = int64(len(m.envelopes) + 1)
	m.envelopes = append(m.envelopes, event)
	return m.run, true, nil
}
func (m *memoryRepo) FailRun(_ context.Context, runID, code, message string, at time.Time, event events.Envelope) (Run, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if isTerminal(m.run.Status) {
		return m.run, false, nil
	}
	m.run.Status, m.run.ErrorCode, m.run.ErrorMessage, m.run.CompletedAt, m.run.UpdatedAt = RunFailed, code, message, &at, at
	for index := range m.messages {
		if m.messages[index].ID == m.run.AssistantMessageID {
			m.messages[index].Status = conversation.MessageFailed
			m.messages[index].UpdatedAt = at
		}
	}
	event.Sequence = int64(len(m.envelopes) + 1)
	m.envelopes = append(m.envelopes, event)
	return m.run, true, nil
}
func (m *memoryRepo) ListToolCallIDs(context.Context, string) ([]string, error) { return nil, nil }
func (m *memoryRepo) UpdateMessageText(_ context.Context, id string, status conversation.MessageStatus, text string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.messages {
		if m.messages[i].ID == id {
			m.messages[i].Status = status
			m.messages[i].Parts[0].Text = text
			m.messages[i].UpdatedAt = at
		}
	}
	return nil
}
func (m *memoryRepo) ListMessages(context.Context, string, int) ([]conversation.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]conversation.Message, len(m.messages))
	copy(result, m.messages)
	return result, nil
}
func (m *memoryRepo) GetConversation(context.Context, string) (conversation.Conversation, error) {
	mode := m.conversationMode
	if !mode.Valid() {
		mode = conversation.PermissionPlan
	}
	return conversation.Conversation{ID: "conversation", ProjectID: "project", PermissionMode: mode}, nil
}

func (m *memoryRepo) UpdateReasoningLevel(_ context.Context, _ string, level modelcap.ReasoningLevel, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return nil
}
func (m *memoryRepo) UpdateModelSelection(_ context.Context, _ string, modelProfileID, modelID string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.modelProfileID = modelProfileID
	m.modelID = modelID
	return nil
}
func (m *memoryRepo) AppendNext(_ context.Context, event events.Envelope) (events.Envelope, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	event.Sequence = int64(len(m.envelopes) + 1)
	m.envelopes = append(m.envelopes, event)
	return event, nil
}

type testRunner struct {
	repo     *memoryRepo
	provider model.ChatModel
}

func (r testRunner) Execute(ctx context.Context, runID string) {
	run, _ := r.repo.Get(ctx, runID)
	now := time.Now().UTC()
	run.Status, run.StartedAt, run.UpdatedAt = RunRunning, &now, now
	_ = r.repo.Update(ctx, run)
	stream, err := r.provider.Stream(ctx, buildRequest(r.repo.messages, run.AssistantMessageID, 200_000))
	if err != nil {
		return
	}
	defer stream.Close()
	text := ""
	for {
		event, recvErr := stream.Recv()
		if event.Type == model.EventTextDelta {
			text += event.Text
		}
		if event.FinishReason != "" {
			run.FinishReason = event.FinishReason
		}
		if recvErr != nil || event.Type == model.EventDone {
			break
		}
	}
	now = time.Now().UTC()
	_ = r.repo.UpdateMessageText(ctx, run.AssistantMessageID, conversation.MessageComplete, text, now)
	run.Status, run.CompletedAt, run.UpdatedAt = RunCompleted, &now, now
	_ = r.repo.Update(ctx, run)
}

func (r testRunner) ResumeExecute(ctx context.Context, runID string) { r.Execute(ctx, runID) }

type blockingRunner struct {
	started chan struct{}
	release chan struct{}
	resume  chan struct{}
	once    sync.Once
	mu      sync.Mutex
	count   int
}

type noopRunner struct{}

func (noopRunner) Execute(context.Context, string)       {}
func (noopRunner) ResumeExecute(context.Context, string) {}

type attachmentResolverFixture struct{}

func (attachmentResolverFixture) Resolve(_ context.Context, projectID string, ids []string) ([]attachment.MessageReference, error) {
	if projectID != "project" || len(ids) != 1 || ids[0] != "paper" {
		return nil, fmt.Errorf("unexpected attachment resolution")
	}
	return []attachment.MessageReference{{AttachmentID: "paper", OriginalName: "paper.pdf", MIMEType: "application/pdf", Format: document.FormatPDF, SizeBytes: 42, UnitCount: 2}}, nil
}

func (f attachmentResolverFixture) ResolveForConversation(ctx context.Context, projectID, conversationID string, ids []string) ([]attachment.MessageReference, error) {
	if conversationID != "conversation" {
		return nil, fmt.Errorf("unexpected conversation resolution")
	}
	return f.Resolve(ctx, projectID, ids)
}

func (r *blockingRunner) Execute(context.Context, string) {
	close(r.started)
	<-r.release
}
func (r *blockingRunner) ResumeExecute(context.Context, string) {
	r.mu.Lock()
	r.count++
	r.mu.Unlock()
	r.once.Do(func() { close(r.resume) })
}

func TestServiceCompletesAndPersistsBeforeTerminalEvent(t *testing.T) {
	repo := &memoryRepo{}
	provider := fake.New([]fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "科研"}}, {Event: model.Event{Type: model.EventTextDelta, Text: "助手"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}})
	service := NewService(repo, repo, repo, nil)
	if err := service.SetRunner(testRunner{repo: repo, provider: provider}); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	run, err := service.Start(context.Background(), StartCommand{ConversationID: "conversation", ModelProfileID: "profile", ModelID: "fixture", Text: "你好"})
	if err != nil {
		t.Fatalf("Start() error=%v", err)
	}
	if run.ModelID != "fixture" {
		t.Fatalf("run model snapshot=%q", run.ModelID)
	}
	repo.mu.Lock()
	persistedProfileID, persistedModelID := repo.modelProfileID, repo.modelID
	repo.mu.Unlock()
	if persistedProfileID != "profile" || persistedModelID != "fixture" {
		t.Fatalf("conversation model selection=(%q,%q)", persistedProfileID, persistedModelID)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, _ := service.Snapshot(context.Background(), run.ID)
		if snapshot.Run.Status == RunCompleted {
			if got := snapshot.Messages[1].Parts[0].Text; got != "科研助手" {
				t.Fatalf("text=%q", got)
			}
			if snapshot.Run.FinishReason != "stop" {
				t.Fatalf("finish reason=%q", snapshot.Run.FinishReason)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run did not complete")
}

func TestServicePersistsAttachmentMediaPartWithoutMessageText(t *testing.T) {
	repo := &memoryRepo{}
	service := NewService(repo, repo, repo, nil)
	if err := service.SetRunner(noopRunner{}); err != nil {
		t.Fatal(err)
	}
	if err := service.SetAttachmentResolver(attachmentResolverFixture{}); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if _, err := service.Start(context.Background(), StartCommand{ConversationID: "conversation", ModelProfileID: "profile", ModelID: "fixture", AttachmentIDs: []string{"paper"}}); err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.messages) != 2 || len(repo.messages[0].Parts) != 1 || repo.messages[0].Parts[0].Type != "media" {
		t.Fatalf("messages = %#v", repo.messages)
	}
	var reference attachment.MessageReference
	if err := json.Unmarshal(repo.messages[0].Parts[0].Payload, &reference); err != nil || reference.AttachmentID != "paper" {
		t.Fatalf("media payload = %#v, %v", reference, err)
	}
}

func TestBuildRequestKeepsNewestMessagesWithinBudget(t *testing.T) {
	messages := []conversation.Message{
		{ID: "old", Role: conversation.RoleUser, Parts: []conversation.MessagePart{{Type: "text", Text: "12345"}}},
		{ID: "new", Role: conversation.RoleAssistant, Parts: []conversation.MessagePart{{Type: "text", Text: "67890"}}},
	}
	request := buildRequest(messages, "", 5)
	if len(request.Messages) != 1 || request.Messages[0].Content != "67890" {
		t.Fatalf("request = %#v", request)
	}
}

func TestResumeQueuedWhileOriginalRunnerExits(t *testing.T) {
	repo := &memoryRepo{run: Run{ID: "run", Status: RunRunning}}
	runner := &blockingRunner{started: make(chan struct{}), release: make(chan struct{}), resume: make(chan struct{})}
	service := NewService(repo, repo, repo, nil)
	if err := service.SetRunner(runner); err != nil {
		t.Fatal(err)
	}
	if err := service.launch("run", false); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if err := service.Resume(context.Background(), "run", "approval-1"); err != nil {
		t.Fatal(err)
	}
	if err := service.Resume(context.Background(), "run", "approval-1"); err != nil {
		t.Fatal(err)
	}
	close(runner.release)
	select {
	case <-runner.resume:
	case <-time.After(time.Second):
		t.Fatal("queued resume was lost")
	}
	if err := service.Resume(context.Background(), "run", "approval-1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	runner.mu.Lock()
	count := runner.count
	runner.mu.Unlock()
	if count != 1 {
		t.Fatalf("resume executions = %d, want 1", count)
	}
	service.Close()
}

func TestResumeCanScheduleLaterApprovalCycle(t *testing.T) {
	repo := &memoryRepo{run: Run{ID: "run", Status: RunRunning}}
	runner := &blockingResumeRunner{started: make(chan struct{}, 2), release: make(chan struct{}, 2)}
	service := NewService(repo, repo, repo, nil)
	if err := service.SetRunner(runner); err != nil {
		t.Fatal(err)
	}
	for cycle := 1; cycle <= 2; cycle++ {
		if err := service.Resume(context.Background(), "run", fmt.Sprintf("approval-%d", cycle)); err != nil {
			t.Fatal(err)
		}
		select {
		case <-runner.started:
		case <-time.After(time.Second):
			t.Fatalf("resume cycle %d did not start", cycle)
		}
		runner.release <- struct{}{}
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			service.mu.Lock()
			active := service.active["run"] != nil
			service.mu.Unlock()
			if !active {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	runner.mu.Lock()
	count := runner.count
	runner.mu.Unlock()
	if count != 2 {
		t.Fatalf("resume executions = %d, want 2", count)
	}
	service.Close()
}

type blockingResumeRunner struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	count   int
}

func (*blockingResumeRunner) Execute(context.Context, string) {}

func (r *blockingResumeRunner) ResumeExecute(context.Context, string) {
	r.mu.Lock()
	r.count++
	r.mu.Unlock()
	r.started <- struct{}{}
	<-r.release
}

func TestCancelWaitingApprovalUsesDurableTerminator(t *testing.T) {
	now := time.Now().UTC()
	repo := &memoryRepo{run: Run{ID: "run", AssistantMessageID: "assistant", Status: RunWaitingApproval}, messages: []conversation.Message{{ID: "assistant", Status: conversation.MessageStreaming}}}
	service := NewService(repo, repo, repo, nil)
	if err := service.SetTerminator(NewTerminator(repo, nil)); err != nil {
		t.Fatal(err)
	}
	service.terminator.now = func() time.Time { return now }
	if err := service.Cancel(context.Background(), "run"); err != nil {
		t.Fatal(err)
	}
	if repo.run.Status != RunCancelled || repo.messages[0].Status != conversation.MessageIncomplete || len(repo.envelopes) != 1 || repo.envelopes[0].Type != "run.cancelled" {
		t.Fatalf("cancelled state = %#v, %#v, %#v", repo.run, repo.messages, repo.envelopes)
	}
}

func TestStartCapturesConversationPermissionMode(t *testing.T) {
	repo := &memoryRepo{conversationMode: conversation.PermissionFullAccess}
	service := NewService(repo, repo, repo, nil)
	if err := service.SetRunner(&blockingStartRunner{started: make(chan struct{})}); err != nil {
		t.Fatal(err)
	}
	run, err := service.Start(context.Background(), StartCommand{ConversationID: "conversation", ModelProfileID: "profile", ModelID: "model", Text: "question"})
	if err != nil {
		t.Fatal(err)
	}
	if run.PermissionMode != conversation.PermissionFullAccess || repo.run.PermissionMode != conversation.PermissionFullAccess {
		t.Fatalf("permission mode was not captured: %#v", run)
	}
	service.Close()
}

func TestSteerCancelsActiveRunBeforeStartingReplacement(t *testing.T) {
	repo := &memoryRepo{run: Run{ID: "old", ConversationID: "conversation", Status: RunWaitingApproval}, conversationMode: conversation.PermissionPlan}
	service := NewService(repo, repo, repo, nil)
	if err := service.SetRunner(&blockingStartRunner{started: make(chan struct{})}); err != nil {
		t.Fatal(err)
	}
	if err := service.SetTerminator(NewTerminator(repo, nil)); err != nil {
		t.Fatal(err)
	}
	replacement, err := service.Steer(context.Background(), "old", StartCommand{ConversationID: "conversation", ModelProfileID: "profile", ModelID: "model", Text: "new direction"})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID == "old" || replacement.Status != RunQueued {
		t.Fatalf("replacement = %#v", replacement)
	}
	if len(repo.envelopes) != 1 || repo.envelopes[0].Type != "run.cancelled" {
		t.Fatalf("old run was not durably cancelled: %#v", repo.envelopes)
	}
	service.Close()
}

func TestSteerRejectsStaleTerminalRunID(t *testing.T) {
	repo := &memoryRepo{run: Run{ID: "old", ConversationID: "conversation", Status: RunCompleted}, conversationMode: conversation.PermissionPlan}
	service := NewService(repo, repo, repo, nil)
	if err := service.SetRunner(&blockingStartRunner{started: make(chan struct{})}); err != nil {
		t.Fatal(err)
	}
	if err := service.SetTerminator(NewTerminator(repo, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Steer(context.Background(), "old", StartCommand{ConversationID: "conversation", ModelProfileID: "profile", ModelID: "model", Text: "new direction"}); err == nil {
		t.Fatal("stale terminal run was accepted as an active steer target")
	}
	if len(repo.envelopes) != 0 {
		t.Fatalf("terminal run produced cancellation events: %#v", repo.envelopes)
	}
}

func TestSteerDoesNotInterruptWorkflowAIRun(t *testing.T) {
	repo := &memoryRepo{
		run:              Run{ID: "workflow-ai", ConversationID: "conversation", Status: RunRunning},
		conversationMode: conversation.PermissionFullAccess,
		workflowAI:       true,
	}
	service := NewService(repo, repo, repo, nil)
	if err := service.SetRunner(&blockingStartRunner{started: make(chan struct{})}); err != nil {
		t.Fatal(err)
	}
	if err := service.SetTerminator(NewTerminator(repo, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Steer(context.Background(), "workflow-ai", StartCommand{ConversationID: "conversation", ModelProfileID: "profile", ModelID: "model", Text: "旁路问题"}); err == nil || !strings.Contains(err.Error(), "不能用普通对话中断") {
		t.Fatalf("Workflow AI steer error = %v", err)
	}
	if len(repo.envelopes) != 0 || repo.run.Status != RunRunning {
		t.Fatalf("Workflow AI run was mutated: run=%#v events=%#v", repo.run, repo.envelopes)
	}
	snapshot, err := service.Snapshot(context.Background(), "workflow-ai")
	if err != nil || !snapshot.WorkflowAI {
		t.Fatalf("Workflow AI snapshot = %#v, %v", snapshot, err)
	}
}

func TestStartDoesNotOccupyActiveWorkflowConversation(t *testing.T) {
	repo := &memoryRepo{conversationMode: conversation.PermissionFullAccess, workflowChatBlocked: true}
	service := NewService(repo, repo, repo, nil)
	if err := service.SetRunner(noopRunner{}); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if _, err := service.Start(context.Background(), StartCommand{ConversationID: "conversation", ModelProfileID: "profile", ModelID: "model", Text: "研究进行到哪里了？"}); err == nil || !strings.Contains(err.Error(), "科研任务仍在执行") {
		t.Fatalf("active Workflow conversation start error = %v", err)
	}
	if repo.run.ID != "" || len(repo.messages) != 0 {
		t.Fatalf("active Workflow conversation was mutated: run=%#v messages=%#v", repo.run, repo.messages)
	}
}

type blockingStartRunner struct{ started chan struct{} }

func (r *blockingStartRunner) Execute(ctx context.Context, _ string) {
	close(r.started)
	<-ctx.Done()
}
func (*blockingStartRunner) ResumeExecute(context.Context, string) {}
