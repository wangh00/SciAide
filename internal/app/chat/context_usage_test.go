package chat

import (
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/modelcap"
)

func TestContextUsageIdentityNeverCrossesRunsModelsOrProtocols(t *testing.T) {
	run := Run{ID: "run", ModelProfileID: "profile", ModelID: "model", APIProtocol: modelcap.ProtocolOpenAIResponses, ModelTurns: 3}
	value := ContextUsageObservation{Version: 1, RunID: run.ID, ModelProfileID: run.ModelProfileID, ModelID: run.ModelID, APIProtocol: run.APIProtocol, TurnIndex: 2, InputTokens: 55824, CharacterEstimate: 244000, Components: map[string]int{strings.Repeat("a", 64): 1}}
	if !value.Matches(run) {
		t.Fatal("valid same-run observation rejected")
	}
	for _, change := range []func(*Run){
		func(r *Run) { r.ID = "other" },
		func(r *Run) { r.ModelProfileID = "another-profile" },
		func(r *Run) { r.ModelID = "another-model" },
		func(r *Run) { r.APIProtocol = modelcap.ProtocolAnthropic },
		func(r *Run) { r.ModelTurns = 1 },
	} {
		other := run
		change(&other)
		if value.Matches(other) {
			t.Fatal("usage transferred outside its exact run/model/protocol")
		}
	}
	for _, change := range []func(*ContextUsageObservation){
		func(o *ContextUsageObservation) { o.Version = 2 },
		func(o *ContextUsageObservation) { o.InputTokens = 0 },
		func(o *ContextUsageObservation) { o.InputTokens = 10000001 },
		func(o *ContextUsageObservation) { o.TurnIndex = 0 },
		func(o *ContextUsageObservation) { o.Components = nil },
		func(o *ContextUsageObservation) { o.Components = map[string]int{"not-a-hash": 1} },
		func(o *ContextUsageObservation) { o.Components = map[string]int{strings.Repeat("z", 64): 1} },
		func(o *ContextUsageObservation) { o.Components = map[string]int{strings.Repeat("a", 64): 0} },
	} {
		invalid := value
		change(&invalid)
		if invalid.Matches(run) {
			t.Fatal("invalid usage observation accepted")
		}
	}
}
