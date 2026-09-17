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
