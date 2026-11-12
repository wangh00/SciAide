package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
)

type firstSourceHostFixture struct {
	*submissionHostFixture
	catalog   *workflow.ReviewSourceCatalog
	sourceErr error
}

func TestFirstReviewSourcePagedFinalRequestHasOneConsistentContract(t *testing.T) {
	loop, state, _ := newLoopFixture(t, nil)
	h, _ := bindFirstSourceFixture(t, loop, state)
	input := mustAgentJSON(t, map[string]any{"context": map[string]any{"markdown": strings.Repeat("Current original report. ", 5000)}})
	prompt := "<stage_input>\n" + string(input) + "\n</stage_input>"
	c := stageBudgetContract(input, prompt)
	c.PromptVersion = "dynamic-independent-review-v9|" + workflow.ReviewSourceProtocolVersion
	run := chat.Run{ID: "run", UserMessageID: "u"}
	messages := []conversation.Message{checkpointText("u", "run", prompt)}
	defs := []tool.Definition{{QualifiedName: workflow.ResearchTaskReadTool, Description: "Read current frozen material", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	view := budgetedStageMessages(messages, run, c, defs, nil, "", "", "", 250000, 80000)
	if text := conversationText(view[0]); !strings.Contains(text, `"$read"`) || strings.Contains(text, "Use original pointers and literal original quotes") || !strings.Contains(text, "Select current sourceSegmentIds") {
		t.Fatal("actual budget projection reintroduced old basis transport")
	}
	r, _, err := NewContextBuilder(0).buildWithResearchState(t.Context(), view, "", "u", defs, nil, skill.RunContext{}, "", "", "", ContextLimits{EffectiveTokens: 250000, AutoCompactTokens: 80000, CurrentUserHostToolReferences: true}, contextmemory.Checkpoint{})
	if err != nil {
		t.Fatal(err)
	}
	b := h.catalog.Binding
	b.InputSHA256 = fmt.Sprintf("%x", sha256.Sum256(input))
	catalog, err := workflow.BuildReviewSourceCatalog(t.Context(), input, b)
	if err != nil {
		t.Fatal(err)
	}
	s := &reviewSourceSubmission{catalog: catalog, fullSchema: workflow.ReviewSourceSubmissionSchema(workflow.ReviewSubmissionSchema(h.value.StructuredOutputSchema))}
	r, err = s.request(r, nil, 250000)
	if err != nil {
		t.Fatal(err)
	}
	if text := submissionRequestText(r); strings.Contains(text, "alternative to the legacy fenced JSON") || strings.Contains(text, "Use original pointers and literal original quotes") || strings.Count(text, "<review_submission_sources>") != 1 {
		t.Fatal("final exploration request contains conflicting contracts")
	}
	text := submissionRequestText(r)
	for _, required := range []string{"reviewedInputSha256", "requiredCorrections", "revisionPlan", "correctionIndex", "do NOT add correction or revision objects"} {
		if !strings.Contains(text, required) {
			t.Fatal("final review request lost strict frozen-shape reminder", required)
		}
	}
	if conversationText(messages[0]) != prompt || !r.RequireTool || len(r.Tools) != 2 {
		t.Fatal("projection changed source/permissions or removed required native action")
	}
}

func TestFirstReviewSourceIssueRepairKeepsSelectorsAndFullValidation(t *testing.T) {
	loop, state, _ := newLoopFixture(t, nil)
	h, payload := bindFirstSourceFixture(t, loop, state)
	schema := h.value.StructuredOutputSchema
	missing := &workflow.ReviewMissingFindingError{ID: "issue-1-2"}
	h.errors = []error{&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", UserMessage: "Historical finding requires explicit current assessment", Cause: missing}}
	canonical, err := h.catalog.ResolveSubmission(payload, workflow.ReviewSourceSubmissionSchema(workflow.ReviewSubmissionSchema(schema)))
	if err != nil {
		t.Fatal(err)
	}
	repair := newReviewRepair(string(canonical), schema, missing)
	if repair == nil || !repair.issues {
		t.Fatal("current issue repair unavailable")
	}
	var wire map[string]any
	_ = json.Unmarshal([]byte(payload), &wire)
	added := wire["reviewFindings"].([]any)[0].(map[string]any)
	added["id"] = missing.ID
	patch := issuePatch(repair, map[string]any{"reviewIssueReplacements": []any{added}})
	script := submissionToolScript(patch)
	script[0].Event.ToolCall.Name = stageRepairTool
	p := fake.New(submissionToolScript(payload), script)
	loop.models = fakeResolver{p}
	if got := loop.Run(t.Context(), state.run.ID); got != OutcomeCompleted || len(h.validated) != 2 || len(p.Requests()) != 2 || state.journalDrafts[1] != payload || state.journalDrafts[2] != patch {
		t.Fatal("source issue repair bypassed validation or rewrote raw audit", got, state.run.ErrorCode, state.run.ErrorDetails)
	}
	var last map[string]any
	_ = json.Unmarshal([]byte(h.validated[1][3]), &last)
	if len(last["reviewFindings"].([]any)) != 2 || strings.Contains(h.validated[1][3], "sourceSegmentIds") {
		t.Fatal("issue merge lost unchanged finding or failed to copy source")
	}
	for _, def := range p.Requests()[1].Tools {
		if def.Name == stageRepairTool && (!strings.Contains(string(def.InputSchema), "sourceSegmentIds") || strings.Contains(string(def.InputSchema), `"quote"`)) {
			t.Fatal("local changed finding reverted to literal quote transport")
		}
	}
}

func (h *firstSourceHostFixture) ValidateResearchSubmission(_ context.Context, conversationID, runID, stepID, text string) (json.RawMessage, error) {
	h.validated = append(h.validated, [4]string{conversationID, runID, stepID, text})
	if index := len(h.validated) - 1; index < len(h.errors) && h.errors[index] != nil {
		return nil, h.errors[index]
	}
	value, _, err := workflow.NormalizeAIStageSubmission(text, h.value.StructuredOutputSchema)
	return value, err
}

func (h *firstSourceHostFixture) StageReviewSources(_ context.Context, request workflow.StageMaterialRequest) (*workflow.ReviewSourceCatalog, error) {
	if h.sourceErr != nil {
		return nil, h.sourceErr
	}
	if h.catalog.Binding.StageMaterialRequest != request {
		return nil, fmt.Errorf("test source binding mismatch")
	}
	return h.catalog, nil
}

func bindFirstSourceFixture(t *testing.T, loop *Loop, state *loopState) (*firstSourceHostFixture, string) {
	t.Helper()
	schema, base := issueRepairFixture(t)
	input := json.RawMessage(`{"context":{"markdown":"当前誊写原文，不能重打成誉写。"}}`)
	c := stageBudgetContract(input, "Review the exact frozen source with source selections.\n<stage_input>\n"+string(input)+"\n</stage_input>")
	c.ID, c.NodeID, c.WorkflowStepID, c.Attempt, c.NodeKind = "execution", "independent_review", "step", 1, string(workflow.NodeAgentStage)
	c.PromptVersion = "dynamic-independent-review-v9|" + workflow.ReviewSourceProtocolVersion
	c.OutputSchema = schema
	c.OutputSchemaSHA256 = fmt.Sprintf("%x", sha256.Sum256(schema))
	loop.runs = workflowContractRuns{loopState: state, contract: c, found: true}
	state.workflowAI = true
	state.messages[0].Parts = []conversation.MessagePart{{Type: "text", Text: c.PromptText}}
	binding := workflow.ReviewSourceBinding{StageMaterialRequest: workflow.StageMaterialRequest{ProjectID: "project", ConversationID: state.run.ConversationID, WorkflowRunID: c.WorkflowRunID, WorkflowStepID: "step", ChatRunID: state.run.ID, Attempt: 1, InputSHA256: c.InputSHA256}, ExecutionID: c.ID, PromptVersion: c.PromptVersion, OutputSchemaSHA256: c.OutputSchemaSHA256}
	catalog, err := workflow.BuildReviewSourceCatalog(t.Context(), input, binding)
	if err != nil {
		t.Fatal(err)
	}
	h := &firstSourceHostFixture{submissionHostFixture: &submissionHostFixture{staticResearchGuidance: &staticResearchGuidance{bound: true, value: workflow.ResearchGuidance{WorkflowRunID: c.WorkflowRunID, WorkflowStepID: "step", StructuredOutputRequired: true, StructuredOutputSchema: schema}}}, catalog: catalog}
	loop.research = h
	var wire map[string]any
	_ = json.Unmarshal([]byte(base), &wire)
	wire["reviewFindings"].([]any)[0].(map[string]any)["basis"] = []any{map[string]any{"sourceSegmentIds": []string{catalog.Segments[0].ID}, "startOffset": 0, "endOffset": 6}}
	return h, string(mustAgentJSON(t, wire))
}

func TestFirstReviewSourceSubmissionCopiesWithoutPriorFailureAndAuditsSelectors(t *testing.T) {
	loop, state, _ := newLoopFixture(t, nil)
	host, payload := bindFirstSourceFixture(t, loop, state)
	provider := fake.New(submissionToolScript(payload))
	loop.models = fakeResolver{provider}
	if got := loop.Run(t.Context(), state.run.ID); got != OutcomeCompleted {
		t.Fatal(got, state.run.ErrorCode, state.run.ErrorDetails)
	}
	if len(provider.Requests()) != 1 || len(host.validated) != 1 || len(state.calls) != 0 || state.journalDrafts[1] != payload || !strings.Contains(host.validated[0][3], `"quote":"当前誊写原文"`) {
		t.Fatal("first review retyped source, lost raw selector audit or bypassed host", host.validated, state.journalDrafts)
	}
	r := provider.Requests()[0]
	if !r.RequireTool || len(r.Tools) != 1 || !strings.Contains(string(r.Tools[0].InputSchema), "sourceSegmentIds") || strings.Contains(string(r.Tools[0].InputSchema), `"quote"`) {
		t.Fatal("first primary result still requests literal quote")
	}
	if strings.Count(submissionRequestText(r), "<review_submission_sources>") != 1 {
		t.Fatal("first source catalog was duplicated or missing")
	}
}

func TestFirstReviewSourceSubmissionUsesOriginalCorrectionBudgetAndCannotReturnLiteralFullReview(t *testing.T) {
	for _, mode := range []string{"stale_id", "literal_full", "approved_type"} {
		t.Run(mode, func(t *testing.T) {
			loop, state, _ := newLoopFixture(t, nil)
			host, payload := bindFirstSourceFixture(t, loop, state)
			bad := strings.Replace(payload, host.catalog.Segments[0].ID, "r_"+strings.Repeat("a", 64)+"_1", 1)
			if mode == "literal_full" {
				_, bad = issueRepairFixture(t)
			}
			if mode == "approved_type" {
				bad = strings.Replace(payload, `"approved":false`, `"approved":"false"`, 1)
			}
			provider := fake.New(submissionToolScript(bad), submissionToolScript(bad), submissionToolScript(bad), submissionToolScript(payload))
			loop.models = fakeResolver{provider}
			if got := loop.Run(t.Context(), state.run.ID); got != OutcomeFailed || state.run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || len(provider.Requests()) != 3 || len(host.validated) != 0 || len(state.calls) != 0 {
				t.Fatal("literal/stale full submission bypassed copying or renewed budget", got, state.run.ErrorCode, len(provider.Requests()))
			}
			for i := 1; i <= 3; i++ {
				if state.journalDrafts[i] != bad {
					t.Fatal("original rejected selector was rewritten")
				}
			}
			if mode == "stale_id" {
				if !strings.Contains(state.run.ErrorMessage, "原文选择提交") || !strings.Contains(state.run.ErrorMessage, "未进入完整科学校验") {
					t.Fatal("source locator error misreported", state.run.ErrorMessage)
				}
			} else if !strings.Contains(state.run.ErrorMessage, "阶段提交格式") || strings.Contains(state.run.ErrorMessage, "符合当前来源") {
				t.Fatal("wire field error misreported as a source locator defect", state.run.ErrorMessage)
			}
		})
	}
}

func TestFirstReviewSourceSubmissionStillRequiresOriginalScientificAcceptance(t *testing.T) {
	loop, state, _ := newLoopFixture(t, nil)
	host, payload := bindFirstSourceFixture(t, loop, state)
	host.errors = []error{&apperr.Error{Code: "WORKFLOW_AI_SUBMISSION_INVALID", UserMessage: "Actual scientific judgment remains unsupported"}}
	provider := fake.New(submissionToolScript(payload), submissionToolScript(payload))
	loop.models = fakeResolver{provider}
	if got := loop.Run(t.Context(), state.run.ID); got != OutcomeCompleted || len(host.validated) != 2 || len(provider.Requests()) != 2 || !strings.Contains(submissionRequestText(provider.Requests()[1]), "Actual scientific judgment remains unsupported") {
		t.Fatal("copying was treated as scientific approval", got, state.run.ErrorCode)
	}
}

func TestFirstReviewSourceBindingAndContextFailuresNeverSpendModelRequests(t *testing.T) {
	for _, mode := range []string{"host_error", "wrong_execution", "bad_prompt", "wrong_kind"} {
		t.Run(mode, func(t *testing.T) {
			loop, state, provider := newLoopFixture(t, nil)
			host, _ := bindFirstSourceFixture(t, loop, state)
			switch mode {
			case "host_error":
				host.sourceErr = fmt.Errorf("inactive source")
			case "wrong_execution":
				host.catalog.Binding.ExecutionID = "stale"
			case "bad_prompt":
				r := loop.runs.(workflowContractRuns)
				r.contract.PromptSHA256 = "wrong"
				loop.runs = r
			case "wrong_kind":
				r := loop.runs.(workflowContractRuns)
				r.contract.NodeKind = string(workflow.NodeTool)
				loop.runs = r
			}
			if got := loop.Run(t.Context(), state.run.ID); got != OutcomeFailed || len(provider.Requests()) != 0 || state.run.ErrorCode != "WORKFLOW_REVIEW_SOURCE_BINDING_INVALID" {
				t.Fatal("bad host source consumed model requests", got, state.run.ErrorCode)
			}
		})
	}
	loop, state, _ := newLoopFixture(t, nil)
	host, _ := bindFirstSourceFixture(t, loop, state)
	s := &reviewSourceSubmission{catalog: host.catalog, fullSchema: workflow.ReviewSourceSubmissionSchema(workflow.ReviewSubmissionSchema(loop.runs.(workflowContractRuns).contract.OutputSchema))}
	admitted := model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: strings.Repeat("CURRENT_EVIDENCE ", 400)}}}
	limit := currentRequestBudgetTokens(admitted, nil)
	got, err := s.request(admitted, nil, limit)
	if err == nil || !reflect.DeepEqual(got, admitted) {
		t.Fatal("mandatory source catalog displaced evidence or silently became literal mode", err)
	}
}

func TestFirstReviewSourceLegacyAndMissingNativeResponsesKeepExplicitBoundaries(t *testing.T) {
	for _, mode := range []string{"legacy", "text_only", "mixed", "empty_complete"} {
		t.Run(mode, func(t *testing.T) {
			loop, state, _ := newLoopFixture(t, nil)
			host, payload := bindFirstSourceFixture(t, loop, state)
			script := submissionToolScript(payload)
			if mode == "legacy" {
				runs := loop.runs.(workflowContractRuns)
				runs.contract.PromptVersion = "dynamic-independent-review-v9"
				loop.runs = runs
				_, base := issueRepairFixture(t)
				script = submissionToolScript(base)
			}
			if mode == "text_only" {
				script = submissionScript(payload)
			}
			if mode == "mixed" {
				other := submissionToolScript(payload)[0]
				other.Event.ToolCall.ID = "extra"
				script = []fake.Step{script[0], other, script[1]}
			}
			if mode == "empty_complete" {
				script = []fake.Step{{Event: model.Event{Type: model.EventDone, FinishReason: "stop"}}}
			}
			provider := fake.New(script, submissionToolScript(payload))
			loop.models = fakeResolver{provider}
			got := loop.Run(t.Context(), state.run.ID)
			if mode == "legacy" {
				if got != OutcomeCompleted || strings.Contains(submissionRequestText(provider.Requests()[0]), "<review_submission_sources>") {
					t.Fatal("old execution hot-upgraded", got, state.run.ErrorCode)
				}
			} else if got != OutcomeFailed || len(host.validated) != 0 || len(state.calls) != 0 || len(provider.Requests()) != 1 {
				t.Fatal("missing/mixed native action was accepted or retried", got, state.run.ErrorCode)
			}
			if mode == "empty_complete" && (state.run.ModelTurns != 1 || state.journalDrafts[1] != "" || state.run.ErrorCode != "WORKFLOW_AI_NATIVE_ACTION_MISSING") {
				t.Fatal("complete empty action was retried or not precisely classified", state.run.ErrorCode, state.journalDrafts)
			}
		})
	}
}
