package chat

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/events"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
)

type memoryRepo struct {
	mu        sync.Mutex
	run       Run
	messages  []conversation.Message
	envelopes []events.Envelope
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
func (m *memoryRepo) Update(_ context.Context, run Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.run = run
	return nil
}
func (m *memoryRepo) InterruptActive(context.Context, time.Time) (int64, error) { return 0, nil }
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
func (m *memoryRepo) AppendNext(_ context.Context, event events.Envelope) (events.Envelope, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	event.Sequence = int64(len(m.envelopes) + 1)
	m.envelopes = append(m.envelopes, event)
	return event, nil
}

type resolver struct{ model model.ChatModel }

func (r resolver) Resolve(context.Context, string, string) (model.ChatModel, error) {
	return r.model, nil
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
	stream, err := r.provider.Stream(ctx, buildRequest(r.repo.messages, run.AssistantMessageID, 120_000))
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
}

func (r *blockingRunner) Execute(context.Context, string) {
	close(r.started)
	<-r.release
}
func (r *blockingRunner) ResumeExecute(context.Context, string) { close(r.resume) }

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
	if err := service.Resume(context.Background(), "run"); err != nil {
		t.Fatal(err)
	}
	close(runner.release)
	select {
	case <-runner.resume:
	case <-time.After(time.Second):
		t.Fatal("queued resume was lost")
	}
	service.Close()
}
