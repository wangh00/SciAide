package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/modelcap"
)

type incrementalCheckpointResolver struct{ model model.ChatModel }

func (r incrementalCheckpointResolver) Resolve(context.Context, string, string) (model.ResolvedChatModel, error) {
	return model.ResolvedChatModel{Model: r.model, APIProtocol: modelcap.ProtocolOpenAIResponses, ContextBudget: modelcap.ResolveContextBudget(272000, 244800, modelcap.ContextWindowSourceManual)}, nil
}

func TestIncrementalCheckpointAutomaticToolTurnCompactsAndContinues(t *testing.T) {
	// One human request can generate enough native protocol state to squeeze
	// earlier dialogue out of the budget. Preserve the native group intact.
	reasoning := model.ProviderItem{Ordinal: 0, Type: "reasoning", Payload: json.RawMessage(`{"type":"reasoning","encrypted_content":"` + strings.Repeat("x", 238000) + `"}`)}
	function := model.ProviderItem{Ordinal: 1, Type: "function_call", CallID: "lookup", Payload: json.RawMessage(`{"type":"function_call","call_id":"lookup","name":"builtin.fixture","arguments":"{\"query\":\"paper\"}"}`)}
	toolTurn := []fake.Step{
		{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &reasoning}},
		{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "lookup", Name: "builtin.fixture", Arguments: json.RawMessage(`{"query":"paper"}`)}}},
		{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &function}},
		{Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}},
	}
	summary := strings.Repeat("摘", 3305)
	loop, state, provider := newLoopFixture(t, nil, toolTurn,
		[]fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: summary}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}},
		[]fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "continued after checkpoint"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}})
	state.messages = append([]conversation.Message{
		checkpointText("boundary", "old", "already summarized"),
		checkpointText("recent", "recent-run", strings.Repeat("新", 1969)),
	}, state.messages...)
	repo := &memoryCheckpointRepository{}
	loop.checkpoints = contextmemory.NewService(repo)
	prior, err := loop.checkpoints.Save(context.Background(), contextmemory.Checkpoint{
		ConversationID: state.run.ConversationID, ThroughMessageID: "boundary", Summary: strings.Repeat("旧", 4122),
		SourceMessageCount: 5, SourceEstimatedTokens: 25746, ModelProfileID: "profile", ModelID: "model", APIProtocol: modelcap.ProtocolOpenAIResponses,
	})
	if err != nil {
		t.Fatal(err)
	}
	loop.models = incrementalCheckpointResolver{model: provider}
	if outcome := loop.Run(context.Background(), state.run.ID); outcome != OutcomeCompleted {
		t.Fatalf("outcome=%s error=%s details=%s", outcome, state.run.ErrorMessage, state.run.ErrorDetails)
	}
	requests := provider.Requests()
	if len(requests) != 3 || len(requests[1].Tools) != 0 || len(requests[1].ProviderTurns) != 0 || len(requests[2].ProviderTurns) != 1 {
		t.Fatal("expected tool turn, summary-only request, then intact native replay")
	}
	if repo.latest.Revision != prior.Revision+1 || repo.latest.ThroughMessageID != "recent" || repo.latest.Summary != summary || repo.latest.SourceEstimatedTokens != 25746+1969 {
		t.Fatal("incremental checkpoint was not saved correctly")
	}
	if err := contextmemory.Verify(repo.latest); err != nil {
		t.Fatal(err)
	}
	if len(state.calls) != 1 || state.messages[1].Parts[0].Text != strings.Repeat("新", 1969) || state.messages[len(state.messages)-1].Parts[0].Text != "continued after checkpoint" {
		t.Fatal("tool reran or original dialogue/answer changed")
	}
	if string(requests[2].ProviderTurns[0].Items[0].Payload) != string(reasoning.Payload) || requests[2].ProviderTurns[0].ToolResults[0].ToolCallID != "lookup" {
		t.Fatal("compaction broke provider state/result pairing")
	}
}

func checkpointText(id, runID, text string) conversation.Message {
	return conversation.Message{ID: id, RunID: runID, Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: text}}}
}

func TestIncrementalCheckpointComparesTheContentItActuallyReplaces(t *testing.T) {
	// Observed incident: previous summary 4122 + new dialogue 1969,
	// returned cumulative summary 3305. The old predicate wrongly rejects it.
	current := contextmemory.Checkpoint{ID: "prior", Summary: strings.Repeat("旧", 4122), ThroughMessageID: "boundary", SourceMessageCount: 5, SourceEstimatedTokens: 25746}
	messages := []conversation.Message{checkpointText("boundary", "old", "already summarized"), checkpointText("new", "next", strings.Repeat("新", 1969))}
	for _, tc := range []struct {
		name    string
		current contextmemory.Checkpoint
		prefix  []model.Message
		want    int
	}{
		{"default prefix includes prior", current, nil, 6091},
		{"explicit prefix includes prior", current, []model.Message{{Role: model.RoleSystem, Content: strings.Repeat("host state ", 1000)}, checkpointContextMessage(current)}, 6091},
		{"missing prior is not counted", current, []model.Message{{Role: model.RoleSystem, Content: strings.Repeat("host state ", 1000)}}, 1969},
		{"first summary does not count historic source accounting", contextmemory.Checkpoint{SourceEstimatedTokens: 999999}, nil, 1987},
	} {
		t.Run(tc.name, func(t *testing.T) {
			batch, err := buildCheckpointBatch(tc.current, messages, "new", 244800, tc.prefix)
			if err != nil || batch.replacedCharacters != tc.want {
				t.Fatal(batch, err)
			}
			if tc.current.ID != "" && batch.estimatedTokens != 1969 {
				t.Fatal("source accounting includes old summary", batch)
			}
		})
	}
	if 3305 < 1969 || !(3305 < 6091) {
		t.Fatal("fixture does not reproduce old false rejection")
	}
}

func TestIncrementalCheckpointAcceptsGrowingSummaryButStillRequiresNetReduction(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		prior, newSource, summary int
		accepted                  bool
	}{
		{"observed second compaction", 4122, 1969, 3305, true},
		{"third summary larger than prior but net smaller", 3305, 1000, 4000, true},
		{"equal combined source rejected", 4122, 1969, 6091, false},
		{"larger combined source rejected", 4122, 1969, 6092, false},
		{"large original history does not waive current reduction", 10, 10, 21, false},
		{"first compression must also reduce", 0, 10, 10, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loop, state, provider := newLoopFixture(t, nil, []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: strings.Repeat("摘", tc.summary)}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}})
			state.run.APIProtocol = modelcap.ProtocolOpenAIChat
			state.run.AutoCompactTokenLimit = 244800
			current := contextmemory.Checkpoint{}
			if tc.prior > 0 {
				current = contextmemory.Checkpoint{ID: "prior", Summary: strings.Repeat("旧", tc.prior), ThroughMessageID: "boundary", SourceMessageCount: 5, SourceEstimatedTokens: 25746, Revision: 1}
			}
			repo := &memoryCheckpointRepository{latest: current}
			loop.checkpoints = contextmemory.NewService(repo)
			messages := []conversation.Message{checkpointText("new", "next", strings.Repeat("新", tc.newSource))}
			before := append([]conversation.Message(nil), messages...)
			saved, err := loop.compactConversation(context.Background(), &state.run, provider, current, messages, "new", nil, true)
			if (err == nil) != tc.accepted {
				t.Fatal(err)
			}
			if tc.accepted {
				if saved.SourceEstimatedTokens != current.SourceEstimatedTokens+tc.newSource || saved.SourceMessageCount != current.SourceMessageCount+1 || saved.Summary != strings.Repeat("摘", tc.summary) || saved.ThroughMessageID != "new" {
					t.Fatal(saved)
				}
			} else if !reflect.DeepEqual(repo.latest, current) || !strings.Contains(err.Error(), "previous summary plus new source") {
				t.Fatal("failed compression replaced checkpoint", err, repo.latest)
			}
			requests := provider.Requests()
			if len(requests) != 1 || len(requests[0].Tools) != 0 || !reflect.DeepEqual(before, messages) {
				t.Fatal("changed source or retried a successful provider response")
			}
			if tc.prior > 0 && requests[0].Messages[1].Content != checkpointContextMessage(current).Content {
				t.Fatal("previous summary not fully supplied")
			}
			if !strings.Contains(requests[0].Messages[len(requests[0].Messages)-1].Content, "Limit: 20000 Unicode characters") {
				t.Fatal("summary limit lowered")
			}
		})
	}
}
