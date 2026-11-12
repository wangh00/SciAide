package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
)

type failingNativeProviderRuns struct{ workflowContractRuns }

func (failingNativeProviderRuns) SaveProviderTurn(context.Context, string, model.ProviderTurn, time.Time) error {
	return errors.New("injected native provider audit failure")
}

// A scoped, local capture is optional and never checked into the repository.
// This replays saved provider messages, not a live model or a second executor.
func TestCapturedNativePlannerMissingActionReplay(t *testing.T) {
	path := os.Getenv("SCIAIDE_PLANNER_NATIVE_REPLAY")
	if path == "" {
		t.Skip("no scoped native planner incident capture")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var turns []struct {
		Attempt, TurnIndex                    int
		DraftText, FinishReason, ExpectedCode string
		Items                                 []model.ProviderItem
	}
	if err := json.Unmarshal(data, &turns); err != nil || len(turns) == 0 {
		t.Fatal("invalid local replay capture", err)
	}
	for _, captured := range turns {
		t.Run(fmt.Sprintf("attempt_%d/turn_%d", captured.Attempt, captured.TurnIndex), func(t *testing.T) {
			script := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: captured.DraftText}}}
			for i := range captured.Items {
				script = append(script, fake.Step{Event: model.Event{Type: model.EventProviderItem, ProviderItem: &captured.Items[i]}})
			}
			script = append(script, fake.Step{Event: model.Event{Type: model.EventDone, FinishReason: captured.FinishReason}})
			loop, state, provider := newLoopFixture(t, nil, script)
			bindNativePlannerActionFixture(loop, state)
			if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeFailed || state.run.ErrorCode != captured.ExpectedCode || len(provider.Requests()) != 1 || len(state.calls) != 0 || state.journalDrafts[1] != captured.DraftText || len(state.providerTurns) != 1 || !reflect.DeepEqual(state.providerTurns[0].Items, captured.Items) {
				t.Fatal("captured provider message was accepted, repaired, or lost instead of classified", outcome, state.run.ErrorCode, len(provider.Requests()))
			}
		})
	}
}

func TestNativePlannerMissingActionStopsWithoutContentRepair(t *testing.T) {
	good := `{"normalizedQuestion":"question","selectedSkills":[],"routes":[]}`
	for _, text := range []string{good, "  ```json\n" + good + "\n```  ", `{"routes":[]}`, "I will submit next.", `<tool_call name="submit_stage_result">{}</tool_call>`, "", "I cannot comply with this request."} {
		for _, failAudit := range []bool{false, true} {
			t.Run(text+map[bool]string{false: "/audit_ok", true: "/audit_failed"}[failAudit], func(t *testing.T) {
				item := model.ProviderItem{Ordinal: 0, Type: "message", Payload: json.RawMessage(`{"type":"message","role":"assistant","content":[]}`)}
				script := []fake.Step{{Event: model.Event{Type: model.EventTextDelta, Text: text}}, {Event: model.Event{Type: model.EventProviderItem, ProviderItem: &item}}, {Event: model.Event{Type: model.EventDone, FinishReason: "completed"}}}
				loop, state, provider := newLoopFixture(t, nil, script, script, script)
				bindNativePlannerActionFixture(loop, state)
				if failAudit {
					loop.runs = failingNativeProviderRuns{loop.runs.(workflowContractRuns)}
				}
				outcome := loop.Run(context.Background(), "run")
				want := "WORKFLOW_AI_NATIVE_ACTION_MISSING"
				if text == "I cannot comply with this request." {
					want = "WORKFLOW_AI_MODEL_REFUSED"
				}
				if failAudit {
					want = "MODEL_PROTOCOL_STATE_SAVE_FAILED"
				}
				if outcome != OutcomeFailed || state.run.ErrorCode != want || len(provider.Requests()) != 1 || len(state.calls) != 0 {
					t.Fatal("native protocol failure entered repair, accepted text, or executed IO", outcome, state.run.ErrorCode, len(provider.Requests()), len(state.calls))
				}
				if !failAudit && (state.journalDrafts[1] != text || state.journalStatuses[1] != chat.ModelTurnCompleted || len(state.providerTurns) != 1) {
					t.Fatal("missing action lost its exact original audit")
				}
			})
		}
	}
}

func TestLegacyPlannerTextIsNotSilentlyUpgraded(t *testing.T) {
	for _, version := range []string{"legacy-v1", "custom-native-planner-v1", "research-starter-semantic-v6-native-planner-v1"} {
		t.Run(version, func(t *testing.T) {
			good := `{"normalizedQuestion":"question","selectedSkills":[],"routes":[]}`
			loop, state, provider := newLoopFixture(t, nil, submissionScript(good))
			bindNativePlannerActionFixture(loop, state)
			runs := loop.runs.(workflowContractRuns)
			runs.contract.PromptVersion = version
			loop.runs = runs
			if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted || len(provider.Requests()) != 1 || provider.Requests()[0].RequireTool || provider.Requests()[0].ForcedTool != "" {
				t.Fatal("legacy execution changed protocol", got, state.run.ErrorCode)
			}
		})
	}
}

func TestNativePlannerInvalidFrozenContractNeverCallsModel(t *testing.T) {
	for _, kind := range []string{"version", "prompt", "schema", "node", "kind"} {
		t.Run(kind, func(t *testing.T) {
			loop, state, provider := newLoopFixture(t, nil, submissionScript(`{}`))
			bindNativePlannerActionFixture(loop, state)
			runs := loop.runs.(workflowContractRuns)
			switch kind {
			case "version":
				runs.contract.PromptVersion += "0"
			case "prompt":
				runs.contract.PromptText += " changed"
			case "schema":
				runs.contract.OutputSchema = json.RawMessage(`{"type":"object"}`)
			case "node":
				runs.contract.NodeID = "custom"
			case "kind":
				runs.contract.NodeKind = "ai_analysis"
			}
			loop.runs = runs
			if outcome := loop.Run(context.Background(), "run"); outcome != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_PROTOCOL_INVALID" || len(provider.Requests()) != 0 {
				t.Fatal("invalid native identity silently downgraded or sent a request", outcome, state.run.ErrorCode)
			}
		})
	}
}
