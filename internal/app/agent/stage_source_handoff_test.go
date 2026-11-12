package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
	"github.com/wangh00/SciAide/internal/modelcap"
)

type handoffGuidanceFixture struct {
	ResearchGuidanceProvider
	packet workflow.StageMaterials
	calls  int
}

func (f *handoffGuidanceFixture) GuidanceForConversation(ctx context.Context, id string) (workflow.ResearchGuidance, bool, error) {
	v, bound, err := f.ResearchGuidanceProvider.GuidanceForConversation(ctx, id)
	v.StageMaterialsVersion = "source-v1"
	return v, bound, err
}

type stageHandoffUsageRuns struct {
	workflowContractRuns
	observations []chat.ContextUsageObservation
}

func (r *stageHandoffUsageRuns) LatestContextUsage(context.Context, string) (chat.ContextUsageObservation, bool, error) {
	return chat.ContextUsageObservation{}, false, nil
}
func (r *stageHandoffUsageRuns) SaveContextUsage(_ context.Context, value chat.ContextUsageObservation) error {
	r.observations = append(r.observations, value)
	return nil
}

func TestStageSourceHandoffKeepsUsageAndCompleteCurrentEvidence(t *testing.T) {
	first := append([]fake.Step{{Event: model.Event{Type: model.EventUsage, Usage: &model.Usage{InputTokens: 6000}}}}, convergenceReadScript(1, 0)...)
	last := append([]fake.Step{{Event: model.Event{Type: model.EventUsage, Usage: &model.Usage{InputTokens: 500}}}}, submissionToolScript(`{"items":["verified result"],"state":"ready"}`)...)
	loop, state, provider := newLoopFixture(t, nil, first, last)
	bindReadConvergenceFixture(t, loop, state)
	runs := &stageHandoffUsageRuns{workflowContractRuns: loop.runs.(workflowContractRuns)}
	loop.runs = runs
	optional := strings.Repeat("OPTIONAL_ORIGINAL_SECTION ", 500)
	loop.research = &handoffGuidanceFixture{ResearchGuidanceProvider: loop.research, packet: workflow.StageMaterials{Sources: []workflow.StageMaterial{{Content: optional}}}}
	loop.models = budgetResolver{model: provider, budget: modelcap.ResolveContextBudget(40000, 0, modelcap.ContextWindowSourceManual)}
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted {
		t.Fatal("optional usage poisoned the following base request", got, state.run.ErrorCode, state.run.ErrorDetails)
	}
	requests := provider.Requests()
	if len(requests) != 2 {
		t.Fatal("new model rounds", len(requests))
	}
	text := func(r model.ChatRequest) string {
		var b strings.Builder
		for _, m := range r.Messages {
			b.WriteString(m.Content)
		}
		return b.String()
	}
	if !strings.Contains(text(requests[0]), optional) || !strings.Contains(text(requests[1]), optional) || !strings.Contains(text(requests[1]), strings.Repeat("中", 5500)) {
		t.Fatalf("optional source did not yield: first=%t next=%t evidence=%t estimated=%d/%d", strings.Contains(text(requests[0]), optional), strings.Contains(text(requests[1]), optional), strings.Contains(text(requests[1]), strings.Repeat("中", 5500)), estimateRequestTokens(requests[0]), estimateRequestTokens(requests[1]))
	}
	if state.run.InputTokens != 6500 {
		t.Fatal("real optional-bearing request usage was omitted", state.run.InputTokens)
	}
	if len(runs.observations) != 2 || len(runs.observations[0].StageSourceIDs) != 1 || len(runs.observations[1].StageSourceIDs) != 1 {
		t.Fatal("actual source packet identity not saved with its measurement")
	}
}

func (f *handoffGuidanceFixture) StageMaterials(_ context.Context, binding workflow.StageMaterialRequest) (workflow.StageMaterials, error) {
	f.calls++
	v := f.packet
	v.Binding = binding
	return v, nil
}

func TestStageSourceHandoffIsRealFirstRequestContentNotReadFlag(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil, submissionToolScript(`{"items":["direct result"],"state":"ready"}`))
	bindReadConvergenceFixture(t, loop, state)
	f := &handoffGuidanceFixture{ResearchGuidanceProvider: loop.research, packet: workflow.StageMaterials{Sources: []workflow.StageMaterial{{Kind: "workspace_snapshot", Locator: "research-inputs/frozen.csv", Content: "UNIQUE_ORIGINAL_SOURCE_原始值", SHA256: "source-hash"}}}}
	loop.research = f
	if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted {
		t.Fatal(got, state.run.ErrorCode)
	}
	requests := provider.Requests()
	if len(requests) != 1 || f.calls != 1 || len(state.calls) != 0 {
		t.Fatal("handoff added IO/model rounds")
	}
	found := false
	for _, m := range requests[0].Messages {
		if strings.Contains(m.Content, "UNIQUE_ORIGINAL_SOURCE_原始值") {
			found = true
			if m.Role != model.RoleUser || m.HostToolReferences {
				t.Fatal("source promoted to trusted tool instructions")
			}
		}
	}
	if !found {
		t.Fatal("only a material manifest, not the original content, reached the model")
	}
}

func TestStageSourceHandoffCannotDisplaceOriginalEvidence(t *testing.T) {
	for _, native := range []bool{false, true} {
		m := model.Message{Role: model.RoleTool, ToolCallID: "call", Content: strings.Repeat("unchanged evidence. ", 2000) + "UNIQUE_FACT"}
		original := model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: "frozen stage input"}, m}}
		if native {
			original.Messages = original.Messages[:1]
			original.ProviderTurns = []model.ProviderTurn{{ToolResults: []model.Message{m}}}
		}
		packet := workflow.StageMaterials{Sources: []workflow.StageMaterial{{Content: strings.Repeat("optional source", 500)}}}
		limit := currentRequestBudgetTokens(original, nil) + 20
		if got, included := withStageSourceHandoff(original, packet, nil, limit); len(included) > 0 || !reflect.DeepEqual(got, original) {
			t.Fatal("optional source displaced admitted evidence")
		}
		if got, included := withStageSourceHandoff(original, packet, nil, limit+20000); len(included) == 0 || len(got.Messages) != len(original.Messages)+1 || !reflect.DeepEqual(got.Messages[:len(original.Messages)], original.Messages) || !reflect.DeepEqual(got.ProviderTurns, original.ProviderTurns) {
			t.Fatal("source handoff modified original/native context")
		}
	}
}

func TestStageSourceHandoffReusesActualSubsetOrderAndPreservesMeasuredReserve(t *testing.T) {
	base := model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: strings.Repeat("stable current input ", 100)}}}
	a := workflow.StageMaterial{Kind: "skill_section", Locator: "a", Content: strings.Repeat("long first source ", 200)}
	b := workflow.StageMaterial{Kind: "skill_section", Locator: "b", Content: "short later source"}
	packet := workflow.StageMaterials{Sources: []workflow.StageMaterial{a, b}}
	// First only B fitted. A is not allowed to replace it on the next turn and
	// make the same B message look like additional, unmeasured context.
	prior, ids := withStageSourceHandoff(base, workflow.StageMaterials{Sources: []workflow.StageMaterial{b}}, nil, 100000)
	obs := observeContextUsage(usageTestRun(), prior, model.Usage{InputTokens: 4000}, true)
	obs.StageSourceIDs = ids
	got, actual := withStageSourceHandoff(base, packet, &obs, observedContextReserve(obs)+10)
	if !reflect.DeepEqual(got, prior) || !reflect.DeepEqual(actual, ids) {
		t.Fatal("actual short subset/order was replaced or double charged")
	}
	// A full original packet is also reused as one measured message.
	prior, ids = withStageSourceHandoff(base, packet, nil, 100000)
	obs = observeContextUsage(usageTestRun(), prior, model.Usage{InputTokens: 20000}, true)
	obs.StageSourceIDs = ids
	if currentRequestBudgetTokens(prior, &obs) < observedContextReserve(obs) {
		t.Fatal("known provider usage/reserve was capped by character counts")
	}
	got, actual = withStageSourceHandoff(base, packet, &obs, observedContextReserve(obs)+10)
	if !reflect.DeepEqual(got, prior) || !reflect.DeepEqual(actual, ids) {
		t.Fatal("unchanged full packet was charged as a new prefix")
	}
	packet.Sources[1].Content = "changed source in another frozen version"
	changed, newIDs := withStageSourceHandoff(base, packet, &obs, observedContextReserve(obs)+10)
	if reflect.DeepEqual(changed, prior) || reflect.DeepEqual(newIDs, ids) {
		t.Fatal("source fingerprint restored stale bytes")
	}
}
