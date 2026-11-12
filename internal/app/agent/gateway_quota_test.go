package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
)

func TestGatewayQuotaMarkerCannotCompleteOrReplaceCheckpoint(t *testing.T) {
	script := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: "[error: usage_limit_reached]"}}, {Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
	loop, state, provider := newLoopFixture(t, nil, script)
	if got := loop.Run(context.Background(), state.run.ID); got != OutcomeFailed || state.run.ErrorCode != "MODEL_REQUEST_REJECTED" || len(provider.Requests()) != 1 {
		t.Fatal("quota completed/retried", got, state.run.ErrorCode, len(provider.Requests()))
	}
	loop, state, provider = newLoopFixture(t, nil, script)
	prior := contextmemory.Checkpoint{ID: "old", Revision: 3, Summary: "verified source", ThroughMessageID: "previous"}
	repo := &memoryCheckpointRepository{latest: prior}
	loop.checkpoints = contextmemory.NewService(repo)
	state.run.AutoCompactTokenLimit = 360000
	messages := []conversation.Message{{ID: "source", RunID: "old", Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: strings.Repeat("original source ", 3000)}}}}
	_, e := loop.compactConversation(context.Background(), &state.run, provider, prior, messages, "source", nil, true)
	if e == nil || apperr.Public(e).Code != "MODEL_REQUEST_REJECTED" || !reflect.DeepEqual(repo.latest, prior) || len(provider.Requests()) != 1 {
		t.Fatal("quota replaced/changed checkpoint or retried", e, repo.latest, len(provider.Requests()))
	}
}

func TestGatewayIdentityMarkerStopsBeforeStageCorrectionOrCheckpoint(t *testing.T) {
	const marker = "[error: no_userid]"
	for _, mode := range []string{"ordinary", "structured", "implementation"} {
		t.Run(mode, func(t *testing.T) {
			loop, state, provider := newLoopFixture(t, nil, submissionScript(marker), submissionScript(marker), submissionScript("Unusable service output without a submission."), submissionScript(marker))
			switch mode {
			case "structured":
				bindSubmissionRepair(loop, state)
			case "implementation":
				bindParts(loop, state)
			}
			if got := loop.Run(context.Background(), state.run.ID); got != OutcomeFailed || state.run.ErrorCode != "MODEL_REQUEST_REJECTED" || len(provider.Requests()) != 1 || len(state.calls) != 0 {
				t.Fatal("service identity error was accepted or sent to a format correction", got, state.run.ErrorCode, len(provider.Requests()))
			}
			if !strings.Contains(state.run.ErrorDetails, "no_userid") {
				t.Fatal("service identity diagnostic missing", state.run.ErrorDetails)
			}
		})
	}
	const checkpointText = " \r\n[error:no_userid]\n"
	loop, state, provider := newLoopFixture(t, nil, submissionScript(checkpointText))
	prior := contextmemory.Checkpoint{ID: "old", Revision: 3, Summary: "verified source", ThroughMessageID: "previous"}
	repo := &memoryCheckpointRepository{latest: prior}
	loop.checkpoints = contextmemory.NewService(repo)
	state.run.AutoCompactTokenLimit = 360000
	messages := []conversation.Message{{ID: "source", RunID: "old", Role: conversation.RoleUser, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{Type: "text", Text: strings.Repeat("original source ", 3000)}}}}
	_, err := loop.compactConversation(context.Background(), &state.run, provider, prior, messages, "source", nil, true)
	if err == nil || apperr.Public(err).Code != "MODEL_REQUEST_REJECTED" || !reflect.DeepEqual(repo.latest, prior) || len(provider.Requests()) != 1 {
		t.Fatal("service error replaced the checkpoint or retried", err, repo.latest, len(provider.Requests()))
	}
	if !strings.Contains(apperr.Public(err).Details, fmt.Sprintf("%x", sha256.Sum256([]byte(checkpointText)))) {
		t.Fatal("checkpoint error fingerprint discarded original whitespace", apperr.Public(err).Details)
	}
}
