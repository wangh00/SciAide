package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
)

type revisionEditHostFixture struct {
	*submissionHostFixture
	base     *workflow.RevisionEditBase
	err      error
	bindings []workflow.StageMaterialRequest
}

func (h *revisionEditHostFixture) StageRevisionEdit(_ context.Context, binding workflow.StageMaterialRequest) (*workflow.RevisionEditBase, error) {
	h.bindings = append(h.bindings, binding)
	return h.base, h.err
}

func revisionEditScript(payload string) []fake.Step {
	return []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: "edit", Name: stageEditTool, Arguments: json.RawMessage(payload)}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
}

func bindRevisionEditAgentFixture(t *testing.T, loop *Loop, state *loopState) (*revisionEditHostFixture, string) {
	t.Helper()
	bindReadConvergenceFixture(t, loop, state, 10)
	base := json.RawMessage(`{"items":["保留前后，腾写工作"],"state":"ready"}`)
	hash := fmt.Sprintf("%x", sha256.Sum256(base))
	host := &revisionEditHostFixture{submissionHostFixture: &submissionHostFixture{staticResearchGuidance: loop.research.(*staticResearchGuidance)}, base: &workflow.RevisionEditBase{Analysis: base, SHA256: hash, Pointer: "/_revisionEdit/priorOutput"}}
	loop.research = host
	return host, fmt.Sprintf(`{"baseSHA256":%q,"textEdits":[{"path":"/items/0","before":"腾写","after":"誊写"}]}`, hash)
}

func TestInitialRevisionEditUsesOneTurnAndAuditsRawPatch(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil)
	host, payload := bindRevisionEditAgentFixture(t, loop, state)
	provider = fake.New(revisionEditScript(payload))
	loop.models = fakeResolver{provider}
	if got := loop.Run(t.Context(), state.run.ID); got != OutcomeCompleted {
		t.Fatal(got, state.run.ErrorCode, state.run.ErrorMessage)
	}
	requests := provider.Requests()
	if len(requests) != 1 || len(host.validated) != 1 || len(state.calls) != 0 || !strings.Contains(host.validated[0][3], "保留前后，誊写工作") || state.journalDrafts[1] != payload {
		t.Fatal("edit was retyped, not audited or sent to IO", len(requests), host.validated, state.journalDrafts)
	}
	if len(host.bindings) != 1 || host.bindings[0].ChatRunID != state.run.ID || host.bindings[0].WorkflowStepID != "step" || !strings.Contains(submissionRequestText(requests[0]), "including methodSummary") {
		t.Fatal("missing live binding or metadata reuse instruction")
	}
}

func TestInitialRevisionEditKeepsOriginalBaseAfterWrongHash(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil)
	host, good := bindRevisionEditAgentFixture(t, loop, state)
	bad := strings.Replace(good, host.base.SHA256, "wrong", 1)
	provider = fake.New(revisionEditScript(bad), revisionEditScript(good))
	loop.models = fakeResolver{provider}
	if got := loop.Run(t.Context(), state.run.ID); got != OutcomeCompleted || len(provider.Requests()) != 2 || len(host.validated) != 1 {
		t.Fatal("bad patch rebased the original or bypassed correction budget", got, state.run.ErrorCode, len(provider.Requests()), host.validated)
	}
	if !strings.Contains(submissionRequestText(provider.Requests()[1]), host.base.SHA256) || state.journalDrafts[1] != bad || state.journalDrafts[2] != good {
		t.Fatal("wrong-hash failure erased evidence or changed the original base")
	}
}

func TestInitialRevisionEditDoesNotRenewCorrectionBudget(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil)
	host, payload := bindRevisionEditAgentFixture(t, loop, state)
	bad := strings.Replace(payload, host.base.SHA256, "wrong", 1)
	provider = fake.New(revisionEditScript(bad), revisionEditScript(bad), revisionEditScript(bad), revisionEditScript(bad))
	loop.models = fakeResolver{provider}
	if got := loop.Run(t.Context(), state.run.ID); got != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || len(provider.Requests()) != 3 || len(host.validated) != 0 {
		t.Fatal("new transport acquired a separate correction allowance", got, state.run.ErrorCode, len(provider.Requests()))
	}
	if !strings.Contains(state.run.ErrorMessage, "局部编辑请求") || !strings.Contains(state.run.ErrorMessage, "未进入完整科学校验") || !strings.Contains(state.run.ErrorDetails, "baseSHA256") {
		t.Fatal("edit protocol failure was misreported as scientific rejection", state.run.ErrorMessage, state.run.ErrorDetails)
	}
}

func TestInitialRevisionEditRetainsScientificRejectionAndFullReplacement(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil)
	host, payload := bindRevisionEditAgentFixture(t, loop, state)
	host.errors = []error{&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", UserMessage: "Scientific method is still unsupported"}}
	provider = fake.New(revisionEditScript(payload), submissionToolScript(`{"items":["explicit scientifically corrected full replacement"],"state":"ready"}`))
	loop.models = fakeResolver{provider}
	if got := loop.Run(t.Context(), state.run.ID); got != OutcomeCompleted || len(host.validated) != 2 || len(provider.Requests()) != 2 || !strings.Contains(submissionRequestText(provider.Requests()[1]), "Scientific method is still unsupported") {
		t.Fatal("initial edit bypassed scientific rejection or disabled full replacement", got, state.run.ErrorCode, host.validated)
	}
}

func TestInitialRevisionEditMixedOrUnofferedControlsNeverExecuteIO(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprint("mixed=", mixed), func(t *testing.T) {
			loop, state, provider := newLoopFixture(t, nil)
			host, payload := bindRevisionEditAgentFixture(t, loop, state)
			script := revisionEditScript(payload)
			if mixed {
				script = append(script[:1], append(convergenceReadScript(1, 0)[1:2], script[1:]...)...)
			} else {
				host.base = nil
			}
			provider = fake.New(script)
			loop.models = fakeResolver{provider}
			if got := loop.Run(t.Context(), state.run.ID); got != OutcomeFailed || state.run.ErrorCode != "MODEL_SUBMISSION_TOOL_VIOLATION" || len(state.calls) != 0 || len(host.validated) != 0 || len(provider.Requests()) != 1 {
				t.Fatal("unoffered/mixed control executed or was retried", got, state.run.ErrorCode, len(state.calls), host.validated)
			}
		})
	}
}

func TestInitialRevisionEditBrokenHostBaselineStopsBeforeModel(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil)
	host, _ := bindRevisionEditAgentFixture(t, loop, state)
	host.err = &apperr.Error{Code: "WORKFLOW_REVISION_WRITE_BASELINE_INVALID", UserMessage: "immutable snapshot missing"}
	if got := loop.Run(t.Context(), state.run.ID); got != OutcomeFailed || len(provider.Requests()) != 0 || state.run.ErrorCode != "WORKFLOW_REVISION_WRITE_BASELINE_INVALID" {
		t.Fatal("host baseline problem caused paid retries", got, state.run.ErrorCode)
	}
}

func TestInitialRevisionEditIsOptionalAndDoesNotDisplaceEvidence(t *testing.T) {
	base := json.RawMessage(`{"methodSummary":"original","markdown":"unchanged"}`)
	edit := &stageRevisionEdit{base: workflow.RevisionEditBase{Analysis: base, SHA256: fmt.Sprintf("%x", sha256.Sum256(base)), Pointer: "/_revisionEdit/priorOutput"}, schema: workflow.RevisionTextEditSchema(base)}
	admitted := stageSubmissionRequest(model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: strings.Repeat("EXACT_SCIENTIFIC_EVIDENCE ", 100)}}}, submissionRepairSchema)
	limit := currentRequestBudgetTokens(admitted, nil)
	got := withStageRevisionEdit(admitted, edit, nil, limit)
	if len(got.Tools) != len(admitted.Tools) || submissionRequestText(got) != submissionRequestText(admitted) {
		t.Fatal("optional edit displaced already-admitted evidence")
	}
	got = withStageRevisionEdit(admitted, edit, nil, limit+6000)
	if len(got.Tools) != 2 || got.ForcedTool != "" || !got.RequireTool || got.DisableTools || admitted.ForcedTool != stageSubmissionTool || len(admitted.Tools) != 1 {
		t.Fatal("dedicated edit locked full fallback, exceeded its budget or mutated source")
	}
	for _, source := range []model.ChatRequest{admitted, stageExplorationRequest(model.ChatRequest{}, submissionRepairSchema)} {
		updated := withStageRevisionEdit(source, edit, nil, 20000)
		text := submissionRequestText(updated)
		if strings.Contains(text, "The only available function is submit_stage_result") || strings.Contains(text, "When you have the stage result, call submit_stage_result EXACTLY ONCE") || !strings.Contains(text, "Call exactly one") && !strings.Contains(text, "call exactly one") {
			t.Fatal("conflicting full-only instruction remains with the new control")
		}
	}
}

func TestInitialRevisionEditResolvesChatCitationsAfterCanonicalMerge(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(fmt.Sprint("reuse=", reuse), func(t *testing.T) {
			loop, state, provider := newLoopFixture(t, nil)
			host, _ := bindRevisionEditAgentFixture(t, loop, state)
			quote := "Exact retained source"
			ref := tool.CitationRef{Kind: citation.KindKnowledgeChunk, ProjectID: "project", IndexVersionID: "index", DocumentID: "document", AttachmentID: "attachment", ChunkID: "chunk", SourceName: "fixture.pdf", Locator: "page:1", Quote: quote, QuoteSHA256: citation.QuoteSHA256(quote), SourceEnd: len(quote)}
			workflowRef := citation.KnowledgeReference("workflow", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
			ref.Reference = citation.KnowledgeReference(state.run.ID, ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
			host.base.Analysis = json.RawMessage(fmt.Sprintf(`{"items":["腾写 %s"],"state":"ready"}`, workflowRef))
			host.base.SHA256 = fmt.Sprintf("%x", sha256.Sum256(host.base.Analysis))
			host.base.CitationAliases = map[string]string{ref.Reference: workflowRef}
			state.calls["seed"] = tool.Call{ID: "seed", RunID: state.run.ID, ToolName: citation.WorkflowSeedToolName, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Citations: []tool.CitationRef{ref}}}
			state.callOrder = append(state.callOrder, "seed")
			edits := []map[string]string{}
			if !reuse {
				edits = append(edits, map[string]string{"path": "/items/0", "before": "腾写 " + ref.Reference, "after": "誊写 " + ref.Reference})
			}
			payload, _ := json.Marshal(map[string]any{"baseSHA256": host.base.SHA256, "textEdits": edits})
			provider = fake.New(revisionEditScript(string(payload)))
			loop.models = fakeResolver{provider}
			if got := loop.Run(t.Context(), state.run.ID); got != OutcomeCompleted || len(state.messages[1].Citations) != 1 || state.messages[1].Citations[0].Reference != ref.Reference || !strings.Contains(state.messages[1].Parts[0].Text, ref.Reference) || state.journalDrafts[1] != string(payload) {
				t.Fatal("edit lost the original journal or current Chat citation binding", got, state.run.ErrorCode, state.messages[1].Citations)
			}
		})
	}
}

func TestInitialCharacterEditCopiesSourceWithoutRegeneratingGlyph(t *testing.T) {
	loop, state, provider := newLoopFixture(t, nil)
	host, _ := bindRevisionEditAgentFixture(t, loop, state)
	host.base.Analysis = json.RawMessage(`{"items":["原样誉写"],"state":"ready"}`)
	host.base.SHA256 = fmt.Sprintf("%x", sha256.Sum256(host.base.Analysis))
	host.base.Characters = &workflow.RevisionCharacterSource{Version: "revision-character-source-v1", SHA256: "frozen-source-hash", Characters: []workflow.RevisionSourceCharacter{{ID: "U+8A8A", Text: "誊", Pointer: "/_reviewCorrections/corrections/0/text", Offset: 4}}}
	payload := fmt.Sprintf(`{"baseSHA256":%q,"sourceSHA256":"frozen-source-hash","characterEdits":[{"path":"/items/0","before":"誉","replacementId":"U+8A8A"}]}`, host.base.SHA256)
	script := revisionEditScript(payload)
	script[0].Event.ToolCall.Name = stageCharacterEditTool
	provider = fake.New(script)
	loop.models = fakeResolver{provider}
	if got := loop.Run(t.Context(), state.run.ID); got != OutcomeCompleted || len(host.validated) != 1 || !strings.Contains(host.validated[0][3], "原样誊写") || state.journalDrafts[1] != payload || len(state.calls) != 0 {
		t.Fatal("source copying skipped audit/validator or executed IO", got, state.run.ErrorCode, host.validated)
	}
	request := provider.Requests()[0]
	if len(request.Tools) != 4 { // Frozen task read + result + literal edit + character copy.
		t.Fatal("character copy displaced an existing allowed control", len(request.Tools))
	}
	if !strings.Contains(submissionRequestText(request), "U+8A8A") || !strings.Contains(submissionRequestText(request), "absolute JSON pointers") {
		t.Fatal("source menu or pointer contract missing")
	}
}
