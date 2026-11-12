package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
)

func characterTransportFixture(t *testing.T) (*Loop, *loopState, *revisionEditHostFixture, string) {
	t.Helper()
	loop, state, _ := newLoopFixture(t, nil)
	host, _ := bindRevisionEditAgentFixture(t, loop, state)
	host.base.Analysis = json.RawMessage(`{"items":["原样誉写"],"state":"ready"}`)
	host.base.SHA256 = fmt.Sprintf("%x", sha256.Sum256(host.base.Analysis))
	host.base.Characters = &workflow.RevisionCharacterSource{
		Version: "revision-character-source-v1", SHA256: "frozen-source-hash",
		Characters: []workflow.RevisionSourceCharacter{{ID: "U+8A8A", Text: "誊", Pointer: "/_reviewCorrections/corrections/0/text", Offset: 4}},
	}
	payload := fmt.Sprintf(`{"baseSHA256":%q,"sourceSHA256":"frozen-source-hash","characterEdits":[{"path":"/items/0","before":"誉","replacementId":"U+8A8A"}]}`, host.base.SHA256)
	return loop, state, host, payload
}

func characterTransportScript(payload string) []fake.Step {
	script := revisionEditScript(payload)
	script[0].Event.ToolCall.Name = stageCharacterEditTool
	return script
}

func TestCharacterTransportOptionalBudgetAndLegacyInput(t *testing.T) {
	_, _, host, _ := characterTransportFixture(t)
	base := *host.base
	withMenu := &stageRevisionEdit{base: base, schema: workflow.RevisionTextEditSchema(base.Analysis), characterSchema: base.CharacterEditSchema()}
	legacyBase := base
	legacyBase.Characters = nil
	legacy := &stageRevisionEdit{base: legacyBase, schema: workflow.RevisionTextEditSchema(base.Analysis)}
	admitted := stageSubmissionRequest(model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: strings.Repeat("EXACT_SCIENTIFIC_EVIDENCE ", 100)}}}, submissionRepairSchema)
	literal := withStageRevisionEdit(admitted, legacy, nil, 100000)
	limit := currentRequestBudgetTokens(literal, nil)
	limited := withStageRevisionEdit(admitted, withMenu, nil, limit)
	if !reflect.DeepEqual(limited, literal) || !strings.Contains(submissionRequestText(limited), "EXACT_SCIENTIFIC_EVIDENCE") {
		t.Fatal("character control displaced evidence or the previously admitted literal interface")
	}
	allowed := withStageRevisionEdit(admitted, withMenu, nil, limit+10000)
	if len(allowed.Tools) != len(literal.Tools)+1 || allowed.Tools[len(allowed.Tools)-1].Name != stageCharacterEditTool || !strings.Contains(submissionRequestText(allowed), "EXACT_SCIENTIFIC_EVIDENCE") {
		t.Fatal("character control was not admitted with sufficient budget")
	}

	loop, state, legacyHost, payload := characterTransportFixture(t)
	legacyHost.base.Characters = nil
	provider := fake.New(characterTransportScript(payload))
	loop.models = fakeResolver{provider}
	if got := loop.Run(t.Context(), state.run.ID); got != OutcomeFailed || state.run.ErrorCode != "MODEL_SUBMISSION_TOOL_VIOLATION" || len(state.calls) != 0 || len(legacyHost.validated) != 0 || len(provider.Requests()) != 1 {
		t.Fatal("legacy input accepted an unoffered character control", got, state.run.ErrorCode, len(state.calls))
	}
}

func TestCharacterTransportMixedControlsNeverExecuteResearchIO(t *testing.T) {
	for _, kind := range []string{"research_tool", "full_result", "literal_edit"} {
		t.Run(kind, func(t *testing.T) {
			loop, state, host, payload := characterTransportFixture(t)
			script := characterTransportScript(payload)
			var other fake.Step
			switch kind {
			case "research_tool":
				other = convergenceReadScript(1, 0)[1]
			case "full_result":
				other = submissionToolScript(`{"items":["not accepted"],"state":"ready"}`)[0]
			case "literal_edit":
				other = revisionEditScript(`{"baseSHA256":"other","textEdits":[]}`)[0]
				other.Event.ToolCall.ID = "literal-edit"
			}
			script = []fake.Step{script[0], other, script[1]}
			provider := fake.New(script)
			loop.models = fakeResolver{provider}
			if got := loop.Run(t.Context(), state.run.ID); got != OutcomeFailed || state.run.ErrorCode != "MODEL_SUBMISSION_TOOL_VIOLATION" || len(state.calls) != 0 || len(host.validated) != 0 || len(provider.Requests()) != 1 {
				t.Fatal("mixed character submission reached IO or validation", kind, got, state.run.ErrorCode, len(state.calls))
			}
		})
	}
}

func TestCharacterTransportUsesExistingCorrectionBudgetAndAudit(t *testing.T) {
	loop, state, host, payload := characterTransportFixture(t)
	bad := strings.Replace(payload, "frozen-source-hash", "wrong-source-hash", 1)
	provider := fake.New(characterTransportScript(bad), characterTransportScript(bad), characterTransportScript(bad), characterTransportScript(bad))
	loop.models = fakeResolver{provider}
	if got := loop.Run(t.Context(), state.run.ID); got != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || len(provider.Requests()) != 3 || len(host.validated) != 0 {
		t.Fatal("character control acquired a separate correction allowance or reached scientific validation", got, state.run.ErrorCode, len(provider.Requests()))
	}
	for turn := 1; turn <= 3; turn++ {
		if state.journalDrafts[turn] != bad {
			t.Fatal("raw rejected character payload was not preserved", turn)
		}
	}
	if !strings.Contains(state.run.ErrorMessage, "最后一次编辑未进入完整科学校验") || !strings.Contains(state.run.ErrorDetails, "sourceSHA256") {
		t.Fatal("wrong source hash was not diagnosed as an editing submission error", state.run.ErrorMessage, state.run.ErrorDetails)
	}
}
