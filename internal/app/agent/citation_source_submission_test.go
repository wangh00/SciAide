package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
)

var firstCitationSchema = json.RawMessage(`{"title":"citation-source-v2","type":"object","additionalProperties":false,"required":["summary","recommendedReferences","citationAssessments"],"properties":{"summary":{"type":"string"},"recommendedReferences":{"type":"array","items":{"type":"string"}},"citationAssessments":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["reference","decision","reason","supportingQuote"],"properties":{"reference":{"type":"string"},"decision":{"type":"string","enum":["support","exclude"]},"reason":{"type":"string"},"supportingQuote":{"type":"string","maxLength":700}}}}}}`)

type firstCitationHost struct{ *submissionHostFixture }

func (h *firstCitationHost) ValidateResearchSubmission(_ context.Context, a, b, c, text string) (json.RawMessage, error) {
	h.validated = append(h.validated, [4]string{a, b, c, text})
	if i := len(h.validated) - 1; i < len(h.errors) && h.errors[i] != nil {
		return nil, h.errors[i]
	}
	v, _, err := workflow.NormalizeAIStageSubmission(text, firstCitationSchema)
	return v, err
}
func firstCitationFixture(t *testing.T, quote string) (*Loop, *loopState, *firstCitationHost, *citationSourceSubmission, string) {
	t.Helper()
	l, state, _ := newLoopFixture(t, nil)
	ref := tool.CitationRef{Kind: citation.KindKnowledgeChunk, ProjectID: "project", IndexVersionID: "index", DocumentID: "document", AttachmentID: "attachment", ChunkID: "chunk", SourceName: "fixture.txt", Locator: "lines:1", Quote: quote, QuoteSHA256: citation.QuoteSHA256(quote), SourceEnd: utf8.RuneCountInString(quote)}
	old := ref
	old.Reference = citation.KnowledgeReference("workflow", ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	ref.Reference = citation.KnowledgeReference(state.run.ID, ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	input := mustAgentJSON(t, map[string]any{"candidates": []tool.CitationRef{old}})
	prompt := "<stage_input>\n" + strings.ReplaceAll(string(input), old.Reference, ref.Reference) + "\n</stage_input>"
	c := stageBudgetContract(input, prompt)
	c.ID, c.NodeID, c.WorkflowStepID, c.Attempt, c.NodeKind = "execution", "evidence_screening", "step", 1, string(workflow.NodeAgentStage)
	c.PromptVersion = "selected-evidence-v6|" + workflow.CitationSourceProtocolVersion
	c.OutputSchema = firstCitationSchema
	c.OutputSchemaSHA256 = fmt.Sprintf("%x", sha256.Sum256(c.OutputSchema))
	l.runs = workflowContractRuns{loopState: state, contract: c, found: true}
	state.workflowAI = true
	state.messages[0].Parts = []conversation.MessagePart{{Type: "text", Text: prompt}}
	call := tool.Call{ID: "seed", ProviderCallID: "seed", RunID: state.run.ID, ToolName: citation.WorkflowSeedToolName, Status: tool.CallCompleted, Arguments: json.RawMessage(`{}`), Result: &tool.Result{Status: tool.ResultSuccess, Citations: []tool.CitationRef{ref}}}
	state.calls[call.ID] = call
	state.callOrder = append(state.callOrder, call.ID)
	host := &firstCitationHost{&submissionHostFixture{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{WorkflowRunID: "workflow", WorkflowStepID: "step", StructuredOutputRequired: true, StructuredOutputSchema: firstCitationSchema}}}}
	l.research = host
	s, err := loadCitationSourceSubmission("project", state.run, c, true, []tool.Call{call})
	if err != nil {
		t.Fatal(err)
	}
	return l, state, host, s, ref.Reference
}
func firstCitationPayload(t *testing.T, s *citationSourceSubmission, ref string, ids []string, offsets ...int) string {
	t.Helper()
	selection := map[string]any{"sourceSegmentIds": ids}
	if len(offsets) == 2 {
		selection["startOffset"], selection["endOffset"] = offsets[0], offsets[1]
	}
	return string(mustAgentJSON(t, map[string]any{"summary": "Bounded scientific claim", "recommendedReferences": []string{ref}, "citationAssessments": []any{map[string]any{"reference": ref, "decision": "support", "reason": "Original scientific reason", "quoteSelection": selection}}}))
}
func TestCitationFirstSubmissionCopiesOriginalOnFirstCallAndKeepsJournal(t *testing.T) {
	l, state, h, s, ref := firstCitationFixture(t, "誊写🙂 &amp; 原文。")
	payload := firstCitationPayload(t, s, ref, []string{s.sources[ref].parts[0].ID})
	p := fake.New(submissionToolScript(payload))
	l.models = fakeResolver{p}
	if got := l.Run(t.Context(), state.run.ID); got != OutcomeCompleted {
		t.Fatal(got, state.run.ErrorCode, state.run.ErrorDetails)
	}
	if len(p.Requests()) != 1 || len(h.validated) != 1 || state.journalDrafts[1] != payload || !strings.Contains(h.validated[0][3], "誊写🙂") {
		t.Fatal("first submission retyped, retried, lost audit or bypassed host", h.validated, state.journalDrafts)
	}
	var exact struct {
		Rows []struct {
			Quote string `json:"supportingQuote"`
		} `json:"citationAssessments"`
	}
	if json.Unmarshal([]byte(h.validated[0][3]), &exact) != nil || len(exact.Rows) != 1 || exact.Rows[0].Quote != "誊写🙂 &amp; 原文。" {
		t.Fatal("exact source changed", h.validated)
	}
	r := p.Requests()[0]
	if !r.RequireTool || !strings.Contains(string(r.Tools[len(r.Tools)-1].InputSchema), "quoteSelection") {
		t.Fatal("required first native selection not offered")
	}
	if strings.Contains(string(s.menu), "誊写") || strings.Count(submissionRequestText(r), "<citation_submission_sources>") != 1 {
		t.Fatal("navigation duplicated source text or catalog")
	}
}
func TestCitationFirstSubmissionRetainsOriginalLengthContinuousRangeAndUnicode(t *testing.T) {
	for _, n := range []int{700, 711, 765, 723} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			_, _, _, s, ref := firstCitationFixture(t, strings.Repeat("界", n))
			source := s.sources[ref]
			ids := []string{}
			for _, p := range source.parts {
				ids = append(ids, p.ID)
			}
			request := model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, HostToolReferences: true, Content: s.contract.PromptText}}}
			all := firstCitationPayload(t, s, ref, ids)
			value, err := s.resolve(all, request, nil)
			if n == 700 {
				if err != nil || !strings.Contains(string(value), strings.Repeat("界", 700)) {
					t.Fatal(err)
				}
			} else {
				var size *citationRepairLengthError
				if !errors.As(err, &size) || size.Actual != n || size.Limit != 700 {
					t.Fatal("old limit relaxed", err)
				}
			}
			shorter := firstCitationPayload(t, s, ref, []string{source.parts[0].ID}, 1, 5)
			value, err = s.resolve(shorter, request, nil)
			if err != nil || !strings.Contains(string(value), `"supportingQuote":"界界界界"`) {
				t.Fatal("Unicode offsets changed source", string(value), err)
			}
			for _, bad := range [][]string{{ids[0], ids[0]}, {"another-attempt"}} {
				if _, err := s.resolve(firstCitationPayload(t, s, ref, bad), request, nil); err == nil {
					t.Fatal("unissued/nonconsecutive selection accepted")
				}
			}
		})
	}
}
func TestCitationFirstSubmissionRejectsHiddenChangedOrUntrustedSource(t *testing.T) {
	_, state, _, s, ref := firstCitationFixture(t, "Exact synthetic source.")
	payload := firstCitationPayload(t, s, ref, []string{s.sources[ref].parts[0].ID})
	_, err := s.resolve(payload, model.ChatRequest{}, nil)
	var missing *citationSourceNotVisibleError
	if !errors.As(err, &missing) {
		t.Fatal("index or stored source counted as visible", err)
	}
	call := state.calls["seed"]
	call.ID = "read"
	call.ProviderCallID = "read"
	call.ToolName = "builtin.knowledge.read"
	call.Result.Text = ref + "\nExact synthetic source."
	view := model.Message{Role: model.RoleTool, ToolCallID: "read", Content: tool.BuildModelContextSnapshot("read", "", *call.Result)}
	request := model.ChatRequest{Messages: []model.Message{view}}
	if _, err := s.resolve(payload, request, []tool.Call{call}); err != nil {
		t.Fatal("current signed tool evidence rejected", err)
	}
	request = stageSubmissionEvidence(request)
	if _, err := s.resolve(payload, request, []tool.Call{call}); err != nil {
		t.Fatal("submission lost current tool evidence", err)
	}
	for _, change := range []string{"other-run", "failed", "untrusted", "hidden-tail"} {
		t.Run(change, func(t *testing.T) {
			c := call
			r := model.ChatRequest{Messages: []model.Message{view}}
			switch change {
			case "other-run":
				c.RunID = "other"
			case "failed":
				c.Status = tool.CallFailed
			case "untrusted":
				c.ToolName = "untrusted.tool"
			case "hidden-tail":
				var env map[string]any
				_ = json.Unmarshal([]byte(view.Content), &env)
				env["text"] = ref + "\nExact synthetic"
				r.Messages[0].Content = string(mustAgentJSON(t, env))
			}
			if _, err := s.resolve(payload, r, []tool.Call{c}); err == nil {
				t.Fatal("wrong authority or truncated evidence accepted")
			}
		})
	}
	call.Result.Citations[0].Quote = "changed without matching hash"
	if err := s.refresh([]tool.Call{call}); err != nil {
		t.Fatal(err)
	}
	if len(s.sources) != 0 {
		t.Fatal("changed bytes minted a selectable source")
	}
}
func TestCitationFirstSubmissionKeepsScienceFailureAndOriginalCorrectionBudget(t *testing.T) {
	l, state, h, s, ref := firstCitationFixture(t, "Exact original result.")
	payload := firstCitationPayload(t, s, ref, []string{s.sources[ref].parts[0].ID})
	reject := &apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", UserMessage: "Independent scientific/source check rejected the claim"}
	h.errors = []error{reject, reject, reject}
	p := fake.New(submissionToolScript(payload), submissionToolScript(payload), submissionToolScript(payload))
	l.models = fakeResolver{p}
	if got := l.Run(t.Context(), state.run.ID); got != OutcomeFailed || len(p.Requests()) != 3 || len(h.validated) != 3 || state.run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" {
		t.Fatal("selection bypassed science or changed correction budget", got, state.run)
	}
	for _, v := range h.validated {
		if strings.Contains(v[3], "quoteSelection") || !strings.Contains(v[3], "Exact original result.") {
			t.Fatal("host saw wire selectors instead of canonical complete result")
		}
	}
}

func TestCitationFirstSubmissionSummaryFormatIsNotSourceSelectionFailure(t *testing.T) {
	l, state, h, s, ref := firstCitationFixture(t, "Exact original result.")
	var schema map[string]any
	if err := json.Unmarshal(s.contract.OutputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	schema["properties"].(map[string]any)["summary"].(map[string]any)["maxLength"] = 1
	c := s.contract
	c.OutputSchema = mustAgentJSON(t, schema)
	c.OutputSchemaSHA256 = fmt.Sprintf("%x", sha256.Sum256(c.OutputSchema))
	l.runs = workflowContractRuns{loopState: state, contract: c, found: true}
	h.value.StructuredOutputSchema = c.OutputSchema
	payload := firstCitationPayload(t, s, ref, []string{s.sources[ref].parts[0].ID})
	p := fake.New(submissionToolScript(payload), submissionToolScript(payload), submissionToolScript(payload))
	l.models = fakeResolver{p}
	if got := l.Run(t.Context(), state.run.ID); got != OutcomeFailed || len(p.Requests()) != 3 || len(h.validated) != 0 || state.run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" {
		t.Fatal("format failure bypassed schema, science boundary or original budget", got, state.run)
	}
	if !strings.Contains(state.run.ErrorMessage, "阶段提交格式") || strings.Contains(state.run.ErrorMessage, "原文选择") || !strings.Contains(state.run.ErrorDetails, "$.summary") || strings.Contains(state.run.ErrorDetails, "invalid current citation selection") {
		t.Fatal("ordinary summary format failure blamed source selection", state.run.ErrorMessage, state.run.ErrorDetails)
	}
}
func TestCitationFirstSubmissionInvalidBindingAndContextStopBeforeModel(t *testing.T) {
	l, state, _, s, _ := firstCitationFixture(t, "Exact original result.")
	c := s.contract
	c.InputSHA256 = strings.Repeat("0", 64)
	if _, err := loadCitationSourceSubmission("project", state.run, c, true, nil); err == nil {
		t.Fatal("tampered input accepted")
	}
	r := model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: strings.Repeat("x", 100)}}}
	if _, err := s.request(r, nil, nil, 50); err == nil {
		t.Fatal("mandatory selection silently displaced evidence")
	}
	c = s.contract
	c.PromptVersion = "selected-evidence-v6"
	l.runs = workflowContractRuns{loopState: state, contract: c, found: true}
	if source, err := loadCitationSourceSubmission("project", state.run, c, true, nil); err != nil || source != nil {
		t.Fatal("legacy execution silently upgraded", err)
	}
}

func TestCitationFirstSubmissionPagedSourceRequiresCurrentExactRange(t *testing.T) {
	_, _, _, s, ref := firstCitationFixture(t, strings.Repeat("前言🙂 ", 200)+"Exact result.")
	source := s.sources[ref]
	view := &reviewSourceSubmission{catalog: &workflow.ReviewSourceCatalog{CitationAliases: s.aliases}, materials: s.materials}
	message, call := sourceVisiblePage(t, view, 0, 30, 1, source.pointer)
	request := model.ChatRequest{Messages: []model.Message{message}}
	payload := firstCitationPayload(t, s, ref, []string{source.parts[0].ID}, 0, 20)
	if _, err := s.resolve(payload, request, []tool.Call{call}); err != nil {
		t.Fatal("verified current page rejected", err)
	}
	payload = firstCitationPayload(t, s, ref, []string{source.parts[0].ID}, 0, 31)
	var hidden *citationSourceNotVisibleError
	if _, err := s.resolve(payload, request, []tool.Call{call}); !errors.As(err, &hidden) {
		t.Fatal("unseen source tail accepted", err)
	}
	request = stageSubmissionEvidence(request)
	if _, err := s.resolve(firstCitationPayload(t, s, ref, []string{source.parts[0].ID}, 0, 20), request, []tool.Call{call}); err != nil {
		t.Fatal("submission lost exact visible page", err)
	}
	banner := researchSubmissionFailureMessage(&citationSourceSubmissionError{cause: &citationRepairLengthError{Reference: ref, Actual: 765, Limit: 700}})
	if !strings.Contains(banner, "765") || !strings.Contains(banner, "700") || strings.Contains(banner, source.quote) {
		t.Fatal("length cause masked or source leaked", banner)
	}
}

func TestCitationFirstSubmissionAliasInsideSourceIsNotUnseenOriginalText(t *testing.T) {
	b := tool.CitationRef{Kind: citation.KindKnowledgeChunk, ProjectID: "project", IndexVersionID: "index-b", DocumentID: "doc-b", AttachmentID: "attachment-b", ChunkID: "chunk-b", SourceName: "b.txt", Locator: "lines:1", Quote: "Other signed evidence.", QuoteSHA256: citation.QuoteSHA256("Other signed evidence.")}
	oldB := citation.KnowledgeReference("workflow", b.IndexVersionID, b.ChunkID, b.QuoteSHA256)
	_, state, _, s, ref := firstCitationFixture(t, "prefix "+oldB+" suffix")
	seed := state.calls["seed"]
	b.Reference = citation.KnowledgeReference(state.run.ID, b.IndexVersionID, b.ChunkID, b.QuoteSHA256)
	seed.Result.Citations = append(seed.Result.Citations, b)
	c := s.contract
	var input map[string]any
	_ = json.Unmarshal(c.Input, &input)
	originalB := b
	originalB.Reference = oldB
	input["candidates"] = append(input["candidates"].([]any), originalB)
	c.Input = mustAgentJSON(t, input)
	c.InputSHA256 = fmt.Sprintf("%x", sha256.Sum256(c.Input))
	c.PromptText = "<stage_input>\n" + strings.ReplaceAll(strings.ReplaceAll(string(c.Input), citation.KnowledgeReference("workflow", "index", "chunk", citation.QuoteSHA256(s.sources[ref].quote)), ref), oldB, b.Reference) + "\n</stage_input>"
	c.PromptSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(c.PromptText)))
	s, err := loadCitationSourceSubmission("project", state.run, c, true, []tool.Call{seed})
	if err != nil {
		t.Fatal(err)
	}
	request := model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: c.PromptText, HostToolReferences: true}}}
	id := s.sources[ref].parts[0].ID
	// Full marker and partial-character overlaps must both reject restoration of
	// old bytes that the actual request replaced with another Chat marker.
	for _, span := range [][2]int{{7, 7 + len(oldB)}, {10, 15}} {
		_, err = s.resolve(firstCitationPayload(t, s, ref, []string{id}, span[0], span[1]), request, []tool.Call{seed})
		var hidden *citationSourceNotVisibleError
		if !errors.As(err, &hidden) {
			t.Fatal("rewritten source alias certified as original visible text", err)
		}
	}
	if _, err = s.resolve(firstCitationPayload(t, s, ref, []string{id}, 0, 6), request, []tool.Call{seed}); err != nil {
		t.Fatal("unaffected exact range blocked", err)
	}
	view := &reviewSourceSubmission{catalog: &workflow.ReviewSourceCatalog{}, materials: s.materials}
	page, pageCall := sourceVisiblePage(t, view, 7, 7+len(oldB), 33, s.sources[ref].pointer)
	withPage := request
	withPage.Messages = append(append([]model.Message(nil), request.Messages...), page)
	if _, err = s.resolve(firstCitationPayload(t, s, ref, []string{id}, 7, 7+len(oldB)), withPage, []tool.Call{seed, pageCall}); err != nil {
		t.Fatal("actual literal task-read page blocked", err)
	}
	read := seed
	read.ID, read.ProviderCallID, read.ToolName = "read", "read", "builtin.knowledge.read"
	read.Result = &tool.Result{Status: tool.ResultSuccess, Text: ref + "\n" + s.sources[ref].quote, Citations: seed.Result.Citations[:1]}
	request.Messages = append(request.Messages, model.Message{Role: model.RoleTool, ToolCallID: "read", Content: tool.BuildModelContextSnapshot("read", "", *read.Result)})
	if _, err = s.resolve(firstCitationPayload(t, s, ref, []string{id}, 7, 7+len(oldB)), request, []tool.Call{seed, read}); err != nil {
		t.Fatal("actual current verbatim tool source rejected", err)
	}
}
