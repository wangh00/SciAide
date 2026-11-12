package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/modelcap"
)

type runtimeCaptureObserver struct {
	NopObserver
	activities []chat.RuntimeActivity
}

func (o *runtimeCaptureObserver) RuntimeActivity(_ chat.Run, value chat.RuntimeActivity) {
	o.activities = append(o.activities, value)
}
func activityPhases(o *runtimeCaptureObserver) []string {
	result := []string{}
	for _, a := range o.activities {
		result = append(result, a.Phase)
	}
	return result
}

func TestRuntimeActivityReportsModelRetryAndToolsWithoutChangingAnswer(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil,
		[]fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "discarded"}}, {Err: &apperr.Error{Code: "MODEL_UNAVAILABLE", Retryable: true}}},
		[]fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "call", Name: "builtin.fixture", Arguments: []byte(`{"query":"specific gap"}`)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}},
		[]fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "Verified answer"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}})
	o := &runtimeCaptureObserver{}
	loop.observer = o
	loop.retryDelay = func(int) time.Duration { return 0 }
	if loop.Run(context.Background(), "run") != OutcomeCompleted {
		t.Fatal(state.run)
	}
	want := []string{"model_wait", "model_processing", "model_wait", "model_processing", "tools", "model_wait", "model_processing"}
	if !reflect.DeepEqual(activityPhases(o), want) {
		t.Fatal(activityPhases(o))
	}
	if len(provider.Requests()) != 3 || len(state.calls) != 1 || state.messages[1].Parts[0].Text != "Verified answer" {
		t.Fatal("telemetry changed execution")
	}
	for _, a := range o.activities {
		if a.RunID != "run" || a.TurnIndex < 1 || a.StartedAt.IsZero() {
			t.Fatal(a)
		}
	}
}

type failingCheckpointStore struct{ memoryCheckpointRepository }

func (*failingCheckpointStore) Save(context.Context, contextmemory.Checkpoint) (contextmemory.Checkpoint, error) {
	return contextmemory.Checkpoint{}, errors.New("fixture save failure")
}

func TestRuntimeActivityCompactionSuccessRequiresSavedCheckpoint(t *testing.T) {
	for _, fail := range []bool{false, true} {
		loop, state, provider := newLoopFixture(t, nil, []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "# State\nVerified source and unresolved decisions preserved."}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}})
		o := &runtimeCaptureObserver{}
		loop.observer = o
		if fail {
			loop.checkpoints = contextmemory.NewService(&failingCheckpointStore{})
		} else {
			loop.checkpoints = contextmemory.NewService(&memoryCheckpointRepository{})
		}
		state.run.ModelTurns = 4
		state.run.APIProtocol = modelcap.ProtocolOpenAIChat
		state.run.AutoCompactTokenLimit = 360000
		messages := []conversation.Message{{ID: "source", RunID: "old", Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: strings.Repeat("original evidence ", 3000)}}}}
		_, err := loop.compactConversation(context.Background(), &state.run, provider, contextmemory.Checkpoint{}, messages, "source", nil, true)
		want := []string{"compaction", "compaction_completed"}
		if fail {
			want = want[:1]
		}
		if (err != nil) != fail || !reflect.DeepEqual(activityPhases(o), want) {
			t.Fatal(fail, err, activityPhases(o))
		}
		if len(provider.Requests()) != 1 || len(provider.Requests()[0].Tools) != 0 || messages[0].Parts[0].Text != strings.Repeat("original evidence ", 3000) {
			t.Fatal("compaction source/request changed")
		}
	}
}
