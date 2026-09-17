package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func TestCheckpointLengthCorrectionRetainsSourceAndBoundsAttempts(t *testing.T) {
	for _, success := range []bool{true, false} {
		t.Run(map[bool]string{true: "recovered", false: "exhausted"}[success], func(t *testing.T) {
			oversized := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: strings.Repeat("x", 20001)}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
			last := oversized
			if success {
				last = []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "Verified state; original evidence remains available."}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
			}
			loop, state, provider := newLoopFixture(t, nil, oversized, oversized, last)
			repo := &memoryCheckpointRepository{}
			loop.checkpoints = contextmemory.NewService(repo)
			state.run.AutoCompactTokenLimit = 360000
			state.run.APIProtocol = modelcap.ProtocolOpenAIChat
			messages := []conversation.Message{{ID: "source", RunID: "old", Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: strings.Repeat("original evidence ", 3000)}}}}
			_, err := loop.compactConversation(context.Background(), &state.run, provider, contextmemory.Checkpoint{}, messages, "source", nil, true)
			if (err == nil) != success {
				t.Fatalf("success=%v err=%v", success, err)
			}
			requests := provider.Requests()
			if len(requests) != 3 {
				t.Fatalf("requests=%d", len(requests))
			}
			for i, r := range requests {
				if len(r.Tools) != 0 || !reflect.DeepEqual(r.Messages[:len(r.Messages)-1], requests[0].Messages[:len(requests[0].Messages)-1]) {
					t.Fatal("source or tool scope changed")
				}
				if !strings.Contains(r.Messages[len(r.Messages)-1].Content, "Unicode characters, NOT model tokens") {
					t.Fatal("ambiguous length unit")
				}
				if i > 0 && r.Messages[len(r.Messages)-1].Content == requests[i-1].Messages[len(r.Messages)-1].Content {
					t.Fatal("retry did not correct instruction")
				}
			}
			if success {
				if repo.latest.ThroughMessageID != "source" || strings.Contains(repo.latest.Summary, strings.Repeat("x", 100)) {
					t.Fatal("invalid checkpoint persisted")
				}
			} else if repo.latest.Revision != 0 {
				t.Fatal("failed summary replaced history")
			}
		})
	}
}
