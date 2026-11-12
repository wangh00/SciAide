package agent

import (
	"context"
	"encoding/json"
	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"strings"
	"testing"
)

func TestManualCompactionAfterInternalRunSummarizesOnlyDiscussion(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "Human objective retained."}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}, []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "hello"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}})
	state.workflowAI = true
	state.run.Status = chat.RunCompleted
	state.run.APIProtocol = "openai_chat_completions"
	for i := range state.messages {
		state.messages[i].Internal = true
		state.messages[i].Parts[0].Text = "INTERNAL_FROZEN_SCHEMA"
	}
	human := conversation.Message{ID: "human-earlier", ConversationID: "earlier-conversation", RunID: "earlier-run", Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: strings.Repeat("Human objective: compare data without changing the outcome. ", 20)}}}
	state.messages = append([]conversation.Message{human}, state.messages...)
	repo := &memoryCheckpointRepository{}
	loop.checkpoints = contextmemory.NewService(repo)
	// A previous internal-boundary checkpoint must be rebuilt, not summarized again.
	_, err := loop.checkpoints.Save(context.Background(), contextmemory.Checkpoint{ConversationID: "conversation", ThroughMessageID: "assistant", Summary: "OLD_INTERNAL_SUMMARY", SourceMessageCount: 2, SourceEstimatedTokens: 100, APIProtocol: "openai_chat_completions"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := loop.CompactConversation(context.Background(), "conversation")
	if err != nil || !result.Complete || result.ThroughMessageID != human.ID || result.SourceMessageCount != 1 {
		t.Fatal(result, err)
	}
	raw, _ := json.Marshal(provider.Requests()[0])
	if strings.Contains(string(raw), "INTERNAL_") || !strings.Contains(string(raw), "Human objective") {
		t.Fatal("wrong summary source", string(raw))
	}
	state.workflowAI = false
	state.run.ID = "discussion"
	state.run.Status = chat.RunQueued
	state.run.UserMessageID = "new-user"
	state.run.AssistantMessageID = "new-assistant"
	state.messages = append(state.messages, conversation.Message{ID: "new-user", ConversationID: "conversation", RunID: "discussion", Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: "hello"}}}, conversation.Message{ID: "new-assistant", ConversationID: "conversation", RunID: "discussion", Role: conversation.RoleAssistant, Status: conversation.MessageStreaming, Parts: []conversation.MessagePart{{Type: "text"}}})
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted {
		t.Fatal(state.run)
	}
	raw, _ = json.Marshal(provider.Requests()[1])
	if strings.Contains(string(raw), "INTERNAL_") || !strings.Contains(string(raw), "Human objective retained") {
		t.Fatal("internal audit leaked into discussion", string(raw))
	}
}

func TestManualCompactionOnlyInternalContentDoesNotCallModel(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil)
	state.run.Status = chat.RunCompleted
	state.workflowAI = true
	for i := range state.messages {
		state.messages[i].Internal = true
	}
	repo := &memoryCheckpointRepository{}
	loop.checkpoints = contextmemory.NewService(repo)
	if _, err := loop.CompactConversation(context.Background(), "conversation"); err == nil {
		t.Fatal("internal-only history summarized")
	}
	if len(provider.Requests()) != 0 || repo.latest.ID != "" {
		t.Fatal("model called or checkpoint saved")
	}
}

func TestOrdinaryChatDiscardsInternalBoundaryCheckpointWithoutDroppingHumanHistory(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "hello"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}})
	state.messages = append([]conversation.Message{
		{ID: "old-human", ConversationID: "conversation", Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: "Keep this human requirement"}}},
		{ID: "old-internal", ConversationID: "conversation", Role: conversation.RoleAssistant, Internal: true, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: "INTERNAL_PROMPT"}}},
	}, state.messages...)
	loop.checkpoints = contextmemory.NewService(&memoryCheckpointRepository{})
	if _, err := loop.checkpoints.Save(context.Background(), contextmemory.Checkpoint{ConversationID: "conversation", ThroughMessageID: "old-internal", Summary: "INTERNAL_SUMMARY", SourceMessageCount: 2, SourceEstimatedTokens: 100, APIProtocol: "openai_chat_completions"}); err != nil {
		t.Fatal(err)
	}
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted {
		t.Fatal(state.run)
	}
	raw, _ := json.Marshal(provider.Requests()[0])
	if strings.Contains(string(raw), "INTERNAL_") || !strings.Contains(string(raw), "Keep this human requirement") {
		t.Fatal("wrong checkpoint or dropped history", string(raw))
	}
}
