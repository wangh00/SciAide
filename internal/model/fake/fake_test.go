package fake

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/wangh00/SciAide/internal/model"
)

func TestModelReturnsScriptAndRecordsRequest(t *testing.T) {
	fake := New([]Step{
		{Event: model.Event{Type: model.EventTextDelta, Text: "hello"}},
		{Event: model.Event{Type: model.EventDone}},
	})
	request := model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}}}
	stream, err := fake.Stream(context.Background(), request)
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	event, err := stream.Recv()
	if err != nil || event.Text != "hello" {
		t.Fatalf("first Recv() = %#v, %v", event, err)
	}
	_, _ = stream.Recv()
	_, err = stream.Recv()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("final Recv() error = %v, want io.EOF", err)
	}
	if got := fake.Requests(); len(got) != 1 || got[0].Messages[0].Content != "hi" {
		t.Fatalf("recorded requests = %#v", got)
	}
}

func TestStreamHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := New([]Step{{Event: model.Event{Type: model.EventTextDelta, Text: "never"}}})
	stream, err := fake.Stream(ctx, model.ChatRequest{})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	cancel()
	_, err = stream.Recv()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv() error = %v, want context.Canceled", err)
	}
}
