package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
)

type batchedDeltaObserver struct {
	NopObserver
	chunks []string
}

func (o *batchedDeltaObserver) ContentDelta(_ chat.Run, delta string) {
	o.chunks = append(o.chunks, delta)
}

func TestVisibleDeltasBatchWithoutChangingTextOrDroppingTail(t *testing.T) {
	for _, fail := range []bool{false, true} {
		script := []fake.Step{}
		for i := 0; i < 10000; i++ {
			script = append(script, fake.Step{Event: model.Event{Type: model.EventTextDelta, Text: "x"}})
		}
		if fail {
			script = append(script, fake.Step{Err: errors.New("stream failed")})
		} else {
			script = append(script, fake.Step{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}})
		}
		stream, _ := fake.New(script).Stream(context.Background(), model.ChatRequest{})
		o := &batchedDeltaObserver{}
		now := time.Now()
		loop := &Loop{observer: o, now: func() time.Time { return now }}
		turn, err := loop.receiveTurn(context.Background(), chat.Run{}, false, true, stream)
		if (err != nil) != fail || turn.text != strings.Repeat("x", 10000) || strings.Join(o.chunks, "") != turn.text || len(o.chunks) != 3 {
			t.Fatal("batch lost content or failed to reduce events", len(o.chunks), err)
		}
		stream.Close()
	}
}

func BenchmarkReceiveLongText(b *testing.B) {
	script := make([]fake.Step, 5001)
	for i := 0; i < 5000; i++ {
		script[i] = fake.Step{Event: model.Event{Type: model.EventTextDelta, Text: strings.Repeat("x", 24)}}
	}
	script[5000] = fake.Step{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}
	now := time.Now()
	loop := &Loop{observer: NopObserver{}, now: func() time.Time { return now }}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		provider := fake.New(script)
		stream, _ := provider.Stream(context.Background(), model.ChatRequest{})
		turn, err := loop.receiveTurn(context.Background(), chat.Run{}, false, true, stream)
		if err != nil || len(turn.text) != 120000 {
			b.Fatal(err, len(turn.text))
		}
		stream.Close()
	}
}

func TestIncrementalTextPreservesJournalAndPartialFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		loop, state, _ := newLoopFixture(t, nil)
		state.journalDrafts = map[int]string{}
		script := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Phase: "commentary", Text: "  inspection  "}}}
		for i := 0; i < 5000; i++ {
			script = append(script, fake.Step{Event: model.Event{Type: model.EventTextDelta, Text: "数"}})
		}
		if fail {
			script = append(script, fake.Step{Err: errors.New("interrupted")})
		} else {
			script = append(script, fake.Step{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}})
		}
		stream, _ := fake.New(script).Stream(context.Background(), model.ChatRequest{})
		turn, err := loop.receiveTurn(context.Background(), chat.Run{ModelTurns: 1}, true, false, stream)
		if (err != nil) != fail || turn.text != strings.Repeat("数", 5000) || turn.commentary != "  inspection  " || state.journalDrafts[1] != turn.draftText() {
			t.Fatal("text or journal changed", err)
		}
		stream.Close()
	}
}

// Model adapters can have a frame already decoded while the host stops the run.
// Cancel after the adapter has read it, before handing it back to the loop.
type stopDuringReceiveStream struct {
	model.Stream
	stop  context.CancelFunc
	reads int
}

func (s *stopDuringReceiveStream) Recv() (model.Event, error) {
	event, err := s.Stream.Recv()
	s.reads++
	if s.reads == 2 {
		s.stop()
	}
	return event, err
}

func TestReceiveTurnDiscardsLateFramesAfterStop(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event model.Event
	}{
		{"text", model.Event{Type: model.EventTextDelta, Text: "不应出现的迟到正文"}},
		{"tool call", model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "late", Name: "builtin.research.state.update"}}},
		{"usage", model.Event{Type: model.EventUsage, Usage: &model.Usage{OutputTokens: 1}}},
		{"completion", model.Event{Type: model.EventDone, FinishReason: "stop"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			loop, state, _ := newLoopFixture(t, nil)
			state.journalDrafts = map[int]string{}
			observer := &batchedDeltaObserver{}
			loop.observer = observer
			const accepted = "停止前已收到的文本"
			stream, err := fake.New([]fake.Step{
				{Event: model.Event{Type: model.EventTextDelta, Text: accepted}},
				{Event: tc.event},
			}).Stream(ctx, model.ChatRequest{})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			turn, err := loop.receiveTurn(ctx, chat.Run{ModelTurns: 1}, true, false, &stopDuringReceiveStream{Stream: stream, stop: stop})
			if !errors.Is(err, context.Canceled) || turn.text != accepted || strings.Join(observer.chunks, "") != accepted || state.journalDrafts[1] != accepted ||
				len(turn.toolCalls) != 0 || len(turn.usages) != 0 || turn.finishReason != "" {
				t.Fatalf("late frame affected stopped turn: err=%v text=%q visible=%q draft=%q calls=%d usages=%d finish=%q",
					err, turn.text, observer.chunks, state.journalDrafts[1], len(turn.toolCalls), len(turn.usages), turn.finishReason)
			}
		})
	}
}
